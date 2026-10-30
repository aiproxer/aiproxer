// Package ledgerstore provides control-plane event-store adapters: an
// in-memory store for deterministic local recording and tests, and a
// Bun-backed durable store for SQLite and Postgres deployments.
//
// Both adapters implement the core-owned [controlplane.Store] port. SQL, Bun,
// HTTP, transport, and provider SDK types never cross into pkg/aipsdk/controlplane
// or internal/core/controlplane; adapters translate into and out of the SDK
// DTOs at their own edges (design "Allowed Dependencies"; requirements 9.5, 10.5).
package ledgerstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/fields"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// MemoryConfig configures an in-memory control-plane event store.
type MemoryConfig struct {
	// StoreID is the stable store identifier embedded in assigned EventIDs.
	StoreID string
	// DefaultPageSize is applied when a query omits Limit. Defaults to 100.
	DefaultPageSize int
	// MaxPageSize is the upper bound for any query page. Defaults to 500.
	MaxPageSize int
	// UnsupportedFilters is the set of canonical filter field names
	// (see internal/infra/controlplane/ledgerstore/contract) this store cannot
	// apply. Filter requests for these fields are reported explicitly via
	// Page.Unsupported rather than silently widening the query (requirement
	// 2.5, 8.6, 9.4). Empty means all documented filters are supported.
	UnsupportedFilters []string
}

// MemoryStore is the in-memory control-plane event store. It is safe for
// concurrent use. It assigns monotonic sequence numbers per store, deduplicates
// by SourceEventKey, and serves bounded query views from the same recorded
// facts (tasks 2.1, 2.4, 2.5).
type MemoryStore struct {
	cfg MemoryConfig

	mu                sync.RWMutex
	defaultPageSize   int
	maxPageSize       int
	unsupportedFields map[string]struct{}

	seq      int64
	bySource map[string]int64
	events   []storedEvent
}

type storedEvent struct {
	event   cp.Event
	seq     int64
	expired bool
}

// NewMemoryStore returns a fresh in-memory store. It does not perform I/O and
// is always ready.
func NewMemoryStore(cfg MemoryConfig) (*MemoryStore, error) {
	if cfg.StoreID == "" {
		return nil, fmt.Errorf("ledgerstore: memory store id is required")
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if maxPageSize < def {
		return nil, fmt.Errorf("ledgerstore: max page size %d < default %d", maxPageSize, def)
	}
	unsup := make(map[string]struct{}, len(cfg.UnsupportedFilters))
	for _, f := range cfg.UnsupportedFilters {
		unsup[f] = struct{}{}
	}
	return &MemoryStore{
		cfg:               cfg,
		defaultPageSize:   def,
		maxPageSize:       maxPageSize,
		unsupportedFields: unsup,
		bySource:          make(map[string]int64),
	}, nil
}

// Close releases no resources for the memory store; it is provided so callers
// can uniform-close memory and durable stores.
func (s *MemoryStore) Close() error { return nil }

// Append records one validated event with a stable monotonic identity and
// source-event-key dedupe (requirement 1.7, 1.8).
func (s *MemoryStore) Append(ctx context.Context, ev cp.Event) (cp.RecordResult, error) {
	if err := ctx.Err(); err != nil {
		return cp.RecordResult{}, err
	}
	if err := controlplane.ValidateEvent(ev); err != nil {
		return cp.RecordResult{}, fmt.Errorf("%w: %v", controlplane.ErrUnsafeEvidence, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if ev.SourceEventKey != "" {
		if existingSeq, ok := s.bySource[ev.SourceEventKey]; ok {
			existing := s.lookupBySeqLocked(existingSeq)
			return cp.RecordResult{
				ID:         cp.EventID{StoreID: s.cfg.StoreID, Sequence: existingSeq},
				Dedupe:     cp.DedupeDuplicate,
				RecordedAt: existing.recordedAt(),
			}, nil
		}
	}

	s.seq++
	seq := s.seq
	ev.ID = cp.EventID{StoreID: s.cfg.StoreID, Sequence: seq}
	if ev.RecordedAt.IsZero() {
		ev.RecordedAt = ev.OccurredAt
	}
	if ev.RecordedAt.Before(ev.OccurredAt) {
		ev.RecordedAt = ev.OccurredAt
	}
	s.events = append(s.events, storedEvent{event: ev, seq: seq})
	if ev.SourceEventKey != "" {
		s.bySource[ev.SourceEventKey] = seq
	}
	return cp.RecordResult{
		ID:         ev.ID,
		Dedupe:     cp.DedupeInserted,
		RecordedAt: ev.RecordedAt,
	}, nil
}

func (s *MemoryStore) lookupBySeqLocked(seq int64) storedEvent {
	// Sequence numbers are contiguous and 1-based (s.seq only increments and
	// s.events is append-only; retention marks in place), so the event with seq
	// lives at index seq-1. Fall back to zero for an out-of-range lookup.
	if seq <= 0 || seq > int64(len(s.events)) {
		return storedEvent{}
	}
	return s.events[seq-1]
}

// ---- queries ----

func resolveVisibility(v cp.Visibility) cp.Visibility {
	if v == "" {
		return cp.VisibilityDefault
	}
	return v
}

func (s *MemoryStore) Events(ctx context.Context, q cp.EventQuery) (cp.Page[cp.Event], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHash(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.Event]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedEventFilters(s.unsupportedFields, q)

	s.mu.RLock()
	matched := make([]sequenced[cp.Event], 0)
	for _, se := range s.events {
		ev := se.event
		if q.Category != "" && !isUnsupportedField(s.unsupportedFields, fields.EventCategory) && ev.Category != q.Category {
			continue
		}
		if !commonFiltersMatch(q.Common, ev, s.unsupportedFields) {
			continue
		}
		matched = append(matched, sequenced[cp.Event]{row: applyQueryVisibility(ev, visibility), seq: se.seq})
	}
	s.mu.RUnlock()
	sortSeq(matched)

	resumed := resumeSeq(matched, cur.LastSeq)
	return paginate(resumed, limit, shapeHash(q), visibility, unsupported), nil
}

func (s *MemoryStore) Sessions(ctx context.Context, q cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashSession(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.SessionSummary]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedSessionFilters(s.unsupportedFields, q)

	s.mu.RLock()
	groups := s.groupSessionsLocked(q.Common, s.unsupportedFields)
	s.mu.RUnlock()

	rows := make([]sequenced[cp.SessionSummary], 0, len(groups))
	for _, g := range groups {
		rows = append(rows, g.toSummary())
	}
	sortSeq(rows)

	resumed := resumeSeq(rows, cur.LastSeq)
	return paginate(resumed, limit, shapeHashSession(q), visibility, unsupported), nil
}

func (s *MemoryStore) Attempts(ctx context.Context, q cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashAttempt(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.AttemptRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedAttemptFilters(s.unsupportedFields, q)

	s.mu.RLock()
	rows := make([]sequenced[cp.AttemptRow], 0)
	for _, se := range s.events {
		ev := se.event
		if ev.Attempt() == nil {
			continue
		}
		if !commonFiltersMatch(q.Common, ev, s.unsupportedFields) {
			continue
		}
		if q.Surfaced != "" && !isUnsupportedField(s.unsupportedFields, fields.AttemptSurfaced) && string(ev.Attempt().Surfaced) != q.Surfaced {
			continue
		}
		rows = append(rows, sequenced[cp.AttemptRow]{row: attemptRowFromEvent(ev), seq: se.seq})
	}
	s.mu.RUnlock()
	sortSeq(rows)

	resumed := resumeSeq(rows, cur.LastSeq)
	return paginate(resumed, limit, shapeHashAttempt(q), visibility, unsupported), nil
}

func (s *MemoryStore) Usage(ctx context.Context, q cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashUsage(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedUsageFilters(s.unsupportedFields, q)
	if strings.TrimSpace(q.RuleID) != "" {
		// Refuse to return widened results when rule_id cannot be applied (14.8).
		return paginate([]sequenced[cp.UsageRow]{}, limit, shapeHashUsage(q), visibility, unsupported), nil
	}

	s.mu.RLock()
	rows := make([]sequenced[cp.UsageRow], 0)
	for _, se := range s.events {
		ev := se.event
		if ev.Usage() == nil {
			continue
		}
		if !commonFiltersMatch(q.Common, ev, s.unsupportedFields) {
			continue
		}
		if !usageDetailMatchesQuery(ev.Usage(), q, s.unsupportedFields) {
			continue
		}
		rows = append(rows, sequenced[cp.UsageRow]{row: usageRowFromEvent(ev), seq: se.seq})
	}
	s.mu.RUnlock()
	sortSeq(rows)

	resumed := resumeSeq(rows, cur.LastSeq)
	return paginate(resumed, limit, shapeHashUsage(q), visibility, unsupported), nil
}

func (s *MemoryStore) UsageAggregate(ctx context.Context, q cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashUsageAggregate(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.UsageAggregate]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedUsageAggregateFilters(s.unsupportedFields, q)

	s.mu.RLock()
	aggMap := map[string]*cp.UsageAggregate{}
	var order []string
	seqFor := map[string]int64{}
	for _, se := range s.events {
		ev := se.event
		if ev.Usage() == nil {
			continue
		}
		if !commonFiltersMatch(q.Common, ev, s.unsupportedFields) {
			continue
		}
		key, a := aggregateRow(q.GroupBy, ev)
		if existing, ok := aggMap[key]; ok {
			mergeAggregate(existing, ev.Usage())
			if se.seq > seqFor[key] {
				seqFor[key] = se.seq
			}
			continue
		}
		aggMap[key] = a
		order = append(order, key)
		seqFor[key] = se.seq
	}
	s.mu.RUnlock()

	rows := make([]sequenced[cp.UsageAggregate], 0, len(order))
	for _, k := range order {
		rows = append(rows, sequenced[cp.UsageAggregate]{row: *aggMap[k], seq: seqFor[k]})
	}
	sortSeq(rows)

	resumed := resumeSeq(rows, cur.LastSeq)
	return paginate(resumed, limit, shapeHashUsageAggregate(q), visibility, unsupported), nil
}

func (s *MemoryStore) PolicyAudit(ctx context.Context, q cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	prep, err := prepareQuery(ctx, s.defaultPageSize, s.maxPageSize, q.Limit, q.Cursor, shapeHashEvidence(q), q.Visibility)
	if err != nil {
		return cp.Page[cp.PolicyAuditRow]{}, err
	}
	limit, cur, visibility := prep.limit, prep.cursor, prep.visibility
	unsupported := unsupportedEvidenceFilters(s.unsupportedFields, q)

	s.mu.RLock()
	rows := make([]sequenced[cp.PolicyAuditRow], 0)
	for _, se := range s.events {
		ev := se.event
		if ev.Policy() == nil && ev.Audit() == nil {
			continue
		}
		if q.Category != "" && !isUnsupportedField(s.unsupportedFields, fields.EvidenceCategory) && ev.Category != q.Category {
			continue
		}
		if q.Effect != "" && !isUnsupportedField(s.unsupportedFields, fields.EvidenceEffect) {
			if ev.Policy() == nil || ev.Policy().Effect != q.Effect {
				continue
			}
		}
		if !commonFiltersMatch(q.Common, ev, s.unsupportedFields) {
			continue
		}
		rows = append(rows, sequenced[cp.PolicyAuditRow]{row: policyAuditRowFromEvent(ev), seq: se.seq})
	}
	s.mu.RUnlock()
	sortSeq(rows)

	resumed := resumeSeq(rows, cur.LastSeq)
	return paginate(resumed, limit, shapeHashEvidence(q), visibility, unsupported), nil
}

// ---- retention / redaction ----

// ApplyRetention marks records at or before the cutoff as expired (standard)
// or redacted (strict). Records remain queryable so consumers can see explicit
// expired/redacted state; details are cleared for redacted/expired rows.
// Repeated runs at the same cutoff mark no additional records (requirement 6.1,
// 6.2, 6.6; design "Idempotency").
func (s *MemoryStore) ApplyRetention(ctx context.Context, cmd controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.RetentionResult{}, err
	}
	if !cmd.Profile.IsKnown() {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: unknown retention profile %q", controlplane.ErrInvalidQuery, cmd.Profile)
	}
	if cmd.Cutoff.IsZero() {
		return controlplane.RetentionResult{}, fmt.Errorf("%w: retention cutoff is required", controlplane.ErrInvalidQuery)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	marked := 0
	for i := range s.events {
		se := &s.events[i]
		if se.expired {
			continue
		}
		if se.event.OccurredAt.After(cmd.Cutoff) {
			continue
		}
		se.expired = true
		ev := &se.event
		if cmd.Profile == controlplane.RetentionProfileStrict {
			ev.EvidenceState = cp.EvidenceRedacted
			ev.RedactionState = cp.RedactionRedacted
		} else {
			ev.EvidenceState = cp.EvidenceExpired
		}
		clearDetail(ev)
		ev.Summary = ""
		marked++
	}
	return controlplane.RetentionResult{
		Marked: marked,
		Pruned: 0,
		Status: cp.CapabilityStatus{
			State:           cp.CapabilityReady,
			RecordingPolicy: cp.RecordingBestEffort,
		},
	}, nil
}

// CheckReadiness reports backing capability availability. The memory store is
// always ready (requirement 7.1).
func (s *MemoryStore) CheckReadiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Compile-time assertion that MemoryStore satisfies the core-owned Store port.
var _ controlplane.Store = (*MemoryStore)(nil)

// ---- helpers ----

func (e storedEvent) recordedAt() time.Time {
	if !e.event.RecordedAt.IsZero() {
		return e.event.RecordedAt
	}
	return e.event.OccurredAt
}

func clearDetail(ev *cp.Event) {
	ev.Detail = nil
}

// applyQueryVisibility downgrades privileged evidence for default-visibility
// queries so privileged raw evidence is not surfaced (requirement 4.6, 6.5).
func applyQueryVisibility(ev cp.Event, visibility cp.Visibility) cp.Event {
	out := ev
	if visibility == cp.VisibilityPrivileged {
		return out
	}
	if ev.Visibility == cp.VisibilityPrivileged || ev.RedactionState == cp.RedactionPrivileged {
		out.Visibility = cp.VisibilityDefault
		if out.RedactionState == cp.RedactionPrivileged {
			out.RedactionState = cp.RedactionRedacted
		}
		out.EvidenceState = cp.EvidenceRedacted
		clearDetail(&out)
	}
	return out
}
