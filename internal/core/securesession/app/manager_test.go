package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func testFingerprintKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func testManager(t *testing.T, st app.Store, lin app.LineageStore, cfg app.ManagerConfig) *app.Manager {
	t.Helper()
	m, err := app.NewManager(st, app.NewRandGenerator(cfg.FingerprintKey), lin, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDefaultGlobalPolicy_nonEmptyBaseline(t *testing.T) {
	t.Parallel()
	p := app.DefaultGlobalPolicy()
	if p.PolicyVersion == "" || p.AuditMode == "" {
		t.Fatalf("%+v", p)
	}
}

// --- 5.1 new session ---

type countingLineage struct {
	mu    sync.Mutex
	calls int
}

func (c *countingLineage) CreateALeg(ctx context.Context, continuityKey string) (app.LineageALeg, error) {
	_ = ctx
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	return app.LineageALeg{ALegID: fmt.Sprintf("a_test_%d", n), ContinuityKey: continuityKey}, nil
}

func (c *countingLineage) FetchALeg(ctx context.Context, aLegID string) (app.LineageALeg, error) {
	return app.LineageALeg{ALegID: aLegID}, nil
}

func (c *countingLineage) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return nil
}

func TestManager_BeginTurn_newSession_proxyOwnedIDsAndALeg(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := &countingLineage{}
	key := testFingerprintKey(t)
	m := testManager(t, st, lin, app.ManagerConfig{
		FingerprintKey: key,
		StoreDurable:   true,
	})

	in := app.BeginInput{
		Now:          time.Unix(1000, 0),
		Principal:    domain.PrincipalRef{ID: "user-1"},
		Workspace:    domain.WorkspaceRef{ID: "ws-1"},
		GlobalPolicy: domain.PolicyMetadata{EffectiveTreatment: "standard"},
		Session: app.SessionWire{
			ClientSessionID: "client-hint-only",
			SessionID:       "attacker-supplied-authority",
		},
	}
	got, err := m.BeginTurn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsNew {
		t.Fatal("expected new session")
	}
	if got.Response.ResumeToken == "" || got.Response.SessionID == "" {
		t.Fatalf("expected resume metadata, got %#v", got.Response)
	}
	if string(got.Record.SessionID) != got.Response.SessionID {
		t.Fatalf("record session id mismatch")
	}
	if got.Record.SessionID == domain.SessionID(in.Session.SessionID) {
		t.Fatal("client-supplied SessionID must not become authoritative")
	}
	if got.Record.ALegID == "" || !strings.HasPrefix(got.Record.ALegID, "a_test_") {
		t.Fatalf("expected lineage A-leg on record, got %q", got.Record.ALegID)
	}
	if lin.calls != 1 {
		t.Fatalf("CreateALeg calls: %d", lin.calls)
	}
}

func TestManager_BeginTurn_newSession_concurrentDistinctIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := &countingLineage{}
	key := testFingerprintKey(t)
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})

	const n = 32
	ids := make(map[string]struct{})
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			got, err := m.BeginTurn(ctx, app.BeginInput{
				Now:       time.Unix(2000, 0),
				Principal: domain.PrincipalRef{ID: "u"},
				Session:   app.SessionWire{},
			})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[string(got.Record.SessionID)] = struct{}{}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != n {
		t.Fatalf("distinct session ids: want %d got %d", n, len(ids))
	}
}

// --- 5.2 resume validation ---

func seedSession(ctx context.Context, t *testing.T, st app.Store, lin app.LineageStore, key []byte, owner domain.PrincipalRef, ws domain.WorkspaceRef, lastAct time.Time, policy domain.PolicyMetadata) (domain.SessionID, domain.ResumeToken) {
	t.Helper()
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true, ResumeWindow: time.Hour})
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:          time.Unix(500, 0),
		Principal:    owner,
		Workspace:    ws,
		GlobalPolicy: policy,
		Session:      app.SessionWire{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// rewind last activity for resume-window tests
	if !lastAct.IsZero() && !lastAct.Equal(got.Record.LastActivityAt) {
		if err := st.TouchActivity(ctx, got.Record.SessionID, lastAct, domain.ActivityRemoteEvent); err != nil {
			t.Fatal(err)
		}
	}
	return got.Record.SessionID, got.Response.ResumeToken
}

func TestManager_BeginTurn_resume_invalidCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	owner := domain.PrincipalRef{ID: "owner-1"}
	ws := domain.WorkspaceRef{ID: "ws-a"}

	t.Run("bad_token_unknown_fingerprint", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: owner,
			Workspace: ws,
			Session: app.SessionWire{
				ResumeToken: "not-a-valid-resume-token-for-any-session",
			},
		})
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("got %v want ErrSessionNotFound", err)
		}
	})

	t.Run("session_id_token_mismatch", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		_, tok := seedSession(ctx, t, st, lin, key, owner, ws, time.Time{}, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: owner,
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   "wrong-session-id",
				ResumeToken: string(tok),
			},
		})
		if !errors.Is(err, domain.ErrInvalidResumeToken) {
			t.Fatalf("got %v want ErrInvalidResumeToken", err)
		}
	})

	t.Run("wrong_owner_resume_non_enumerating", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		sid, tok := seedSession(ctx, t, st, lin, key, owner, ws, time.Time{}, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: domain.PrincipalRef{ID: "other-user"},
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
			},
		})
		// Fingerprint mixes principal id; another principal yields a different digest, so lookup fails without leaking existence.
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("got %v want ErrSessionNotFound", err)
		}
	})

	t.Run("issuer_mismatch_same_fingerprint_material", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		o := domain.PrincipalRef{ID: "same-id", Issuer: "issuer-a", Tenant: "t1"}
		sid, tok := seedSession(ctx, t, st, lin, key, o, ws, time.Time{}, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: domain.PrincipalRef{ID: "same-id", Issuer: "issuer-b", Tenant: "t1"},
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
			},
		})
		if !errors.Is(err, domain.ErrOwnerMismatch) {
			t.Fatalf("got %v want ErrOwnerMismatch", err)
		}
	})

	t.Run("wrong_ALeg_hint_non_enumerating", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		sid, tok := seedSession(ctx, t, st, lin, key, owner, ws, time.Time{}, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: owner,
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
				ALegID:      "forged-a-leg",
			},
		})
		if !errors.Is(err, domain.ErrSessionNotFound) {
			t.Fatalf("got %v want ErrSessionNotFound", err)
		}
	})

	t.Run("resume_expired_by_window", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		old := time.Unix(1000, 0)
		sid, tok := seedSession(ctx, t, st, lin, key, owner, ws, old, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{
			FingerprintKey: key,
			StoreDurable:   true,
			ResumeWindow:   30 * time.Minute,
		})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       old.Add(45 * time.Minute),
			Principal: owner,
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
			},
		})
		if !errors.Is(err, domain.ErrResumeExpired) {
			t.Fatalf("got %v want ErrResumeExpired", err)
		}
	})

	t.Run("unbounded_window_old_activity_ok", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		old := time.Unix(1, 0)
		sid, tok := seedSession(ctx, t, st, lin, key, owner, ws, old, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{
			FingerprintKey: key,
			StoreDurable:   true,
			ResumeWindow:   0,
		})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(1_000_000, 0),
			Principal: owner,
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing_principal", func(t *testing.T) {
		t.Parallel()
		st := memory.New(memory.Options{SimulateDurable: true})
		lin := b2bualineage.New(testB2BUA(t))
		sid, tok := seedSession(ctx, t, st, lin, key, owner, ws, time.Time{}, domain.PolicyMetadata{})
		m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
		_, err := m.BeginTurn(ctx, app.BeginInput{
			Now:       time.Unix(900, 0),
			Principal: domain.PrincipalRef{},
			Workspace: ws,
			Session: app.SessionWire{
				SessionID:   string(sid),
				ResumeToken: string(tok),
			},
		})
		if !errors.Is(err, domain.ErrMissingPrincipal) {
			t.Fatalf("got %v want ErrMissingPrincipal", err)
		}
	})
}

func testB2BUA(t *testing.T) *b2bua.MemoryStore {
	t.Helper()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// --- 5.3 workspace + policy merge (separate subtests for 5.4 structure) ---

func TestManager_BeginTurn_workspaceDeniedOnResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	owner := domain.PrincipalRef{ID: "u1"}
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	sid, tok := seedSession(ctx, t, st, lin, key, owner, domain.WorkspaceRef{ID: "ws-locked"}, time.Time{}, domain.PolicyMetadata{})
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	_, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(800, 0),
		Principal: owner,
		Workspace: domain.WorkspaceRef{ID: "other-ws"},
		Session: app.SessionWire{
			SessionID:   string(sid),
			ResumeToken: string(tok),
		},
	})
	if !errors.Is(err, domain.ErrWorkspaceDenied) {
		t.Fatalf("got %v want ErrWorkspaceDenied", err)
	}
}

func TestManager_BeginTurn_workspaceRequiredMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	key := testFingerprintKey(t)
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	_, err := m.BeginTurn(ctx, app.BeginInput{
		Now:                    time.Unix(800, 0),
		Principal:              domain.PrincipalRef{ID: "u"},
		WorkspaceMatchRequired: true,
		Workspace:              domain.WorkspaceRef{},
		Session:                app.SessionWire{},
	})
	if !errors.Is(err, domain.ErrPolicyUnavailable) {
		t.Fatalf("got %v want ErrPolicyUnavailable", err)
	}
}

func TestManager_BeginTurn_policyMerge_stricterGlobalWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	owner := domain.PrincipalRef{ID: "u1"}
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))

	storedPol := domain.PolicyMetadata{
		EffectiveTreatment: "relaxed",
		AuditMode:          "best_effort",
		TranscriptEnabled:  true,
		RedactionProfile:   "standard",
	}
	sid, tok := seedSession(ctx, t, st, lin, key, owner, domain.WorkspaceRef{}, time.Time{}, storedPol)

	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(900, 0),
		Principal: owner,
		Session: app.SessionWire{
			SessionID:   string(sid),
			ResumeToken: string(tok),
		},
		GlobalPolicy: domain.PolicyMetadata{
			EffectiveTreatment: "strict",
			AuditMode:          "mandatory",
			TranscriptEnabled:  false,
			RedactionProfile:   "strict",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectivePolicy.EffectiveTreatment != "strict" {
		t.Fatalf("EffectiveTreatment: got %q", got.EffectivePolicy.EffectiveTreatment)
	}
	if got.EffectivePolicy.AuditMode != "mandatory" {
		t.Fatalf("AuditMode: got %q", got.EffectivePolicy.AuditMode)
	}
	if got.EffectivePolicy.TranscriptEnabled {
		t.Fatal("transcript should be disabled when global disables (stricter)")
	}
	if got.EffectivePolicy.RedactionProfile != "strict" {
		t.Fatalf("RedactionProfile: got %q", got.EffectivePolicy.RedactionProfile)
	}
}

func TestManager_BeginTurn_noWorkspaceExplicitEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(700, 0),
		Principal: domain.PrincipalRef{ID: "u"},
		Session:   app.SessionWire{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Workspace.ID != "" {
		t.Fatalf("expected empty workspace id, got %q", got.Record.Workspace.ID)
	}
}

// --- 5.5 routing metadata + first-turn lineage ---

func TestManager_BeginTurn_effectiveRouteHintFromSessionRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	owner := domain.PrincipalRef{ID: "u1"}
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	sid, tok := seedSession(ctx, t, st, lin, key, owner, domain.WorkspaceRef{}, time.Time{}, domain.PolicyMetadata{
		RouteHint: "session-bound-route",
	})
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(900, 0),
		Principal: owner,
		Session: app.SessionWire{
			SessionID:   string(sid),
			ResumeToken: string(tok),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.EffectivePolicy.RouteHint != "session-bound-route" {
		t.Fatalf("RouteHint: got %q", got.EffectivePolicy.RouteHint)
	}
}

func TestManager_BeginTurn_resume_doesNotResetWeightedFirstConsumed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	owner := domain.PrincipalRef{ID: "u1"}
	b := testB2BUA(t)
	lin := b2bualineage.New(b)
	st := memory.New(memory.Options{SimulateDurable: true})
	sid, tok := seedSession(ctx, t, st, lin, key, owner, domain.WorkspaceRef{}, time.Time{}, domain.PolicyMetadata{})

	rec0, err := st.LoadByID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	al0, err := b.FetchALeg(ctx, rec0.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if al0.WeightedFirstConsumed {
		t.Fatal("expected WeightedFirstConsumed false on new A-leg")
	}
	if err := b.SetWeightedFirstConsumed(ctx, rec0.ALegID, true); err != nil {
		t.Fatal(err)
	}

	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	_, err = m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(910, 0),
		Principal: owner,
		Session: app.SessionWire{
			SessionID:   string(sid),
			ResumeToken: string(tok),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	al1, err := b.FetchALeg(ctx, rec0.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !al1.WeightedFirstConsumed {
		t.Fatal("resume must not clear WeightedFirstConsumed")
	}
}

// --- 5.6 mandatory readiness + FinishTurn ---

func TestManager_BeginTurn_mandatoryReadinessFailsBeforeLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	st := memory.New(memory.Options{
		SimulateDurable: true,
		ReadinessError:  domain.ErrStorageUnavailable,
	})
	lin := &countingLineage{}
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	_, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(100, 0),
		Principal: domain.PrincipalRef{ID: "u"},
		Session:   app.SessionWire{},
	})
	if !errors.Is(err, domain.ErrStorageUnavailable) {
		t.Fatalf("got %v want ErrStorageUnavailable", err)
	}
	if lin.calls != 0 {
		t.Fatalf("lineage should not run when readiness fails: calls=%d", lin.calls)
	}
}

func TestManager_BeginTurn_mandatoryAuditWithoutDurableStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := testFingerprintKey(t)
	st := memory.New(memory.Options{SimulateDurable: false})
	lin := b2bualineage.New(testB2BUA(t))
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: false})
	_, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(100, 0),
		Principal: domain.PrincipalRef{ID: "u"},
		Session:   app.SessionWire{},
		GlobalPolicy: domain.PolicyMetadata{
			AuditMode: "mandatory",
		},
	})
	if !errors.Is(err, domain.ErrMandatoryAuditFailure) {
		t.Fatalf("got %v want ErrMandatoryAuditFailure", err)
	}
}

//nolint:paralleltest // Serial FinishTurn calls mutate shared manager/session; audit scan is sequential.
func TestManager_FinishTurn_auditOutcomes(t *testing.T) {
	ctx := context.Background()
	key := testFingerprintKey(t)
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	m := testManager(t, st, lin, app.ManagerConfig{FingerprintKey: key, StoreDurable: true})
	got, err := m.BeginTurn(ctx, app.BeginInput{
		Now:       time.Unix(300, 0),
		Principal: domain.PrincipalRef{ID: "u"},
		Session:   app.SessionWire{},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid := got.Record.SessionID
	tid := got.TurnID

	kinds := []struct {
		k    app.TurnOutcomeKind
		want string
	}{
		{app.TurnOutcomeSuccess, "success"},
		{app.TurnOutcomePreOutputDenied, "pre_output_denied"},
		{app.TurnOutcomeSurfacedFailure, "surfaced_failure"},
		{app.TurnOutcomePostOutputRecorderFailure, "post_output_recorder_failure"},
		{app.TurnOutcomeUnknown, "unknown"},
	}
	for _, tc := range kinds {
		t.Run(tc.want, func(t *testing.T) {
			if err := m.FinishTurn(ctx, sid, tid, app.TurnOutcome{Kind: tc.k}); err != nil {
				t.Fatal(err)
			}
		})
	}
	aud, err := st.Audit(ctx, sid, domain.ReadOptions{Limit: 20, AfterSeq: 0})
	if err != nil {
		t.Fatal(err)
	}
	var gotResults []string
	for _, a := range aud {
		if a.Action == "turn_outcome" {
			gotResults = append(gotResults, a.Result)
		}
	}
	if len(gotResults) < len(kinds) {
		t.Fatalf("audit outcomes: %#v", gotResults)
	}
	found := map[string]bool{}
	for _, r := range gotResults {
		found[r] = true
	}
	for _, tc := range kinds {
		if !found[tc.want] {
			t.Fatalf("missing audit result %q in %#v", tc.want, gotResults)
		}
	}
}

func TestManager_resume_fingerprintPrincipalOnly_ignoresAgentDigestDrift(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{SimulateDurable: true})
	lin := b2bualineage.New(testB2BUA(t))
	key := testFingerprintKey(t)
	m := testManager(t, st, lin, app.ManagerConfig{
		FingerprintKey:                 key,
		StoreDurable:                   true,
		ResumeFingerprintPrincipalOnly: true,
	})
	newIn := app.BeginInput{
		Now:          time.Unix(4000, 0),
		Principal:    domain.PrincipalRef{ID: "u-principal"},
		Workspace:    domain.WorkspaceRef{ID: "w"},
		GlobalPolicy: domain.PolicyMetadata{EffectiveTreatment: "standard"},
		Session:      app.SessionWire{},
		ClientHints:  domain.ClientHints{AgentIdentityDigest: "agent-v1"},
	}
	got, err := m.BeginTurn(ctx, newIn)
	if err != nil {
		t.Fatal(err)
	}
	tok := string(got.Response.ResumeToken)
	sid := string(got.Record.SessionID)

	resumeIn := app.BeginInput{
		Now:          time.Unix(4001, 0),
		Principal:    domain.PrincipalRef{ID: "u-principal"},
		Workspace:    domain.WorkspaceRef{ID: "w"},
		GlobalPolicy: domain.PolicyMetadata{EffectiveTreatment: "standard"},
		Session: app.SessionWire{
			SessionID:   sid,
			ResumeToken: tok,
		},
		ClientHints: domain.ClientHints{AgentIdentityDigest: "agent-v2-different"},
	}
	if _, err := m.BeginTurn(ctx, resumeIn); err != nil {
		t.Fatalf("resume with drifted agent digest should succeed under principal-only fingerprint: %v", err)
	}
}
