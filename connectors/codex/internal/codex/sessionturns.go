package codex

import (
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	sessionTurnTTL        = time.Hour
	sessionTurnMaxEntries = 1024
)

// sessionTurnCounter tracks, per session key, which backend round-trips
// ("turns") have been reserved so the openai-codex connector can apply early-
// and mid-session verbosity bumps against a single shared turn number. The key
// prefers the proxy-owned session identity when available, so concurrent
// sessions handled by the same backend instance do not share turn counters. It
// is in-memory, per-process, TTL-bounded, and LRU-capped, mirroring
// wsContinuationStore. A nil receiver is safe: reserve returns 0 and
// release/commit are no-ops.
//
// Turn allocation is atomic under the mutex: reserveTurn assigns a unique turn
// number immediately so concurrent opens for the same session key cannot both
// observe the same turn. Callers must releaseTurn when the open ultimately
// fails, or commitTurn when it succeeds, so in-flight tickets are tracked
// separately from the committed high-water mark. TTL expiry and capacity
// eviction skip entries that still have in-flight reservations.
type sessionTurnCounter struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]sessionTurnEntry
	order      []string
	now        func() time.Time
}

type sessionTurnEntry struct {
	nextTurn  int
	committed int
	inflight  map[int]struct{}
	expiresAt time.Time
}

func newSessionTurnCounter(ttl time.Duration, maxEntries int) *sessionTurnCounter {
	if ttl <= 0 {
		ttl = sessionTurnTTL
	}
	if maxEntries <= 0 {
		maxEntries = sessionTurnMaxEntries
	}
	return &sessionTurnCounter{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]sessionTurnEntry),
		now:        time.Now,
	}
}

func verbosityTurnsEnabled(cfg Config) bool {
	return !cfg.EarlySessionVerbosityBumpDisabled || !cfg.MidSessionVerbosityBumpDisabled
}

// currentTurnNumber reports the next turn number that reserveTurn would assign
// without mutating state. Turn 1 is returned for a new key, and 0 is returned
// when the key is empty or the counter is nil.
func (s *sessionTurnCounter) currentTurnNumber(convID string) int {
	if s == nil || strings.TrimSpace(convID) == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[convID]
	if !ok || entry.nextTurn <= 0 {
		return 1
	}
	return entry.nextTurn
}

// reserveTurn allocates the next turn number for the given key under the lock
// and returns it. Empty ids and nil receivers return 0. Callers must releaseTurn
// on failure paths or commitTurn on success so in-flight tickets are cleared.
func (s *sessionTurnCounter) reserveTurn(convID string) int {
	if s == nil || strings.TrimSpace(convID) == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry := s.entries[convID]
	if entry.nextTurn <= 0 {
		entry.nextTurn = 1
	}
	turnNo := entry.nextTurn
	entry.nextTurn++
	if entry.inflight == nil {
		entry.inflight = make(map[int]struct{})
	}
	entry.inflight[turnNo] = struct{}{}
	entry.expiresAt = s.now().Add(s.ttl)
	s.entries[convID] = entry
	s.touchLocked(convID)
	s.evictIdleOverflowLocked()
	return turnNo
}

// releaseTurn undoes a prior reserveTurn for the given key and turn number after
// a failed open. It is a no-op when the key is unknown, empty, or the turn was
// already released or committed. It never rewinds past the committed high-water
// mark from successful opens.
func (s *sessionTurnCounter) releaseTurn(convID string, turnNo int) {
	if s == nil || strings.TrimSpace(convID) == "" || turnNo <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[convID]
	if !ok || entry.nextTurn <= 0 || entry.inflight == nil {
		return
	}
	if _, ok := entry.inflight[turnNo]; !ok {
		return
	}
	delete(entry.inflight, turnNo)
	if turnNo == entry.nextTurn-1 {
		for entry.nextTurn > entry.committed+1 {
			if _, ok := entry.inflight[entry.nextTurn-1]; ok {
				break
			}
			entry.nextTurn--
		}
	}
	s.storeOrDeleteLocked(convID, entry)
}

// commitTurn marks a reserved turn as successfully completed. The turn number
// remains consumed (nextTurn is not rewound), but the in-flight ticket is
// cleared so idle TTL/eviction can reclaim the entry later.
func (s *sessionTurnCounter) commitTurn(convID string, turnNo int) {
	if s == nil || strings.TrimSpace(convID) == "" || turnNo <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked()
	entry, ok := s.entries[convID]
	if !ok || entry.inflight == nil {
		return
	}
	if _, ok := entry.inflight[turnNo]; !ok {
		return
	}
	delete(entry.inflight, turnNo)
	if turnNo > entry.committed {
		entry.committed = turnNo
	}
	entry.expiresAt = s.now().Add(s.ttl)
	s.entries[convID] = entry
	s.touchLocked(convID)
}

func (s *sessionTurnCounter) storeOrDeleteLocked(convID string, entry sessionTurnEntry) {
	if len(entry.inflight) == 0 && entry.committed == 0 && entry.nextTurn <= 1 {
		delete(s.entries, convID)
		out := s.order[:0]
		for _, existing := range s.order {
			if existing != convID {
				out = append(out, existing)
			}
		}
		s.order = out
		return
	}
	entry.expiresAt = s.now().Add(s.ttl)
	s.entries[convID] = entry
	s.touchLocked(convID)
}

func (s *sessionTurnCounter) purgeExpiredLocked() {
	now := s.now()
	out := s.order[:0]
	for _, key := range s.order {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		if len(entry.inflight) == 0 && !entry.expiresAt.After(now) {
			delete(s.entries, key)
			continue
		}
		out = append(out, key)
	}
	s.order = out
}

// evictIdleOverflowLocked drops the oldest idle entries until the store is
// within maxEntries. Entries with in-flight reservations are never evicted.
func (s *sessionTurnCounter) evictIdleOverflowLocked() {
	for len(s.entries) > s.maxEntries && len(s.order) > 0 {
		evicted := false
		for i, key := range s.order {
			entry, ok := s.entries[key]
			if !ok {
				s.order = append(s.order[:i], s.order[i+1:]...)
				evicted = true
				break
			}
			if len(entry.inflight) > 0 {
				continue
			}
			delete(s.entries, key)
			s.order = append(s.order[:i], s.order[i+1:]...)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (s *sessionTurnCounter) touchLocked(key string) {
	out := s.order[:0]
	for _, existing := range s.order {
		if existing != key {
			out = append(out, existing)
		}
	}
	out = append(out, key)
	s.order = out
}

// sessionTurnKey uses proxy-owned authority for native turn state, then falls
// back to caller-local identity for unrelated verbosity accounting.
func sessionTurnKey(call aipapi.Call, fallback string) string {
	if id := strings.TrimSpace(call.Session.AuthoritativeSessionID); id != "" {
		return "session:" + id
	}
	if id := strings.TrimSpace(call.ID); id != "" && !isGeneratedCallID(id) {
		return "call:" + id
	}
	return fallback
}
