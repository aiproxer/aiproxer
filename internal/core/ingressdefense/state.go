package ingressdefense

import (
	"math"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// stateShardCount is the upper bound of independent shards. One exact source
	// address always maps to exactly one shard, so unrelated addresses never wait
	// on each other's lock and no single lock spans unrelated request work.
	stateShardCount = 32
	// maxOffenseLevel saturates the accumulated offense level. At this level the
	// exponential backoff is already far past every configured ceiling, so a
	// larger level cannot change any observable quarantine. It also bounds the
	// doubling loop in quarantineDuration.
	maxOffenseLevel = 64
)

// entry is the bounded adaptive state of one exact source address. It retains
// only the normative counters, penalty state and timestamps: the address itself
// is the map key, and no credential, principal, body, prompt, complete path,
// header, User-Agent or other attacker-controlled string is ever stored. The
// design's Adaptive entry model carries exactly these fields plus one internal
// admission counter, which is bounded ring bookkeeping rather than adaptive
// state and exists so a ring slot can prove it still owns the entry it names.
type entry struct {
	windowStartedAt time.Time
	failures        int
	offenseLevel    int
	quarantineUntil time.Time
	lastHostileAt   time.Time
	// admission is the identity of the admission that created this entry. It
	// makes a live entry distinguishable from a later admission of the same
	// address, which is what lets a ring slot prove it still owns the entry it
	// names. It is not security state and never widens or narrows an offense.
	admission uint64
}

// admissionSlot is one occupied position of a shard's admission ring. A slot owns
// an entry only while the live entry for addr still carries the same admission
// identity, so a slot left behind by Clear or by a TTL expiry can never evict the
// newer entry a later re-admission of the same address created.
type admissionSlot struct {
	addr      netip.Addr
	admission uint64
}

// shard is one independently locked slice of the process state. Its capacity is
// fixed at construction, so the total entry count can never exceed MaxEntries
// regardless of how many unique addresses arrive.
type shard struct {
	mu      sync.Mutex
	entries map[netip.Addr]*entry
	// order is the insertion-order ring of this shard's admissions and cursor is
	// its eviction cursor. See reserve for the deterministic eviction rule.
	order  []admissionSlot
	cursor int
	cap    int
	count  atomic.Int64
	// nextAdmission issues the per-shard admission identity stamped on every new
	// entry. Zero is never issued, so a slot written before any admission can
	// never alias a live entry.
	nextAdmission uint64
}

// State is the bounded, process-local, exact-address adaptive hostile-source
// state. It is process-owned and shared by every immutable generation: request
// policy arrives per call, capacity and inactivity TTL are fixed at
// construction, and nothing is persisted. The state owns no clock, no goroutine,
// no timer and no I/O: every operation takes the current instant from its caller
// so expiry is lazy on lookup and mutation and shutdown needs no cleanup.
//
// A lock is held only for one map/ring mutation. No lock is ever held while
// calling authentication, HTTP handlers, logging, metrics or any other code.
//
// Caller obligations, because the process-owned store outlives every generation
// and only the two recorders can see a per-generation policy: a caller must
// check Policy.Enabled before consulting this state at all, since the process
// store survives a reload that disables self-defense, and it must apply
// Policy.AdaptiveExempt before IsQuarantined, before RecordProbe, and before
// RecordAuthFailure.
type State struct {
	limits StateLimits
	shards []shard
}

// NewState constructs the bounded process state for the given limits. Shard count
// is min(stateShardCount, MaxEntries) and each shard's capacity is MaxEntries
// divided across the shards, so the sum of shard capacities equals MaxEntries
// exactly and no address can be tracked beyond it.
func NewState(limits StateLimits) (*State, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	shards := min(stateShardCount, limits.MaxEntries)
	s := &State{limits: limits, shards: make([]shard, shards)}
	base, extra := limits.MaxEntries/shards, limits.MaxEntries%shards
	for i := range s.shards {
		s.shards[i].entries = make(map[netip.Addr]*entry)
		s.shards[i].cap = base
		if i < extra {
			s.shards[i].cap++
		}
	}
	return s, nil
}

// Len reports the number of tracked source entries. It is an eventually exact
// gauge: every entry change is applied under a shard lock, so a quiescent state
// reports exactly and a concurrent caller may observe a neighbouring instant.
func (s *State) Len() int {
	total := 0
	for i := range s.shards {
		total += int(s.shards[i].count.Load())
	}
	return total
}

// IsQuarantined reports whether addr is quarantined at now. A read is not
// hostile activity: it never refreshes lastHostileAt, so a source cannot keep
// alive state it never attacks again. It does expire an entry that has been
// inactive for StateTTL, so a read path is also a lazy expiry path.
func (s *State) IsQuarantined(addr netip.Addr, now time.Time) bool {
	key, ok := stateKey(addr)
	if !ok {
		return false
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	found, live := sh.lookup(key, now, s.limits.StateTTL)
	return live && now.Before(found.quarantineUntil)
}

// RecordAuthFailure records one counted unauthenticated-failure offense for addr
// and starts or escalates a quarantine when the configured threshold is reached
// inside the failure window. The window begins with the first counted failure.
//
// This is the only auth-evidence entry point, so a 403, a 5xx, a provider error
// or a request that already established a principal cannot be recorded here:
// the classification of an authentication outcome is the auth adapter's decision,
// not this package's. Adaptive exemption is likewise the gate's decision. A
// counted failure below the threshold reports the zero Transition so the closed
// reason vocabulary stays reserved for quarantine transitions.
func (s *State) RecordAuthFailure(addr netip.Addr, now time.Time, p Policy) Transition {
	if !p.Enabled {
		return Transition{EntryCount: s.Len()}
	}
	key, ok := stateKey(addr)
	if !ok {
		return Transition{EntryCount: s.Len()}
	}
	threshold := max(p.AuthFailures, 1)

	sh := s.shardFor(key)
	sh.mu.Lock()
	found, live := sh.touch(key, now, s.limits.StateTTL)
	if !live || now.Sub(found.windowStartedAt) >= p.FailureWindow {
		found.windowStartedAt = now
		found.failures = 1
	} else {
		found.failures++
	}
	if found.failures < threshold {
		sh.mu.Unlock()
		return Transition{EntryCount: s.Len()}
	}
	found.failures = 0
	found.windowStartedAt = time.Time{}
	until := sh.penalize(found, now, p)
	sh.mu.Unlock()
	return Transition{QuarantineStarted: true, QuarantineUntil: until, Reason: ReasonAuthFailureThreshold, EntryCount: s.Len()}
}

// RecordProbe records one strong hostile offense for addr: a matched
// impossible-path target is a deterministic rejection, so it counts immediately
// as a single offense on the same offense level the thresholded auth-failure
// window escalates. There is no weighted scoring engine behind it. The
// authentication window is left untouched because a path offense is not
// authentication evidence.
func (s *State) RecordProbe(addr netip.Addr, now time.Time, p Policy) Transition {
	if !p.Enabled {
		return Transition{EntryCount: s.Len()}
	}
	key, ok := stateKey(addr)
	if !ok {
		return Transition{EntryCount: s.Len()}
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	found, _ := sh.touch(key, now, s.limits.StateTTL)
	until := sh.penalize(found, now, p)
	sh.mu.Unlock()
	return Transition{QuarantineStarted: true, QuarantineUntil: until, Reason: ReasonImpossiblePath, EntryCount: s.Len()}
}

// Clear deletes the exact-address entry after a successful full authentication
// chain, discarding its accumulated offense level, and reports the number of
// tracked source entries afterwards. It is a reset, never a trust cache: a later
// hostile event starts a fresh offense level and a prior success never exempts a
// later impossible-path request.
func (s *State) Clear(addr netip.Addr) int {
	key, ok := stateKey(addr)
	if !ok {
		return s.Len()
	}
	sh := s.shardFor(key)
	sh.mu.Lock()
	if _, tracked := sh.entries[key]; tracked {
		delete(sh.entries, key)
		sh.count.Add(-1)
	}
	sh.mu.Unlock()
	return s.Len()
}

// penalize raises the offense level, computes the saturating exponential
// quarantine and extends the active deadline with max(existing, now+duration).
// The caller holds the shard lock and owns found.
func (sh *shard) penalize(found *entry, now time.Time, p Policy) time.Time {
	found.offenseLevel = nextOffenseLevel(found.offenseLevel)
	duration := quarantineDuration(found.offenseLevel, p.InitialQuarantine, p.MaxQuarantine)
	until := now.Add(duration)
	if until.Before(found.quarantineUntil) {
		until = found.quarantineUntil
	}
	found.quarantineUntil = until
	return until
}

// lookup returns the live entry for key, and whether one exists. An entry whose
// hostile inactivity reached the state TTL is dropped before use. Unlike touch
// it never refreshes lastHostileAt, because a read is not hostile activity. The
// caller holds the shard lock.
func (sh *shard) lookup(key netip.Addr, now time.Time, ttl time.Duration) (*entry, bool) {
	found, ok := sh.entries[key]
	if !ok {
		return nil, false
	}
	if now.Sub(found.lastHostileAt) < ttl {
		return found, true
	}
	delete(sh.entries, key)
	sh.count.Add(-1)
	return nil, false
}

// touch returns the entry for one recorded hostile event, creating it when the
// address is new or its previous entry already expired, and whether the returned
// entry survived the inactivity check. It refreshes lastHostileAt, so expiry is
// lazy on every mutation. The caller holds the shard lock and owns the returned
// pointer.
func (sh *shard) touch(key netip.Addr, now time.Time, ttl time.Duration) (*entry, bool) {
	found, ok := sh.entries[key]
	if ok && now.Sub(found.lastHostileAt) < ttl {
		found.lastHostileAt = now
		return found, true
	}
	if ok {
		delete(sh.entries, key)
		sh.count.Add(-1)
	}
	found = &entry{lastHostileAt: now, admission: sh.issueAdmission()}
	sh.reserve(key, found.admission)
	sh.entries[key] = found
	sh.count.Add(1)
	return found, false
}

// issueAdmission returns a fresh, non-zero admission identity for one new entry.
// The counter is per shard and monotonic, so the identity is unique among the
// live entries of that shard. It is skipped over on wrap so it never returns
// zero; aliasing a stale slot would therefore need 2^64 admissions into one
// single shard, which no process reaches.
func (sh *shard) issueAdmission() uint64 {
	sh.nextAdmission++
	if sh.nextAdmission == 0 {
		sh.nextAdmission++
	}
	return sh.nextAdmission
}

// reserve makes room for one more address inside this shard's fixed capacity.
//
// Deterministic bounded eviction rule: the shard keeps an insertion-order ring of
// the admissions it has made. While the ring is still filling, a new admission
// appends to it. Once it is full, every new admission unconditionally replaces the
// slot under the cursor and advances the cursor, so an evicted admission is the
// least recently ADMITTED one still owning its slot. The ring records admission
// order, not liveness: a slot whose entry was already dropped by lazy expiry or
// by Clear is overwritten without evicting any live entry, and such an admission
// still advances the cursor. The mapping from address to shard is a fixed FNV-1a
// hash, so the admitted set after any sequence of addresses is reproducible.
//
// A slot evicts only the entry it still owns. Ownership is the pair (address,
// admission identity), not the address alone: Clear and TTL expiry deliberately
// leave the slot behind, so after an address is dropped and admitted again the
// ring can hold the same address twice, and the stale slot must not delete the
// live entry the newer admission created. Comparing the live entry's identity
// with the slot's identity keeps the double-admission case bounded and exact
// without scanning the ring or taking any lock beyond the one already held.
//
// Eviction only ever discards the evicted address's own state. It never widens an
// offense, never denies an address that owns no state, and never rejects traffic
// globally: a lost entry costs the attacker their own accumulated offense level,
// which is the fail-open direction.
func (sh *shard) reserve(key netip.Addr, admission uint64) {
	if len(sh.order) < sh.cap {
		sh.order = append(sh.order, admissionSlot{addr: key, admission: admission})
		return
	}
	victim := sh.order[sh.cursor]
	sh.order[sh.cursor] = admissionSlot{addr: key, admission: admission}
	sh.cursor = (sh.cursor + 1) % sh.cap
	if live, tracked := sh.entries[victim.addr]; tracked && live.admission == victim.admission {
		delete(sh.entries, victim.addr)
		sh.count.Add(-1)
	}
}

// stateKey normalizes one source address into the exact-address state key. The
// driving adapter already resolves the trusted client address before calling the
// state; the state still unmaps the IPv4-in-IPv6 representation defensively so one
// host can never occupy two entries and cannot be used to double-count itself. It
// never truncates to a prefix and never drops a zone, so two distinct addresses
// never share an entry and no aggregate network state can exist.
func stateKey(addr netip.Addr) (netip.Addr, bool) {
	if !addr.IsValid() {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (s *State) shardFor(key netip.Addr) *shard {
	return &s.shards[s.shardIndex(key)]
}

// shardIndex is the deterministic address-to-shard mapping. The IPv4 and
// IPv4-in-IPv6 forms of one host share the same 16-byte representation and
// therefore the same shard.
func (s *State) shardIndex(key netip.Addr) int {
	raw := key.As16()
	hash := uint32(2166136261)
	for _, b := range raw {
		hash = (hash ^ uint32(b)) * 16777619
	}
	return int(hash % uint32(len(s.shards)))
}

// nextOffenseLevel raises the offense level without ever overflowing it.
func nextOffenseLevel(level int) int {
	if level >= maxOffenseLevel {
		return maxOffenseLevel
	}
	return level + 1
}

// quarantineDuration returns min(initial * 2^(level-1), limit) without ever
// overflowing time.Duration: doubling stops as soon as the configured ceiling is
// reached or the next doubling would exceed the int64 nanosecond range. The only
// call path raises the offense level before computing the duration, so a
// non-positive level performs no doubling and yields the unchanged first-offense
// duration, never a shorter one, except at exactly math.MinInt, where level-1
// wraps to MaxInt and the ladder doubles to the ceiling. A non-positive
// first-offense duration returns the ceiling, so a non-positive
// initial_quarantine with a positive max_quarantine still quarantines; a
// non-positive ceiling returns that ceiling, which puts the deadline in the past,
// and Policy.Validate rejects every policy that reaches either position.
func quarantineDuration(level int, initial, limit time.Duration) time.Duration {
	if initial <= 0 {
		return limit
	}
	duration := initial
	for range level - 1 {
		if duration >= limit || duration > time.Duration(math.MaxInt64/2) {
			return limit
		}
		duration *= 2
	}
	if duration > limit {
		return limit
	}
	return duration
}
