package reasoningpreservation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type SessionPartition struct {
	opaque string
}

func NewSessionPartition(opaque string) SessionPartition {
	return SessionPartition{opaque: opaque}
}

func (p SessionPartition) String() string {
	return ""
}

func (p SessionPartition) key() string {
	return p.opaque
}

type EvictionSummary struct {
	EvictedTurns int
	EvictedBytes int
	ExpiredTurns int
	ExpiredBytes int
}

type TurnStore interface {
	Append(context.Context, SessionPartition, TurnArtifact) (EvictionSummary, error)
	Snapshot(context.Context, SessionPartition) ([]TurnArtifact, error)
	Delete(context.Context, SessionPartition, ...string) error
}

type StoreOptions struct {
	TTL                      time.Duration
	MaxTurnsPerSession       int
	MaxReasoningBytesPerTurn int
	MaxSessionBytes          int
	Now                      func() time.Time
	CompressionLimits        CompressionLimits
}

type memoryTurnStore struct {
	opts StoreOptions
	mu   sync.Mutex
	by   map[string][]TurnArtifact
	// optional compression state (bounded, separate budgets)
	compBy                   map[string]map[string]*compressionEntry
	pendingPerSession        map[string]int
	surrogateBytesPerSession map[string]int
	totalPending             int
	totalSurrogateBytes      int
	reservationSeq           uint64
}

func NewMemoryTurnStore(opts StoreOptions) (TurnStore, error) {
	if opts.TTL <= 0 || opts.MaxTurnsPerSession <= 0 || opts.MaxReasoningBytesPerTurn <= 0 || opts.MaxSessionBytes <= 0 {
		return nil, fmt.Errorf("%s: invalid store options", ID)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CompressionLimits.MaxPendingPerSession < 0 || opts.CompressionLimits.MaxPendingTotal < 0 || opts.CompressionLimits.MaxSurrogateBytesPerTurn < 0 || opts.CompressionLimits.MaxSurrogateBytesPerSession < 0 || opts.CompressionLimits.MaxSurrogateBytesTotal < 0 {
		return nil, fmt.Errorf("%s: invalid compression limits", ID)
	}
	return &memoryTurnStore{
		opts:                     opts,
		by:                       make(map[string][]TurnArtifact),
		compBy:                   make(map[string]map[string]*compressionEntry),
		pendingPerSession:        make(map[string]int),
		surrogateBytesPerSession: make(map[string]int),
	}, nil
}

func (s *memoryTurnStore) Append(ctx context.Context, partition SessionPartition, artifact TurnArtifact) (EvictionSummary, error) {
	if err := ctx.Err(); err != nil {
		return EvictionSummary{}, err
	}
	var sum EvictionSummary
	s.mu.Lock()
	defer s.mu.Unlock()

	key := partition.key()
	now := s.opts.Now()
	sum = s.expireLocked(key, now, sum)

	copied := cloneArtifact(artifact)
	if copied.CreatedAt.IsZero() {
		copied.CreatedAt = now
	}
	if copied.ReasoningBytes > s.opts.MaxReasoningBytesPerTurn || copied.ReasoningBytes > s.opts.MaxSessionBytes {
		sum.EvictedTurns++
		sum.EvictedBytes += max(0, copied.ReasoningBytes)
		clearArtifact(&copied)
		return sum, nil
	}

	list := s.by[key]
	for len(list) >= s.opts.MaxTurnsPerSession {
		var evicted TurnArtifact
		evicted, list = list[0], list[1:]
		sum.EvictedTurns++
		sum.EvictedBytes += max(0, evicted.ReasoningBytes)
		s.clearOptionalLocked(key, evicted.ID)
		clearArtifact(&evicted)
	}
	sessionBytes := sessionBytesOf(list)
	for sessionBytes+copied.ReasoningBytes > s.opts.MaxSessionBytes && len(list) > 0 {
		var evicted TurnArtifact
		evicted, list = list[0], list[1:]
		sum.EvictedTurns++
		sum.EvictedBytes += max(0, evicted.ReasoningBytes)
		sessionBytes -= max(0, evicted.ReasoningBytes)
		s.clearOptionalLocked(key, evicted.ID)
		clearArtifact(&evicted)
	}
	if sessionBytes+copied.ReasoningBytes > s.opts.MaxSessionBytes {
		sum.EvictedTurns++
		sum.EvictedBytes += max(0, copied.ReasoningBytes)
		clearArtifact(&copied)
		s.by[key] = list
		if len(list) == 0 {
			delete(s.by, key)
		}
		return sum, nil
	}
	list = append(list, copied)
	s.by[key] = list
	return sum, nil
}

func (s *memoryTurnStore) Snapshot(ctx context.Context, partition SessionPartition) ([]TurnArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	_ = s.expireLocked(key, s.opts.Now(), EvictionSummary{})
	list := s.by[key]
	out := make([]TurnArtifact, len(list))
	for i := range list {
		out[i] = cloneArtifact(list[i])
	}
	return out, nil
}

func (s *memoryTurnStore) Delete(ctx context.Context, partition SessionPartition, ids ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := partition.key()
	list := s.by[key]
	if len(list) == 0 {
		return nil
	}
	kept := list[:0]
	for i := range list {
		if _, drop := want[list[i].ID]; drop {
			s.clearOptionalLocked(key, list[i].ID)
			clearArtifact(&list[i])
			continue
		}
		kept = append(kept, list[i])
	}
	if len(kept) == 0 {
		delete(s.by, key)
		return nil
	}
	s.by[key] = kept
	return nil
}

func (s *memoryTurnStore) expireLocked(key string, now time.Time, sum EvictionSummary) EvictionSummary {
	list := s.by[key]
	if len(list) == 0 {
		return sum
	}
	kept := list[:0]
	for i := range list {
		if now.Sub(list[i].CreatedAt) >= s.opts.TTL {
			sum.ExpiredTurns++
			sum.ExpiredBytes += max(0, list[i].ReasoningBytes)
			s.clearOptionalLocked(key, list[i].ID)
			clearArtifact(&list[i])
			continue
		}
		kept = append(kept, list[i])
	}
	if len(kept) == 0 {
		delete(s.by, key)
		return sum
	}
	s.by[key] = kept
	return sum
}

func (s *memoryTurnStore) clearOptionalLocked(sessionKey, artifactID string) {
	m, ok := s.compBy[sessionKey]
	if !ok {
		return
	}
	entry, ok := m[artifactID]
	if !ok {
		return
	}
	if entry.pending != nil {
		if c := s.pendingPerSession[sessionKey]; c > 0 {
			s.pendingPerSession[sessionKey] = c - 1
			if s.pendingPerSession[sessionKey] == 0 {
				delete(s.pendingPerSession, sessionKey)
			}
		}
		if s.totalPending > 0 {
			s.totalPending--
		}
	}
	if entry.surrogate != nil {
		b := entry.surrogate.Bytes
		if c := s.surrogateBytesPerSession[sessionKey]; c >= b {
			nc := c - b
			if nc == 0 {
				delete(s.surrogateBytesPerSession, sessionKey)
			} else {
				s.surrogateBytesPerSession[sessionKey] = nc
			}
		} else {
			delete(s.surrogateBytesPerSession, sessionKey)
		}
		if s.totalSurrogateBytes >= b {
			s.totalSurrogateBytes -= b
		} else {
			s.totalSurrogateBytes = 0
		}
	}
	delete(m, artifactID)
	if len(m) == 0 {
		delete(s.compBy, sessionKey)
	}
}

func sessionBytesOf(list []TurnArtifact) int {
	n := 0
	for i := range list {
		n += max(0, list[i].ReasoningBytes)
	}
	return n
}

func clearArtifact(a *TurnArtifact) {
	if a == nil {
		return
	}
	a.Anchor = [32]byte{}
	a.SourceBackend = ""
	a.SourceModel = ""
	for i := range a.Reasoning {
		if a.Reasoning[i].Part.Reasoning != nil {
			a.Reasoning[i].Part.Reasoning.Text = ""
			a.Reasoning[i].Part.Reasoning.Signature = ""
			a.Reasoning[i].Part.Reasoning.Opaque = nil
			a.Reasoning[i].Part.Reasoning = nil
		}
		a.Reasoning[i].Part = aipapi.Part{}
	}
	a.Reasoning = nil
	a.ReasoningBytes = 0
}

func cloneArtifact(in TurnArtifact) TurnArtifact {
	out := in
	if len(in.Reasoning) > 0 {
		out.Reasoning = make([]PlacedReasoning, len(in.Reasoning))
		for i := range in.Reasoning {
			out.Reasoning[i] = PlacedReasoning{
				BeforeNonReasoningPart: in.Reasoning[i].BeforeNonReasoningPart,
				Part:                   clonePart(in.Reasoning[i].Part),
			}
		}
	}
	return out
}

func cloneArtifacts(in []TurnArtifact) []TurnArtifact {
	if len(in) == 0 {
		return nil
	}
	out := make([]TurnArtifact, len(in))
	for i := range in {
		out[i] = cloneArtifact(in[i])
	}
	return out
}
