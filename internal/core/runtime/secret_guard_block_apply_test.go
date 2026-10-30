package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

type recordingSecretGuardMetrics struct {
	decisions   int
	matches     int
	quarantines int
	failures    int
	scanLimits  int
}

func (m *recordingSecretGuardMetrics) IncDecision(action, outcome, sourceCategory string) {
	m.decisions++
}

func (m *recordingSecretGuardMetrics) IncMatch(action, outcome, sourceCategory string) { m.matches++ }

func (m *recordingSecretGuardMetrics) IncQuarantine(action, outcome, sourceCategory string) {
	m.quarantines++
}

func (m *recordingSecretGuardMetrics) IncFailure(action, outcome, sourceCategory string) {
	m.failures++
}

func (m *recordingSecretGuardMetrics) IncScanLimit(action, outcome, sourceCategory string) {
	m.scanLimits++
}

type countingBLeg struct {
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (b *countingBLeg) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (b *countingBLeg) Close() error {
	b.closeCount.Add(1)
	return nil
}

func (b *countingBLeg) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	b.cancelCount.Add(1)
	return aipapi.CancelResult{}
}

type cancelingQuarantineStore struct {
	app.Store
	cancel func()
}

func (s *cancelingQuarantineStore) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	err := s.Store.Quarantine(ctx, in)
	if err == nil && s.cancel != nil {
		s.cancel()
	}
	return err
}

type auditCollector struct {
	mu     sync.Mutex
	events []secretguard.DecisionEvent
	err    error
}

func (c *auditCollector) OnSecretDecision(_ context.Context, ev secretguard.DecisionEvent) error {
	c.mu.Lock()
	c.events = append(c.events, ev)
	err := c.err
	c.mu.Unlock()
	return err
}

func (c *auditCollector) one(t *testing.T) secretguard.DecisionEvent {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) != 1 {
		t.Fatalf("audit events=%d want 1", len(c.events))
	}
	return c.events[0]
}

func TestExecutor_applySecretGuardBlock_precedenceAndCounts(t *testing.T) {
	t.Parallel()

	const (
		sessionID = "sess-apply"
		aLegID    = "aleg-apply"
		turnID    = "turn-apply"
	)
	baseCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	block := &extensions.SecretGuardBlockInfo{
		GuardID: "secrets-guard",
		Decision: secretguard.Decision{
			Outcome: secretguard.OutcomeBlock,
			Findings: []secretguard.Finding{{
				SecretRefName:   "OPENAI_API_KEY",
				SourceCategory:  secretguard.SourceCategoryProxyEnv,
				Location:        "messages[0].parts[0].text",
				OccurrenceCount: 1,
			}},
		},
	}

	tests := []struct {
		name                 string
		secureSession        func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore)
		sessionID            string
		auditErr             error
		auditPolicy          secretguard.AuditFailurePolicy
		wantDenialCode       string
		wantPolicyDenied     bool
		wantStorageFault     bool
		wantQuarantine       string
		wantCommittedCancel  bool
		wantStoreQuarantined bool
	}{
		{
			name: "no_secure_session_manager",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				return nil, nil, nil
			},
			sessionID:        "",
			auditPolicy:      secretguard.AuditFailClosed,
			wantPolicyDenied: true,
			wantQuarantine:   secretguard.QuarantineResultSkipped,
		},
		{
			name: "missing_session_id",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				mgr := newBlockApplyManager(t, memSS)
				return mgr, memSS, nil
			},
			sessionID:        "",
			auditPolicy:      secretguard.AuditFailClosed,
			wantDenialCode:   string(aipapi.SessionDeniedStorageUnavailable),
			wantStorageFault: true,
			wantQuarantine:   secretguard.QuarantineResultFailed,
		},
		{
			name: "quarantine_store_error",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				fake := &testkit.FakeSecureSessionStore{Delegate: memSS, QuarantineErr: errors.New("disk full")}
				mgr := newBlockApplyManager(t, fake)
				return mgr, memSS, fake
			},
			sessionID:        sessionID,
			auditPolicy:      secretguard.AuditFailClosed,
			wantDenialCode:   string(aipapi.SessionDeniedStorageUnavailable),
			wantStorageFault: true,
			wantQuarantine:   secretguard.QuarantineResultFailed,
		},
		{
			name: "successful_quarantine",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				mgr := newBlockApplyManager(t, memSS)
				seedBlockSession(t, memSS, sessionID, aLegID)
				return mgr, memSS, nil
			},
			sessionID:            sessionID,
			auditPolicy:          secretguard.AuditFailClosed,
			wantPolicyDenied:     true,
			wantQuarantine:       secretguard.QuarantineResultCommitted,
			wantCommittedCancel:  true,
			wantStoreQuarantined: true,
		},
		{
			name: "fail_closed_audit_wins_over_store_error",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				fake := &testkit.FakeSecureSessionStore{Delegate: memSS, QuarantineErr: errors.New("disk full")}
				mgr := newBlockApplyManager(t, fake)
				seedBlockSession(t, memSS, sessionID, aLegID)
				return mgr, memSS, fake
			},
			sessionID:        sessionID,
			auditErr:         errors.New("audit sink down"),
			auditPolicy:      secretguard.AuditFailClosed,
			wantDenialCode:   string(aipapi.SessionDeniedMandatoryAuditFailure),
			wantStorageFault: true,
			wantQuarantine:   secretguard.QuarantineResultFailed,
		},
		{
			name: "best_effort_audit_falls_back_to_storage",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				fake := &testkit.FakeSecureSessionStore{Delegate: memSS, QuarantineErr: errors.New("disk full")}
				mgr := newBlockApplyManager(t, fake)
				seedBlockSession(t, memSS, sessionID, aLegID)
				return mgr, memSS, fake
			},
			sessionID:        sessionID,
			auditErr:         errors.New("audit sink down"),
			auditPolicy:      secretguard.AuditBestEffort,
			wantDenialCode:   string(aipapi.SessionDeniedStorageUnavailable),
			wantStorageFault: true,
			wantQuarantine:   secretguard.QuarantineResultFailed,
		},
		{
			name: "fail_closed_audit_after_committed_quarantine",
			secureSession: func(t *testing.T) (*app.Manager, *memory.Store, *testkit.FakeSecureSessionStore) {
				t.Helper()
				memSS := memory.New(memory.Options{SimulateDurable: true})
				mgr := newBlockApplyManager(t, memSS)
				seedBlockSession(t, memSS, sessionID, aLegID)
				return mgr, memSS, nil
			},
			sessionID:            sessionID,
			auditErr:             errors.New("audit sink down"),
			auditPolicy:          secretguard.AuditFailClosed,
			wantDenialCode:       string(aipapi.SessionDeniedMandatoryAuditFailure),
			wantQuarantine:       secretguard.QuarantineResultCommitted,
			wantCommittedCancel:  true,
			wantStoreQuarantined: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			audit := &auditCollector{err: tc.auditErr}
			metrics := &recordingSecretGuardMetrics{}
			mgr, memSS, fake := tc.secureSession(t)
			ctx := t.Context()
			ex := runtime.TestExecutor()
			ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
				SecretGuardPlane: extensions.SecretGuardPlane{
					DecisionObserver:   audit,
					AuditFailurePolicy: tc.auditPolicy,
					AccessMode:         "single_user",
					ConfigVersion:      "cfg-apply",
				},
			})
			ex.SecretGuardDecisionMetrics = metrics
			ex.SecureSession = mgr
			ex.ALegLifecycle = leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
			ex.Now = func() time.Time { return time.Unix(2500, 0).UTC() }

			if tc.wantCommittedCancel {
				leg := ex.ALegLifecycle.StartALeg(aLegID)
				canceling := &countingBLeg{}
				if err := leg.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "b1", Attempt: canceling}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if got := canceling.cancelCount.Load(); got != 1 {
						t.Fatalf("cancel count=%d want 1", got)
					}
					if got := canceling.closeCount.Load(); got != 1 {
						t.Fatalf("close count=%d want 1", got)
					}
				})
			}

			err := ex.ApplySecretGuardBlockForTest(ctx, baseCall, runtime.SecretGuardStageInputForTest{
				TraceID:   "trace-apply",
				Principal: execview.PrincipalView{ID: "user-apply"},
				Session: session.SessionView{
					AuthoritativeSessionID: sessionID,
					ALegID:                 aLegID,
				},
				SessionID: sessionID,
				TurnID:    turnID,
			}, block)

			if tc.wantDenialCode != "" {
				if code := aipapi.SessionDenialPublicCode(err); code != tc.wantDenialCode {
					t.Fatalf("denial code=%q want %q (err=%v)", code, tc.wantDenialCode, err)
				}
			} else if !tc.wantPolicyDenied {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
			} else if !aipapi.IsPolicyDenied(err) {
				t.Fatalf("want policy denied, got %v", err)
			}

			if metrics.decisions != 0 || metrics.quarantines != 1 {
				t.Fatalf("metrics=%+v", metrics)
			}
			ev := audit.one(t)
			if ev.QuarantineResult != tc.wantQuarantine {
				t.Fatalf("quarantine_result=%q want %q", ev.QuarantineResult, tc.wantQuarantine)
			}
			if ev.Outcome != secretguard.OutcomeBlock {
				t.Fatalf("outcome=%q want block", ev.Outcome)
			}
			if tc.wantStoreQuarantined {
				rec, err := memSS.LoadByID(ctx, domain.SessionID(sessionID))
				if err != nil {
					t.Fatal(err)
				}
				if !rec.Status.IsQuarantined() || rec.ResumeEligible {
					t.Fatalf("store state: status=%q resume=%v", rec.Status, rec.ResumeEligible)
				}
				if err := mgr.AssertActive(ctx, domain.SessionID(sessionID)); !errors.Is(err, domain.ErrSessionQuarantined) {
					t.Fatalf("AssertActive returned %v want ErrSessionQuarantined", err)
				}
			}
			if tc.wantStorageFault && !ex.QuarantinePersistenceFaulted() {
				t.Fatal("quarantine persistence fault must be latched")
			}
			if !tc.wantStorageFault && ex.QuarantinePersistenceFaulted() {
				t.Fatal("quarantine persistence fault must remain unset")
			}
			if fake != nil && fake.QuarantineCalls == 0 && tc.sessionID != "" && tc.wantQuarantine != secretguard.QuarantineResultSkipped {
				t.Fatal("quarantine should have been attempted")
			}
		})
	}
}

func TestExecutor_applySecretGuardBlock_cancelDuringQuarantineStillCancelsALeg(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	const (
		sessionID = "sess-cleanup"
		aLegID    = "aleg-cleanup"
		turnID    = "turn-cleanup"
	)
	memSS := memory.New(memory.Options{SimulateDurable: true})
	key := secretGuardFingerprintKey(t)
	store := &cancelingQuarantineStore{Store: memSS, cancel: cancel}
	mgr, err := app.NewManager(store, app.NewRandGenerator(key), b2bualineage.New(newB2BuaStore(t)), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memSS.Create(t.Context(), domain.CreateRecord{
		SessionID:         domain.SessionID(sessionID),
		ResumeFingerprint: domain.TokenFingerprint{1},
		Owner:             domain.PrincipalRef{ID: "user-cleanup"},
		ALegID:            aLegID,
		ResumeEligible:    true,
		CreatedAt:         time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		SecretGuardPlane: extensions.SecretGuardPlane{
			AuditFailurePolicy: secretguard.AuditFailClosed,
		},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			SecretGuards: []secretguard.Guard{&blockingSecretGuard{}},
		}),
	})
	ex.SecureSession = mgr
	ex.ALegLifecycle = leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	ex.Now = func() time.Time { return time.Unix(2600, 0).UTC() }

	leg := ex.ALegLifecycle.StartALeg(aLegID)
	canceling := &countingBLeg{}
	if err := leg.RegisterBLeg(t.Context(), leglifecycle.BLegHandle{ID: "b1", Attempt: canceling}); err != nil {
		t.Fatal(err)
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("token=" + testkit.SyntheticOpenAIAPIKey)},
		}},
	}
	err = ex.ApplySecretGuardBlockForTest(ctx, call, runtime.SecretGuardStageInputForTest{
		TraceID:   "trace-cleanup",
		Principal: execview.PrincipalView{ID: "user-cleanup"},
		Session: session.SessionView{
			AuthoritativeSessionID: sessionID,
			ALegID:                 aLegID,
		},
		SessionID: domain.SessionID(sessionID),
		TurnID:    turnID,
	}, &extensions.SecretGuardBlockInfo{
		GuardID: "secrets-guard",
		Decision: secretguard.Decision{
			Outcome: secretguard.OutcomeBlock,
			Findings: []secretguard.Finding{{
				SecretRefName:   "OPENAI_API_KEY",
				SourceCategory:  secretguard.SourceCategoryProxyEnv,
				Location:        "messages[0].parts[0].text",
				OccurrenceCount: 1,
			}},
		},
	})
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("expected policy denied, got %v", err)
	}
	if got := canceling.cancelCount.Load(); got != 1 {
		t.Fatalf("cancel count=%d want 1", got)
	}
	if got := canceling.closeCount.Load(); got != 1 {
		t.Fatalf("close count=%d want 1", got)
	}

	rec, err := memSS.LoadByID(t.Context(), domain.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Status.IsQuarantined() || rec.ResumeEligible {
		t.Fatalf("quarantine state: status=%q resume=%v", rec.Status, rec.ResumeEligible)
	}
}

func newBlockApplyManager(t *testing.T, store app.Store) *app.Manager {
	t.Helper()
	key := secretGuardFingerprintKey(t)
	mgr, err := app.NewManager(store, app.NewRandGenerator(key), b2bualineage.New(newB2BuaStore(t)), app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func newB2BuaStore(t *testing.T) b2bua.Store {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func seedBlockSession(t *testing.T, store *memory.Store, sessionID, aLegID string) {
	t.Helper()
	if _, err := store.Create(t.Context(), domain.CreateRecord{
		SessionID:         domain.SessionID(sessionID),
		ResumeFingerprint: domain.TokenFingerprint{1},
		Owner:             domain.PrincipalRef{ID: "user-apply"},
		ALegID:            aLegID,
		ResumeEligible:    true,
		CreatedAt:         time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}
