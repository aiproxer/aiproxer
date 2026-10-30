package memory

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

var (
	_ app.Store              = (*Store)(nil)
	_ app.SessionUsageRollup = (*Store)(nil)
)

// Store is a race-safe in-memory implementation of [app.Store].
type Store struct {
	mu sync.RWMutex

	byID            map[domain.SessionID]*sessionRow
	byFingerprint   map[string]domain.SessionID
	byALeg          map[string]domain.SessionID
	readinessError  error
	simulateDurable bool
}

type attemptPair struct {
	trace      domain.AttemptTrace
	outcome    domain.AttemptOutcome
	hasOutcome bool
	accounting domain.AttemptAccounting
}

type sessionRow struct {
	rec        domain.Record
	transcript []domain.TranscriptItem
	audit      []domain.AuditItem

	usageIn  int64
	usageOut int64
	turnIDs  map[domain.TurnID]struct{}
	attemptN int
	attempts []attemptPair
}

// Options configures the in-memory store (testing and non-durable runtime).
type Options struct {
	// ReadinessError, when non-nil, makes CheckReadiness return it (after policy checks).
	ReadinessError error
	// SimulateDurable treats the store as durable-ready for mandatory audit/durable policy gates.
	SimulateDurable bool
}

// New returns an empty in-memory secure-session store.
func New(opts Options) *Store {
	return &Store{
		byID:            make(map[domain.SessionID]*sessionRow),
		byFingerprint:   make(map[string]domain.SessionID),
		byALeg:          make(map[string]domain.SessionID),
		readinessError:  opts.ReadinessError,
		simulateDurable: opts.SimulateDurable,
	}
}

func fpKey(fp domain.TokenFingerprint) string {
	return string(fp[:])
}

// Create implements [github.com/aiproxer/aiproxer/internal/core/securesession/app.Store].
func (s *Store) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.byID[rec.SessionID]; dup {
		return domain.Record{}, domain.ErrDuplicateSessionID
	}
	key := fpKey(rec.ResumeFingerprint)
	if _, dup := s.byFingerprint[key]; dup {
		return domain.Record{}, domain.ErrDuplicateFingerprint
	}
	if rec.ALegID != "" {
		if _, dup := s.byALeg[rec.ALegID]; dup {
			return domain.Record{}, domain.ErrDuplicateSessionID
		}
	}
	row := &sessionRow{
		rec: domain.Record{
			SessionID:          rec.SessionID,
			ResumeFingerprint:  rec.ResumeFingerprint,
			Owner:              rec.Owner,
			Workspace:          rec.Workspace,
			ClientHints:        rec.ClientHints,
			Policy:             rec.Policy,
			ALegID:             rec.ALegID,
			Status:             domain.SessionStatusActive,
			ResumeEligible:     rec.ResumeEligible,
			LastActivityAt:     rec.CreatedAt,
			LastActivitySource: domain.ActivitySystem,
			CreatedAt:          rec.CreatedAt,
		},
		transcript: []domain.TranscriptItem{},
		audit:      []domain.AuditItem{},
		turnIDs:    make(map[domain.TurnID]struct{}),
	}
	s.byID[rec.SessionID] = row
	s.byFingerprint[key] = rec.SessionID
	if rec.ALegID != "" {
		s.byALeg[rec.ALegID] = rec.SessionID
	}
	return row.rec, nil
}

func (s *Store) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return domain.Record{}, domain.ErrSessionNotFound
	}
	return row.rec, nil
}

func (s *Store) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byFingerprint[fpKey(fp)]
	if !ok {
		return domain.Record{}, domain.ErrSessionNotFound
	}
	return s.byID[id].rec, nil
}

func (s *Store) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byALeg[aLegID]
	if !ok {
		return domain.Record{}, domain.ErrSessionNotFound
	}
	return s.byID[id].rec, nil
}

func (s *Store) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[id]
	if !ok {
		return domain.ErrSessionNotFound
	}
	// Monotonic merge: ignore strictly older touches (concurrent out-of-order activity).
	if at.Before(row.rec.LastActivityAt) {
		return nil
	}
	row.rec.LastActivityAt = at
	row.rec.LastActivitySource = source
	return nil
}

func (s *Store) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[trace.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	row.attemptN++
	row.rec.LatestAttemptTrace = trace
	row.attempts = append(row.attempts, attemptPair{trace: trace})
	return nil
}

func (s *Store) UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[outcome.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	found := false
	for i := range slices.Backward(row.attempts) {
		ap := &row.attempts[i]
		if ap.trace.TurnID != outcome.TurnID || ap.trace.BLegID != outcome.BLegID {
			continue
		}
		if ap.hasOutcome {
			continue
		}
		ap.outcome = outcome
		ap.hasOutcome = true
		found = true
		break
	}
	if !found {
		return domain.ErrSessionNotFound
	}
	row.rec.LatestAttemptOutcome = outcome
	return nil
}

func (s *Store) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[item.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	if !row.rec.Policy.TranscriptEnabled {
		return domain.ErrTranscriptDisabled
	}
	row.transcript = append(row.transcript, item)
	row.turnIDs[item.TurnID] = struct{}{}
	return nil
}

func (s *Store) NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[id]
	if !ok {
		return 0, domain.ErrSessionNotFound
	}
	var maxSeq int64
	for _, t := range row.transcript {
		if t.Seq > maxSeq {
			maxSeq = t.Seq
		}
	}
	return maxSeq + 1, nil
}

func (s *Store) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[delta.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	row.usageIn += delta.InputTokens
	row.usageOut += delta.OutputTokens
	if delta.BLegID != "" {
		deltaAccounting := domain.AttemptAccounting{
			BLegID:                   delta.BLegID,
			InputTokens:              delta.InputTokens,
			OutputTokens:             delta.OutputTokens,
			CacheReadTokens:          delta.CacheReadTokens,
			CacheWriteTokens:         delta.CacheWriteTokens,
			NonCachedInputTokens:     delta.NonCachedInputTokens,
			ReasoningTokens:          delta.ReasoningTokens,
			NonReasoningOutputTokens: delta.NonReasoningOutputTokens,
			TotalTokens:              delta.TotalTokens,
			CostNanoUnits:            delta.CostNanoUnits,
			CostMinorUnits:           delta.CostMinorUnits,
			Currency:                 delta.Currency,
			CostSource:               delta.CostSource,
			RawUsageJSON:             delta.RawUsageJSON,
			BillingUnavailable:       delta.BillingUnavailable,
			RequestStartedAt:         delta.RequestStartedAt,
			FirstRemoteEventAt:       delta.FirstRemoteEventAt,
			FirstMeaningfulTokenAt:   delta.FirstMeaningfulTokenAt,
			RemoteCompletedAt:        delta.RemoteCompletedAt,
			ProxyCompletedAt:         delta.ProxyCompletedAt,
			TTFTMillis:               delta.TTFTMillis,
			RemoteDurationMillis:     delta.RemoteDurationMillis,
			CompletionDurationMillis: delta.CompletionDurationMillis,
			CompletionTPSMilli:       delta.CompletionTPSMilli,
		}
		for i := range slices.Backward(row.attempts) {
			ap := &row.attempts[i]
			if ap.trace.TurnID == delta.TurnID && ap.trace.BLegID == delta.BLegID {
				ap.accounting = domain.MergeAttemptAccounting(ap.accounting, deltaAccounting)
				break
			}
		}
		row.rec.LatestAttemptAccounting = domain.MergeAttemptAccounting(row.rec.LatestAttemptAccounting, deltaAccounting)
	}
	return nil
}

func (s *Store) NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[id]
	if !ok {
		return 0, domain.ErrSessionNotFound
	}
	return nextAuditSeqLocked(row), nil
}

// nextAuditSeqLocked returns the next audit sequence number for row.
// Caller must hold s.mu.
func nextAuditSeqLocked(row *sessionRow) int64 {
	var n int64
	for _, a := range row.audit {
		if a.Seq > n {
			n = a.Seq
		}
	}
	return n + 1
}

func (s *Store) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[item.SessionID]
	if !ok {
		return domain.ErrSessionNotFound
	}
	row.audit = append(row.audit, item)
	return nil
}

func (s *Store) Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	out := filterAuditAfterSeq(row.audit, opts.AfterSeq, opts.Limit)
	if out == nil {
		return []domain.AuditItem{}, nil
	}
	return out, nil
}

func (s *Store) Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	if !row.rec.Policy.TranscriptEnabled {
		return []domain.TranscriptItem{}, nil
	}
	out := filterTranscriptAfterSeq(row.transcript, opts.AfterSeq, opts.Limit)
	if out == nil {
		return []domain.TranscriptItem{}, nil
	}
	return out, nil
}

func (s *Store) ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	src := row.attempts
	if len(src) > limit {
		src = src[len(src)-limit:]
	}
	out := make([]domain.AttemptEvidence, 0, len(src))
	for _, ap := range src {
		ev := domain.AttemptEvidence{Trace: ap.trace, Accounting: ap.accounting}
		if ap.hasOutcome {
			ev.Outcome = ap.outcome
		}
		out = append(out, ev)
	}
	return out, nil
}

func (s *Store) Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]domain.SessionID, 0, len(s.byID))
	for id, row := range s.byID {
		if query.OwnerID != "" && row.rec.Owner.ID != query.OwnerID {
			continue
		}
		if query.WorkspaceID != "" && row.rec.Workspace.ID != query.WorkspaceID {
			continue
		}
		matches = append(matches, id)
	}
	slices.SortFunc(matches, func(a, b domain.SessionID) int {
		ra, rb := s.byID[a].rec, s.byID[b].rec
		if !ra.LastActivityAt.Equal(rb.LastActivityAt) {
			if ra.LastActivityAt.Before(rb.LastActivityAt) {
				return 1
			}
			return -1
		}
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]domain.Summary, 0, len(matches))
	for _, id := range matches {
		row := s.byID[id]
		out = append(out, domain.Summary{
			SessionID:      id,
			OwnerID:        row.rec.Owner.ID,
			WorkspaceID:    row.rec.Workspace.ID,
			LastActivityAt: row.rec.LastActivityAt,
			TurnCount:      len(row.turnIDs),
			AttemptCount:   row.attemptN,

			Status:            row.rec.Status,
			ResumeEligible:    row.rec.ResumeEligible,
			ALegID:            row.rec.ALegID,
			PolicyVersion:     row.rec.Policy.PolicyVersion,
			TranscriptEnabled: row.rec.Policy.TranscriptEnabled,
			RedactionProfile:  row.rec.Policy.RedactionProfile,
			AuditMode:         row.rec.Policy.AuditMode,
			UsageInputTokens:  row.usageIn,
			UsageOutputTokens: row.usageOut,
		})
	}
	return out, nil
}

// UsageTokenTotals implements [github.com/aiproxer/aiproxer/internal/core/securesession/app.SessionUsageRollup].
func (s *Store) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, ok := s.byID[id]
	if !ok {
		return 0, 0, domain.ErrSessionNotFound
	}
	return row.usageIn, row.usageOut, nil
}

func (s *Store) CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error {
	_ = ctx
	s.mu.RLock()
	re := s.readinessError
	sd := s.simulateDurable
	s.mu.RUnlock()
	if re != nil {
		return re
	}
	if policy.AuditMode == "mandatory" && !sd {
		return domain.ErrMandatoryAuditFailure
	}
	return nil
}

func filterTranscriptAfterSeq(items []domain.TranscriptItem, after int64, limit int) []domain.TranscriptItem {
	buf := make([]domain.TranscriptItem, 0, len(items))
	for _, it := range items {
		if it.Seq <= after {
			continue
		}
		buf = append(buf, it)
	}
	if limit > 0 && len(buf) > limit {
		buf = buf[:limit]
	}
	return buf
}

func filterAuditAfterSeq(items []domain.AuditItem, after int64, limit int) []domain.AuditItem {
	buf := make([]domain.AuditItem, 0, len(items))
	for _, it := range items {
		if it.Seq <= after {
			continue
		}
		buf = append(buf, it)
	}
	if limit > 0 && len(buf) > limit {
		buf = buf[:limit]
	}
	return buf
}
