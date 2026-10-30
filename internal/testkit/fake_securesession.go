package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FakeSecureSessionStore implements [app.Store] with injectable per-method errors.
// When a field is nil and Delegate is non-nil, the call forwards to Delegate.
// When both are unset, reads return [domain.ErrSessionNotFound] and mutating calls return nil or not-found as appropriate.
type FakeSecureSessionStore struct {
	Delegate app.Store

	mu sync.Mutex

	CreateErr                  error
	LoadByIDErr                error
	LoadByResumeFingerprintErr error
	LoadByALegIDErr            error
	TouchActivityErr           error
	AppendAttemptTraceErr      error
	UpdateAttemptOutcomeErr    error
	AppendTranscriptErr        error
	NextTranscriptSeqErr       error
	AddUsageErr                error
	AppendAuditErr             error
	NextAuditSeqErr            error
	AuditErr                   error
	SummaryErr                 error
	TranscriptErr              error
	ListAttemptEvidenceErr     error
	CheckReadinessErr          error
	QuarantineErr              error

	CreateCalls     int
	QuarantineCalls int
}

var (
	_ app.Store              = (*FakeSecureSessionStore)(nil)
	_ app.SessionUsageRollup = (*FakeSecureSessionStore)(nil)
)

func (f *FakeSecureSessionStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	f.mu.Lock()
	f.CreateCalls++
	err := f.CreateErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return domain.Record{}, err
	}
	if del != nil {
		return del.Create(ctx, rec)
	}
	return domain.Record{}, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	f.mu.Lock()
	err := f.LoadByIDErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return domain.Record{}, err
	}
	if del != nil {
		return del.LoadByID(ctx, id)
	}
	return domain.Record{}, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	f.mu.Lock()
	err := f.LoadByResumeFingerprintErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return domain.Record{}, err
	}
	if del != nil {
		return del.LoadByResumeFingerprint(ctx, fp)
	}
	return domain.Record{}, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	f.mu.Lock()
	err := f.LoadByALegIDErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return domain.Record{}, err
	}
	if del != nil {
		return del.LoadByALegID(ctx, aLegID)
	}
	return domain.Record{}, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	f.mu.Lock()
	err := f.TouchActivityErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.TouchActivity(ctx, id, at, source)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	f.mu.Lock()
	err := f.AppendAttemptTraceErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.AppendAttemptTrace(ctx, trace)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	f.mu.Lock()
	err := f.UpdateAttemptOutcomeErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.UpdateAttemptOutcome(ctx, outcome)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	f.mu.Lock()
	err := f.AppendTranscriptErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.AppendTranscript(ctx, item)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	f.mu.Lock()
	err := f.NextTranscriptSeqErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if del != nil {
		return del.NextTranscriptSeq(ctx, id)
	}
	return 0, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	f.mu.Lock()
	err := f.AddUsageErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.AddUsage(ctx, delta)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	f.mu.Lock()
	err := f.NextAuditSeqErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if del != nil {
		return del.NextAuditSeq(ctx, id)
	}
	return 0, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	f.mu.Lock()
	err := f.AppendAuditErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.AppendAudit(ctx, item)
	}
	return domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error) {
	f.mu.Lock()
	err := f.AuditErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if del != nil {
		return del.Audit(ctx, id, opts)
	}
	return nil, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error) {
	f.mu.Lock()
	err := f.SummaryErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if del != nil {
		return del.Summary(ctx, query)
	}
	return []domain.Summary{}, nil
}

func (f *FakeSecureSessionStore) Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error) {
	f.mu.Lock()
	err := f.TranscriptErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if del != nil {
		return del.Transcript(ctx, id, opts)
	}
	return nil, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	f.mu.Lock()
	err := f.ListAttemptEvidenceErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if del != nil {
		return del.ListAttemptEvidence(ctx, id, opts)
	}
	return nil, domain.ErrSessionNotFound
}

func (f *FakeSecureSessionStore) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	f.mu.Lock()
	del := f.Delegate
	f.mu.Unlock()
	if del == nil {
		return 0, 0, domain.ErrSessionNotFound
	}
	if u, ok := del.(app.SessionUsageRollup); ok {
		return u.UsageTokenTotals(ctx, id)
	}
	return 0, 0, nil
}

func (f *FakeSecureSessionStore) CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error {
	f.mu.Lock()
	err := f.CheckReadinessErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.CheckReadiness(ctx, policy)
	}
	return nil
}

func (f *FakeSecureSessionStore) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	f.mu.Lock()
	f.QuarantineCalls++
	err := f.QuarantineErr
	del := f.Delegate
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if del != nil {
		return del.Quarantine(ctx, in)
	}
	return domain.ErrQuarantineUnimplemented
}

// FakeB2BUAStore implements [b2bua.Store] with injectable errors and optional backing implementation.
type FakeB2BUAStore struct {
	Impl b2bua.Store

	ResolveALegErr              error
	CreateALegErr               error
	FetchALegErr                error
	SetWeightedFirstConsumedErr error
	NextBLegErr                 error
	RecordAttemptErr            error
	LoadAttemptsErr             error

	ResolveALegHook              func(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error)
	CreateALegHook               func(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error)
	FetchALegHook                func(ctx context.Context, aLegID string) (b2bua.ALegRecord, error)
	SetWeightedFirstConsumedHook func(ctx context.Context, aLegID string, consumed bool) error
	NextBLegHook                 func(ctx context.Context, aLegID string) (b2bua.BLegRecord, error)
	RecordAttemptHook            func(ctx context.Context, rec aipapi.AttemptRecord) error
	LoadAttemptsHook             func(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error)
}

var _ b2bua.Store = (*FakeB2BUAStore)(nil)

func (f *FakeB2BUAStore) ResolveALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	if f.ResolveALegHook != nil {
		return f.ResolveALegHook(ctx, continuityKey)
	}
	if err := f.ResolveALegErr; err != nil {
		return b2bua.ALegRecord{}, err
	}
	if f.Impl != nil {
		return f.Impl.ResolveALeg(ctx, continuityKey)
	}
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (f *FakeB2BUAStore) CreateALeg(ctx context.Context, continuityKey string) (b2bua.ALegRecord, error) {
	if f.CreateALegHook != nil {
		return f.CreateALegHook(ctx, continuityKey)
	}
	if err := f.CreateALegErr; err != nil {
		return b2bua.ALegRecord{}, err
	}
	if f.Impl != nil {
		return f.Impl.CreateALeg(ctx, continuityKey)
	}
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (f *FakeB2BUAStore) FetchALeg(ctx context.Context, aLegID string) (b2bua.ALegRecord, error) {
	if f.FetchALegHook != nil {
		return f.FetchALegHook(ctx, aLegID)
	}
	if err := f.FetchALegErr; err != nil {
		return b2bua.ALegRecord{}, err
	}
	if f.Impl != nil {
		return f.Impl.FetchALeg(ctx, aLegID)
	}
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (f *FakeB2BUAStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	if f.SetWeightedFirstConsumedHook != nil {
		return f.SetWeightedFirstConsumedHook(ctx, aLegID, consumed)
	}
	if err := f.SetWeightedFirstConsumedErr; err != nil {
		return err
	}
	if f.Impl != nil {
		return f.Impl.SetWeightedFirstConsumed(ctx, aLegID, consumed)
	}
	return nil
}

func (f *FakeB2BUAStore) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	if f.NextBLegHook != nil {
		return f.NextBLegHook(ctx, aLegID)
	}
	if err := f.NextBLegErr; err != nil {
		return b2bua.BLegRecord{}, err
	}
	if f.Impl != nil {
		return f.Impl.NextBLeg(ctx, aLegID)
	}
	return b2bua.BLegRecord{}, b2bua.ErrALegNotFound
}

func (f *FakeB2BUAStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	if f.RecordAttemptHook != nil {
		return f.RecordAttemptHook(ctx, rec)
	}
	if err := f.RecordAttemptErr; err != nil {
		return err
	}
	if f.Impl != nil {
		return f.Impl.RecordAttempt(ctx, rec)
	}
	return nil
}

func (f *FakeB2BUAStore) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	if f.LoadAttemptsHook != nil {
		return f.LoadAttemptsHook(ctx, aLegID)
	}
	if err := f.LoadAttemptsErr; err != nil {
		return nil, err
	}
	if f.Impl != nil {
		return f.Impl.LoadAttempts(ctx, aLegID)
	}
	return []aipapi.AttemptRecord{}, nil
}

// FakeSecureSessionRecorder is a placeholder for future secure-session recorder wiring (tasks 5.x/6.x).
// Use PostOutputFailure to simulate recorder errors after client-visible output has begun.
type FakeSecureSessionRecorder struct {
	mu sync.Mutex

	PostOutputFailure error

	TranscriptAppends int
	AuditAppends      int
	UsageAdds         int
	TouchCalls        int
	GateRecords       int
	StreamRecords     int
}

func (f *FakeSecureSessionRecorder) RecordClientTurnAfterGate(_ context.Context, _ app.ClientTurnRecordInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GateRecords++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}

func (f *FakeSecureSessionRecorder) RecordPostHookStreamEvent(_ context.Context, _ app.StreamEventRecordInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StreamRecords++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}

var _ app.GateRecording = (*FakeSecureSessionRecorder)(nil)

func (f *FakeSecureSessionRecorder) AppendTranscript(_ context.Context, _ domain.TranscriptItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TranscriptAppends++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}

func (f *FakeSecureSessionRecorder) AppendAudit(_ context.Context, _ domain.AuditItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AuditAppends++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}

func (f *FakeSecureSessionRecorder) AddUsage(_ context.Context, _ domain.UsageDelta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UsageAdds++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}

func (f *FakeSecureSessionRecorder) TouchActivity(_ context.Context, _ domain.SessionID, _ time.Time, _ domain.ActivitySource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TouchCalls++
	if f.PostOutputFailure != nil {
		return f.PostOutputFailure
	}
	return nil
}
