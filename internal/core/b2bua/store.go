package b2bua

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Sentinel errors for A-leg continuity and validation.
var (
	ErrALegNotFound         = errors.New("b2bua: a-leg not found")
	ErrInvalidContinuityKey = errors.New("b2bua: continuity key required for resolve")
	ErrInvalidAttempt       = errors.New("b2bua: invalid attempt record")
	ErrInvalidMaxLegs       = errors.New("b2bua: max_legs must be non-negative")
	// ErrInterleavedStateUnsupported is returned when an operation targets a
	// store that does not implement InterleavedStateStore and the requested
	// state is non-empty. Empty state remains a no-op so callers can treat
	// unsupported lineage as a new-session equivalent.
	ErrInterleavedStateUnsupported = errors.New("b2bua: interleaved state not supported by store")
)

// noCopy signals go vet's copylocks analyzer to reject accidental copies.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// ALegRecord is the core-owned logical session row for routing and lineage.
type ALegRecord struct {
	ALegID                string
	ContinuityKey         string
	CreatedAt             time.Time
	LastSeenAt            time.Time
	WeightedFirstConsumed bool
}

// BLegRecord identifies one backend attempt slot within an A-leg.
type BLegRecord struct {
	BLegID string
	ALegID string
	Seq    int
}

// Store persists A-leg continuity, allocates B-leg sequence numbers, and records attempt lineage.
type Store interface {
	ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	FetchALeg(ctx context.Context, aLegID string) (ALegRecord, error)
	SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error
	NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error)
	RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error
	LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error)
}

// InterleavedStateStore is the narrow optional contract for persisting
// thinker cycle state and memo references under A-leg continuity authority.
//
// It is intentionally separate from Store so pkg/aipsdk/continuity can mirror
// only the base continuity contract without importing internal/core/interleavedstate
// (archtests forbid pkg/aipsdk from depending on internal/). Durable and in-memory
// stores implement this in addition to Store; callers type-assert or accept this
// interface explicitly. A zero-value state is valid and means "no thinker state",
// which is backward-compatible with A-legs created before interleaved thinking.
// ALegRetirementObserver is an optional lifecycle seam. Stores call the observer
// only after completing the eviction mutation and releasing their own locks.
type ALegRetirementObserver interface {
	SetALegRetirementObserver(func(string))
}

// Retirement observer keys name the owner of a registered retirement
// cleanup. The process-owned cleanup and the published generation's cleanup
// register under different keys, so neither registration displaces the other.
const (
	// RetirementObserverProcess is the key of the process-owned cleanup.
	RetirementObserverProcess = "process"
	// RetirementObserverGeneration is the key of the published generation's
	// cleanup.
	RetirementObserverGeneration = "generation"
)

// ALegRetirementObservers is an optional composition seam for continuity
// stores that accept more than one retirement observer. The store composes
// them: a retirement notifies every registered observer.
//
// A keyed observer replaces the observer previously registered under the same
// key, so a published generation takes over the generation-scoped cleanup
// without leaking the previous generation's callback. The core only forwards:
// the feature policy stays with the feature that owns it, and the store never
// learns what a cleanup does.
type ALegRetirementObservers interface {
	SetKeyedALegRetirementObserver(key string, observer func(string))
}

type InterleavedStateStore interface {
	SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error
	FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error)
}

// MemoryStoreOptions configures the in-memory implementation.
type MemoryStoreOptions struct {
	// TTL after LastSeenAt after which an A-leg is lazily evicted. Zero disables expiry.
	// Non-zero TTL also enables a sweep on CreateALeg so idle sessions that are never
	// touched again (e.g. anonymous one-shot A-legs) are still reclaimed.
	TTL time.Duration
	// MaxLegs caps how many concurrent A-leg rows may be retained. Zero selects
	// DefaultMemoryStoreMaxLegsWithoutTTL (including when TTL-based expiry is enabled).
	// Negative values are rejected by NewMemoryStore.
	MaxLegs int
	// Now returns the current time; when nil, NewMemoryStore uses time.Now.
	Now func() time.Time
}

// DefaultMemoryStoreMaxLegsWithoutTTL is applied when TTL is disabled and MaxLegs is zero,
// preventing unbounded growth of anonymous sessions in long-lived processes.
const DefaultMemoryStoreMaxLegsWithoutTTL = 100_000

// MemoryStore is a mutex-protected in-memory Store with lazy TTL eviction.
type MemoryStore struct {
	_       noCopy
	ttl     time.Duration
	maxLegs int
	now     func() time.Time
	mu      sync.RWMutex
	legs    map[string]*legState // aLegID -> state
	// continuityKey (non-empty) -> current aLegID for Resolve
	byKey     map[string]string
	onRetired func(string)
	// retirementObservers holds the keyed retirement callbacks. The store
	// composes them with the default callback: a retirement notifies every
	// registered observer.
	retirementObservers map[string]func(string)
}

var (
	_ Store                 = (*MemoryStore)(nil)
	_ InterleavedStateStore = (*MemoryStore)(nil)
	_ routeoverride.Store   = (*MemoryStore)(nil)
)

type legState struct {
	record       ALegRecord
	nextSeq      int
	seqToBLeg    map[int]string
	attemptBySeq map[int]aipapi.AttemptRecord
	interleaved  interleavedstate.State
	override     routeoverride.State
}

// NewMemoryStore returns an empty store. opts may be zero-valued defaults.
func NewMemoryStore(opts MemoryStoreOptions) (*MemoryStore, error) {
	if opts.MaxLegs < 0 {
		return nil, ErrInvalidMaxLegs
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	maxLegs := opts.MaxLegs
	if maxLegs <= 0 {
		// Cap concurrent A-legs when MaxLegs is unset, including TTL mode (lazy eviction alone
		// can allow unbounded growth under bursty unique continuity keys).
		maxLegs = DefaultMemoryStoreMaxLegsWithoutTTL
	}
	return &MemoryStore{
		ttl:     opts.TTL,
		maxLegs: maxLegs,
		now:     now,
		legs:    make(map[string]*legState),
		byKey:   make(map[string]string),
	}, nil
}

func (s *MemoryStore) nowTime() time.Time { return s.now() }

func (s *MemoryStore) lockForOperation() []string {
	s.mu.Lock()
	return nil
}

func (s *MemoryStore) unlockForOperation(retired []string) {
	s.mu.Unlock()
	if len(retired) == 0 {
		return
	}
	s.mu.RLock()
	observer := s.onRetired
	keyed := make([]func(string), 0, len(s.retirementObservers))
	for _, candidate := range s.retirementObservers {
		keyed = append(keyed, candidate)
	}
	s.mu.RUnlock()
	if observer == nil && len(keyed) == 0 {
		return
	}
	for _, aLegID := range retired {
		if observer != nil {
			observer(aLegID)
		}
		for _, candidate := range keyed {
			candidate(aLegID)
		}
	}
}

// ResolveALeg returns the active A-leg for a non-empty continuity key, refreshing LastSeenAt.
func (s *MemoryStore) ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	if continuityKey == "" {
		return ALegRecord{}, ErrInvalidContinuityKey
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	now := s.nowTime()
	aID, ok := s.byKey[continuityKey]
	if !ok {
		return ALegRecord{}, ErrALegNotFound
	}
	st, ok := s.legs[aID]
	if !ok {
		delete(s.byKey, continuityKey)
		return ALegRecord{}, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, now) {
		retired = append(retired, st.record.ALegID)
		return ALegRecord{}, ErrALegNotFound
	}
	st.record.LastSeenAt = now
	return st.record, nil
}

// CreateALeg allocates a new A-leg. Empty continuityKey skips key registration (always-new session).
func (s *MemoryStore) CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	now := s.nowTime()
	retired = append(retired, s.sweepExpiredLegsLocked(now)...)
	aID, err := RandomALegID()
	if err != nil {
		return ALegRecord{}, fmt.Errorf("b2bua: allocate a-leg id: %w", err)
	}
	rec := ALegRecord{
		ALegID:        aID,
		ContinuityKey: continuityKey,
		CreatedAt:     now,
		LastSeenAt:    now,
	}
	st := &legState{
		record:       rec,
		seqToBLeg:    make(map[int]string),
		attemptBySeq: make(map[int]aipapi.AttemptRecord),
	}
	s.legs[aID] = st
	if continuityKey != "" {
		if oldID, ok := s.byKey[continuityKey]; ok && oldID != aID {
			if retiredID := s.removeLegLocked(oldID); retiredID != "" {
				retired = append(retired, retiredID)
			}
		}
		s.byKey[continuityKey] = aID
	}
	retired = append(retired, s.enforceMaxLegsLocked()...)
	return rec, nil
}

// FetchALeg loads an A-leg by id (for clients that already hold ALegID).
func (s *MemoryStore) FetchALeg(ctx context.Context, aLegID string) (ALegRecord, error) {
	if err := ctx.Err(); err != nil {
		return ALegRecord{}, err
	}
	if aLegID == "" {
		return ALegRecord{}, ErrALegNotFound
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return ALegRecord{}, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return ALegRecord{}, ErrALegNotFound
	}
	st.record.LastSeenAt = s.nowTime()
	return st.record, nil
}

// SetWeightedFirstConsumed updates session first-request routing state (idempotent).
func (s *MemoryStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return ErrALegNotFound
	}
	st.record.WeightedFirstConsumed = consumed
	st.record.LastSeenAt = s.nowTime()
	return nil
}

// SetInterleavedState stores the thinker cycle state and memo reference for an
// A-leg. An empty state is permitted and clears any previously stored state.
// The state is validated before storage; invalid state is rejected without
// mutating the stored value.
func (s *MemoryStore) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("b2bua: invalid interleaved state: %w", err)
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return ErrALegNotFound
	}
	st.interleaved = state
	st.record.LastSeenAt = s.nowTime()
	return nil
}

// FetchInterleavedState returns the thinker cycle state and memo reference for
// an A-leg. A leg with no stored state returns the zero value, which is a
// harmless "new session" state for cycle purposes.
func (s *MemoryStore) FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	if err := ctx.Err(); err != nil {
		return interleavedstate.State{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return interleavedstate.State{}, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return interleavedstate.State{}, ErrALegNotFound
	}
	st.record.LastSeenAt = s.nowTime()
	return st.interleaved, nil
}

// NextBLeg allocates the next monotonic B-leg id and sequence for the A-leg.
func (s *MemoryStore) NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error) {
	if err := ctx.Err(); err != nil {
		return BLegRecord{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return BLegRecord{}, ErrALegNotFound
	}
	now := s.nowTime()
	if s.evictIfStaleLocked(st, now) {
		retired = append(retired, st.record.ALegID)
		return BLegRecord{}, ErrALegNotFound
	}
	st.nextSeq++
	seq := st.nextSeq
	bid, err := RandomBLegID()
	if err != nil {
		return BLegRecord{}, fmt.Errorf("b2bua: allocate b-leg id: %w", err)
	}
	st.seqToBLeg[seq] = bid
	st.record.LastSeenAt = now
	return BLegRecord{BLegID: bid, ALegID: aLegID, Seq: seq}, nil
}

// RecordAttempt upserts one lineage row for (ALegID, Seq). BLegID must match the allocation from NextBLeg.
func (s *MemoryStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rec.ALegID == "" || rec.Seq <= 0 || rec.BLegID == "" {
		return fmt.Errorf("%w: missing ids or seq", ErrInvalidAttempt)
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[rec.ALegID]
	if !ok {
		return ErrALegNotFound
	}
	now := s.nowTime()
	if s.evictIfStaleLocked(st, now) {
		retired = append(retired, st.record.ALegID)
		return ErrALegNotFound
	}
	if rec.Seq > st.nextSeq {
		return fmt.Errorf("%w: seq %d beyond allocated next %d", ErrInvalidAttempt, rec.Seq, st.nextSeq)
	}
	wantBLeg, ok := st.seqToBLeg[rec.Seq]
	if !ok || wantBLeg != rec.BLegID {
		return fmt.Errorf("%w: b-leg mismatch for seq %d", ErrInvalidAttempt, rec.Seq)
	}
	st.attemptBySeq[rec.Seq] = rec
	st.record.LastSeenAt = now
	return nil
}

// LoadAttempts returns attempt rows for an A-leg ordered by ascending Seq.
func (s *MemoryStore) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	st, ok := s.legs[aLegID]
	if !ok {
		return nil, ErrALegNotFound
	}
	if s.evictIfStaleLocked(st, s.nowTime()) {
		retired = append(retired, st.record.ALegID)
		return nil, ErrALegNotFound
	}
	st.record.LastSeenAt = s.nowTime()
	out := make([]aipapi.AttemptRecord, 0, len(st.attemptBySeq))
	for _, r := range st.attemptBySeq {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b aipapi.AttemptRecord) int {
		return cmp.Compare(a.Seq, b.Seq)
	})
	return out, nil
}

// SetALegRetirementObserver binds the process-owned session retirement callback.
func (s *MemoryStore) SetALegRetirementObserver(observer func(string)) {
	s.mu.Lock()
	s.onRetired = observer
	s.mu.Unlock()
}

// SetKeyedALegRetirementObserver binds one keyed session retirement callback.
// The callback previously bound under the same key is replaced.
func (s *MemoryStore) SetKeyedALegRetirementObserver(key string, observer func(string)) {
	s.mu.Lock()
	if s.retirementObservers == nil {
		s.retirementObservers = map[string]func(string){}
	}
	if observer == nil {
		delete(s.retirementObservers, key)
	} else {
		s.retirementObservers[key] = observer
	}
	s.mu.Unlock()
}

func (s *MemoryStore) evictIfStaleLocked(st *legState, now time.Time) bool {
	if s.ttl <= 0 {
		return false
	}
	if now.Sub(st.record.LastSeenAt) < s.ttl {
		return false
	}
	s.removeLegLocked(st.record.ALegID)
	return true
}

// sweepExpiredLegsLocked removes every leg whose LastSeenAt is older than TTL.
// Called from CreateALeg so idle sessions (never re-accessed) are still reclaimed.
func (s *MemoryStore) sweepExpiredLegsLocked(now time.Time) []string {
	if s.ttl <= 0 {
		return nil
	}
	stale := make([]string, 0, len(s.legs))
	for id, st := range s.legs {
		if now.Sub(st.record.LastSeenAt) >= s.ttl {
			stale = append(stale, id)
		}
	}
	retired := make([]string, 0, len(stale))
	for _, id := range stale {
		if retiredID := s.removeLegLocked(id); retiredID != "" {
			retired = append(retired, retiredID)
		}
	}
	return retired
}

func (s *MemoryStore) removeLegLocked(aLegID string) string {
	st, ok := s.legs[aLegID]
	if !ok {
		return ""
	}
	delete(s.legs, aLegID)
	continuityKey := st.record.ContinuityKey
	if continuityKey != "" {
		if cur, ok := s.byKey[continuityKey]; ok && cur == aLegID {
			delete(s.byKey, continuityKey)
		}
	}
	return aLegID
}

func (s *MemoryStore) enforceMaxLegsLocked() []string {
	retired := make([]string, 0)
	for s.maxLegs > 0 && len(s.legs) > s.maxLegs {
		victim := ""
		var oldest time.Time
		first := true
		for id, st := range s.legs {
			if first {
				victim = id
				oldest = st.record.LastSeenAt
				first = false
				continue
			}
			if st.record.LastSeenAt.Before(oldest) {
				victim = id
				oldest = st.record.LastSeenAt
			} else if st.record.LastSeenAt.Equal(oldest) && id < victim {
				victim = id
			}
		}
		if victim == "" {
			break
		}
		if retiredID := s.removeLegLocked(victim); retiredID != "" {
			retired = append(retired, retiredID)
		}
	}
	return retired
}
