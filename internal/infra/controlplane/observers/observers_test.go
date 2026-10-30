package observers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// fixedTime is the deterministic time used across observer tests.
var fixedTime = time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

// harness wires a real normalizer, recorder, and in-memory store so adapter
// tests can assert fan-out and non-interference with deterministic events.
type harness struct {
	t        *testing.T
	store    *ledgerstore.MemoryStore
	status   *controlplane.Status
	recorder *controlplane.RecorderService
	normal   *controlplane.Normalizer
}

func newHarness(t *testing.T, policy cp.RecordingPolicy, required []cp.Category) *harness {
	t.Helper()
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "obs-test"})
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: policy})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy:   policy,
		Required: required,
		Clock:    fixedClock{t: fixedTime},
	})
	normal := controlplane.NewNormalizer(
		fixedClock{t: fixedTime},
		cp.SourceRef{Name: "observers-test", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
	return &harness{t: t, store: store, status: status, recorder: recorder, normal: normal}
}

func (h *harness) disabledRecorder() *controlplane.RecorderService {
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: cp.RecordingDisabled})
	return controlplane.NewRecorderService(h.store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingDisabled,
		Clock:  fixedClock{t: fixedTime},
	})
}

// events returns all recorded events in store order.
func (h *harness) events() []cp.Event {
	page, err := h.store.Events(context.Background(), cp.EventQuery{Limit: 100})
	if err != nil {
		h.t.Fatalf("events query: %v", err)
	}
	return page.Items
}

func knownScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("principal-1"),
		CredentialID:   scope.Known("cred-1"),
		TenantID:       scope.Known("tenant-1"),
		OrganizationID: scope.Known("org-1"),
		Origin:         scope.OriginClient,
		Roles:          []string{"ops"},
	}
}

//go:fix inline
func ptrScope(v scope.PrincipalScopeView) *scope.PrincipalScopeView { return new(v) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func mustMarshal(t *testing.T, ev cp.Event) []byte {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// captureAuthSink is a test-only auth.EventSink recording calls and optional error.
type captureAuthSink struct {
	mu          sync.Mutex
	authCalls   int
	sessionCall int
	lastAuth    sdkauth.AuthDecisionEvent
	lastSession sdkauth.SessionStartEvent
	authErr     error
	sessionErr  error
}

func (s *captureAuthSink) OnAuthDecision(_ context.Context, ev sdkauth.AuthDecisionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authCalls++
	s.lastAuth = ev
	return s.authErr
}

func (s *captureAuthSink) OnSessionStart(_ context.Context, ev sdkauth.SessionStartEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionCall++
	s.lastSession = ev
	return s.sessionErr
}

// failingStore is a test-only controlplane.Store whose Append always returns err.
type failingStore struct{ err error }

func (s *failingStore) Append(context.Context, cp.Event) (cp.RecordResult, error) {
	return cp.RecordResult{}, s.err
}

func (s *failingStore) Sessions(context.Context, cp.SessionQuery) (cp.Page[cp.SessionSummary], error) {
	return cp.Page[cp.SessionSummary]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) Attempts(context.Context, cp.AttemptQuery) (cp.Page[cp.AttemptRow], error) {
	return cp.Page[cp.AttemptRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) Usage(context.Context, cp.UsageQuery) (cp.Page[cp.UsageRow], error) {
	return cp.Page[cp.UsageRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) UsageAggregate(context.Context, cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error) {
	return cp.Page[cp.UsageAggregate]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) PolicyAudit(context.Context, cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error) {
	return cp.Page[cp.PolicyAuditRow]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) Events(context.Context, cp.EventQuery) (cp.Page[cp.Event], error) {
	return cp.Page[cp.Event]{Visibility: cp.VisibilityDefault}, nil
}

func (s *failingStore) ApplyRetention(context.Context, controlplane.RetentionCommand) (controlplane.RetentionResult, error) {
	return controlplane.RetentionResult{}, nil
}
func (s *failingStore) CheckReadiness(context.Context) error { return nil }

var _ controlplane.Store = (*failingStore)(nil)

// newFailingRecorder returns a RecorderService backed by a store that always
// fails Append, so adapter fail-closed/degradation behavior can be exercised.
func newFailingRecorder(t *testing.T, status *controlplane.Status, policy cp.RecordingPolicy, required []cp.Category) *controlplane.RecorderService {
	t.Helper()
	return controlplane.NewRecorderService(&failingStore{err: errors.New("store down")}, status, controlplane.RecorderConfig{
		Policy:   policy,
		Required: required,
		Clock:    fixedClock{t: fixedTime},
	})
}

// fakeSecureSessionStore is a test-only app.Store tracking mutating calls and
// returning configurable results. Reads return empty/zero values.
type fakeSecureSessionStore struct {
	mu sync.Mutex

	createErr   error
	createRec   domain.Record
	createCalls int

	touchErr   error
	touchCalls int
	lastTouch  struct {
		id     domain.SessionID
		at     time.Time
		source domain.ActivitySource
	}

	appendAttemptTraceErr   error
	appendAttemptTraceCalls int
	lastAttemptTrace        domain.AttemptTrace

	updateAttemptOutcomeErr   error
	updateAttemptOutcomeCalls int
	lastAttemptOutcome        domain.AttemptOutcome

	quarantineErr   error
	quarantineCalls int
	lastQuarantine  domain.QuarantineInput

	addUsageErr   error
	addUsageCalls int
	lastUsage     domain.UsageDelta

	appendAuditErr   error
	appendAuditCalls int
	lastAudit        domain.AuditItem

	appendTranscriptErr error
	nextTranscriptSeq   int64
	nextTranscriptErr   error
}

func (f *fakeSecureSessionStore) Create(_ context.Context, rec domain.CreateRecord) (domain.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return domain.Record{}, f.createErr
	}
	if f.createRec.SessionID != "" {
		return f.createRec, nil
	}
	return domain.Record{
		SessionID: rec.SessionID,
		ALegID:    rec.ALegID,
		Owner:     rec.Owner,
		CreatedAt: rec.CreatedAt,
	}, nil
}

func (f *fakeSecureSessionStore) LoadByID(context.Context, domain.SessionID) (domain.Record, error) {
	return domain.Record{}, nil
}

func (f *fakeSecureSessionStore) LoadByResumeFingerprint(context.Context, domain.TokenFingerprint) (domain.Record, error) {
	return domain.Record{}, nil
}

func (f *fakeSecureSessionStore) LoadByALegID(context.Context, string) (domain.Record, error) {
	return domain.Record{}, nil
}

func (f *fakeSecureSessionStore) TouchActivity(_ context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touchCalls++
	f.lastTouch.id = id
	f.lastTouch.at = at
	f.lastTouch.source = source
	return f.touchErr
}

func (f *fakeSecureSessionStore) AppendAttemptTrace(_ context.Context, trace domain.AttemptTrace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendAttemptTraceCalls++
	f.lastAttemptTrace = trace
	return f.appendAttemptTraceErr
}

func (f *fakeSecureSessionStore) UpdateAttemptOutcome(_ context.Context, outcome domain.AttemptOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateAttemptOutcomeCalls++
	f.lastAttemptOutcome = outcome
	return f.updateAttemptOutcomeErr
}

func (f *fakeSecureSessionStore) Quarantine(_ context.Context, in domain.QuarantineInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quarantineCalls++
	f.lastQuarantine = in
	return f.quarantineErr
}

func (f *fakeSecureSessionStore) AppendTranscript(context.Context, domain.TranscriptItem) error {
	return nil
}

func (f *fakeSecureSessionStore) NextTranscriptSeq(context.Context, domain.SessionID) (int64, error) {
	return 1, nil
}

func (f *fakeSecureSessionStore) AddUsage(_ context.Context, delta domain.UsageDelta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addUsageCalls++
	f.lastUsage = delta
	return f.addUsageErr
}

func (f *fakeSecureSessionStore) NextAuditSeq(context.Context, domain.SessionID) (int64, error) {
	return 1, nil
}

func (f *fakeSecureSessionStore) AppendAudit(_ context.Context, item domain.AuditItem) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendAuditCalls++
	f.lastAudit = item
	return f.appendAuditErr
}

func (f *fakeSecureSessionStore) Audit(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AuditItem, error) {
	return nil, nil
}

func (f *fakeSecureSessionStore) Summary(context.Context, domain.SummaryQuery) ([]domain.Summary, error) {
	return nil, nil
}

func (f *fakeSecureSessionStore) Transcript(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.TranscriptItem, error) {
	return nil, nil
}

func (f *fakeSecureSessionStore) ListAttemptEvidence(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

func (f *fakeSecureSessionStore) CheckReadiness(context.Context, domain.PolicyMetadata) error {
	return nil
}

var _ app.Store = (*fakeSecureSessionStore)(nil)

// fakeB2BUAStore is a test-only b2bua.Store tracking RecordAttempt calls.
type fakeB2BUAStore struct {
	mu               sync.Mutex
	recordAttemptErr error
	recordCalls      int
	lastAttempt      aipapi.AttemptRecord
	allocateErr      error
}

func (f *fakeB2BUAStore) ResolveALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (f *fakeB2BUAStore) CreateALeg(_ context.Context, _ string) (b2bua.ALegRecord, error) {
	if f.allocateErr != nil {
		return b2bua.ALegRecord{}, f.allocateErr
	}
	return b2bua.ALegRecord{ALegID: "aleg-1"}, nil
}

func (f *fakeB2BUAStore) FetchALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (f *fakeB2BUAStore) SetWeightedFirstConsumed(context.Context, string, bool) error { return nil }

func (f *fakeB2BUAStore) NextBLeg(context.Context, string) (b2bua.BLegRecord, error) {
	return b2bua.BLegRecord{BLegID: "bleg-1", ALegID: "aleg-1", Seq: 1}, nil
}

func (f *fakeB2BUAStore) RecordAttempt(_ context.Context, rec aipapi.AttemptRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCalls++
	f.lastAttempt = rec
	return f.recordAttemptErr
}

func (f *fakeB2BUAStore) LoadAttempts(context.Context, string) ([]aipapi.AttemptRecord, error) {
	return nil, nil
}

var _ b2bua.Store = (*fakeB2BUAStore)(nil)
