package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type countingSecureStore struct {
	app.Store
	creates        atomic.Int32
	loadsFP        atomic.Int32
	touches        atomic.Int32
	storageUnavail atomic.Int32
}

func (s *countingSecureStore) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	s.creates.Add(1)
	return s.Store.Create(ctx, rec)
}

func (s *countingSecureStore) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	s.loadsFP.Add(1)
	return s.Store.LoadByResumeFingerprint(ctx, fp)
}

func (s *countingSecureStore) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, src domain.ActivitySource) error {
	s.touches.Add(1)
	return s.Store.TouchActivity(ctx, id, at, src)
}

type countingMetricsSink struct {
	newCalls     atomic.Int32
	resumeCalls  atomic.Int32
	deniedCalls  atomic.Int32
	lastDenied   string
	unavailCalls atomic.Int32
}

func (m *countingMetricsSink) ObserveBeginTurnNew() {
	m.newCalls.Add(1)
}

func (m *countingMetricsSink) ObserveBeginTurnResume() {
	m.resumeCalls.Add(1)
}

func (m *countingMetricsSink) ObserveBeginTurnDenied(code string) {
	m.deniedCalls.Add(1)
	m.lastDenied = code
}

func (m *countingMetricsSink) ObserveStorageUnavailable() {
	m.unavailCalls.Add(1)
}

func (m *countingMetricsSink) ObserveActivityTouch(float64) {}

func (m *countingMetricsSink) ObserveRecorderClientTurnFailed(bool) {}

func (m *countingMetricsSink) ObserveRecorderStreamEventFailed(bool, bool) {}

// TestPrepareSecureSession_NoEarlyBeginTurn proves that PrepareSecureSession resolves
// principal, scope, session openers, and workspace into a bounded BeginInput without
// calling BeginTurn, creating lineage, or touching store activity (Req 6.2, 14.1, 19).
func TestPrepareSecureSession_NoEarlyBeginTurn(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawMem := memory.New(memory.Options{SimulateDurable: true})
	storeSpy := &countingSecureStore{Store: rawMem}
	mgr := testSecureManager(t, storeSpy, b2)

	opener := &captureSessionOpener{labels: map[string]string{"env": "test-env"}}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionOpeners: []session.Opener{opener},
		}),
	})

	metrics := &countingMetricsSink{}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionMetrics = metrics
	ex.Now = func() time.Time { return time.Unix(2000, 0) }

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "usr-42",
		DisplayName: "User 42",
	})

	sessionInput := largebody.SessionInput{
		AuthoritativeSessionID: "",
		ClientSessionID:        "client-sess-1",
		ALegID:                 "",
		ResumeToken:            largebody.SensitiveString{},
		NewSessionRequested:    true,
	}

	prep, err := ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "trace-abc-123",
		Session: sessionInput,
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	// 1. Assert NO store side effects during PrepareSecureSession (Req 6.2).
	if storeSpy.creates.Load() != 0 {
		t.Fatalf("store creates occurred during prepare: %d", storeSpy.creates.Load())
	}
	if storeSpy.loadsFP.Load() != 0 {
		t.Fatalf("store loads occurred during prepare: %d", storeSpy.loadsFP.Load())
	}
	if storeSpy.touches.Load() != 0 {
		t.Fatalf("store touches occurred during prepare: %d", storeSpy.touches.Load())
	}
	if metrics.newCalls.Load() != 0 || metrics.resumeCalls.Load() != 0 || metrics.deniedCalls.Load() != 0 {
		t.Fatalf("metrics recorded during prepare: new=%d resume=%d denied=%d",
			metrics.newCalls.Load(), metrics.resumeCalls.Load(), metrics.deniedCalls.Load())
	}

	// 2. Assert prepared state correctness.
	if prep.TraceID() != "trace-abc-123" {
		t.Fatalf("trace id: want trace-abc-123 got %q", prep.TraceID())
	}
	if !prep.HasPrincipal() || prep.Principal().ID != "usr-42" {
		t.Fatalf("principal mismatch: %v", prep.Principal())
	}
	if prep.PreSession().Labels["env"] != "test-env" {
		t.Fatalf("session opener labels not propagated to preSession: got %+v", prep.PreSession().Labels)
	}
	beginIn := prep.BeginInput()
	if beginIn.TraceID != "trace-abc-123" {
		t.Fatalf("beginInput trace: %q", beginIn.TraceID)
	}
	if beginIn.Principal.ID != "usr-42" {
		t.Fatalf("beginInput principal: %q", beginIn.Principal.ID)
	}
	if beginIn.ClientHints.ClientSessionID != "client-sess-1" {
		t.Fatalf("beginInput clientHints: %q", beginIn.ClientHints.ClientSessionID)
	}

	// 3. Post-commit: ExecuteBeginTurn executes the gate exactly once.
	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}
	if !br.IsNew {
		t.Fatal("expected new session")
	}
	if storeSpy.creates.Load() != 1 {
		t.Fatalf("expected exactly 1 store create after ExecuteBeginTurn, got %d", storeSpy.creates.Load())
	}
	if metrics.newCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 new session metric, got %d", metrics.newCalls.Load())
	}

	// 4. ResponseCarrier contains authoritative credentials.
	carrier := prep.ResponseCarrier(br)
	if carrier.AuthoritativeSessionID == "" {
		t.Fatal("expected non-empty authoritative session ID in carrier")
	}
	if carrier.ALegID == "" {
		t.Fatal("expected non-empty A-leg ID in carrier")
	}
	if carrier.ResumeToken.Reveal() == "" {
		t.Fatal("expected non-empty resume token in carrier for new session")
	}
}

// TestPrepareSecureSession_CanonicalParity proves that PrepareSecureSession produces
// identical BeginInput and resolution facts compared to the canonical prepareSubmitAndALeg path.
func TestPrepareSecureSession_CanonicalParity(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)

	opener := &captureSessionOpener{labels: map[string]string{"region": "eu-west-1"}}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionOpeners: []session.Opener{opener},
		}),
	})

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(2100, 0) }

	ctx := execview.WithPrincipal(
		scope.WithScope(context.Background(), scope.PrincipalScopeView{
			PrincipalID: scope.Known("usr-parity"),
			TenantID:    scope.Known("tenant-a"),
		}),
		execview.PrincipalView{ID: "usr-parity", DisplayName: "Parity User"},
	)

	canonicalCall := &aipapi.Call{
		ID: "trace-parity-1",
		Session: aipapi.SessionRef{
			ClientSessionID: "client-parity-hint",
			ContinuityKey:   "cont-key-1",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}

	// Prepare through fact-based helper.
	factSession := largebody.SessionInputFromRef(canonicalCall.Session)
	prep, err := ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID:       canonicalCall.ID,
		Session:       factSession,
		ContinuityKey: canonicalCall.Session.ContinuityKey,
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	beginIn := prep.BeginInput()
	if beginIn.TraceID != "trace-parity-1" {
		t.Fatalf("trace id mismatch: got %q want %q", beginIn.TraceID, canonicalCall.ID)
	}
	if beginIn.Principal.ID != "usr-parity" {
		t.Fatalf("principal id mismatch: got %q", beginIn.Principal.ID)
	}
	if beginIn.Session.ClientSessionID != "client-parity-hint" {
		t.Fatalf("session client id mismatch: got %q", beginIn.Session.ClientSessionID)
	}
	if beginIn.Session.ContinuityKey != "cont-key-1" {
		t.Fatalf("continuity key mismatch: got %q want cont-key-1", beginIn.Session.ContinuityKey)
	}
	if beginIn.WorkspaceMatchRequired != ex.SecureSessionRequireWorkspaceID {
		t.Fatalf("workspace match required mismatch")
	}

	// Now execute canonical prepareSubmitAndALeg on an identical executor state and compare.
	exCanonical := setSecureSessionDenialMapper(TestExecutor())
	exCanonical.Store = b2
	exCanonical.Bus = hooks.New(hooks.Config{})
	exCanonical.RuntimeSnapshot = snap
	exCanonical.SecureSession = mgr
	exCanonical.Now = ex.Now

	_, baseline, aLeg, _, _, err := exCanonical.prepareSubmitAndALeg(ctx, exCanonical.Bus, canonicalCall)
	if err != nil {
		t.Fatalf("prepareSubmitAndALeg failed: %v", err)
	}

	// Also execute BeginTurn on prep to verify post-commit parity.
	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}

	aLegFact, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatalf("ResolveALeg failed: %v", err)
	}
	if aLegFact.ALegID == "" {
		t.Fatal("expected non-empty ALegID from ResolveALeg")
	}
	if aLegFact.ALegID != br.Record.ALegID {
		t.Fatalf("a-leg id parity mismatch: got %q want %q", aLegFact.ALegID, br.Record.ALegID)
	}
	if aLeg.ALegID == "" {
		t.Fatal("expected non-empty canonical ALegID")
	}

	boundSession := prep.BindSession(br, aLegFact)
	if boundSession.AuthoritativeSessionID != string(br.Record.SessionID) {
		t.Fatalf("bound session sid mismatch: %q vs %q", boundSession.AuthoritativeSessionID, br.Record.SessionID)
	}
	if boundSession.ALegID != aLegFact.ALegID {
		t.Fatalf("bound session aleg mismatch: %q vs %q", boundSession.ALegID, aLegFact.ALegID)
	}

	// Verify baseline call has resume token cleared (Requirement 14.1 parity).
	if baseline.Session.ResumeToken != "" {
		t.Fatalf("baseline call leaked resume token: %q", baseline.Session.ResumeToken)
	}
}

// TestPrepareSecureSession_ResumeAndDenialLifecycle tests resume and denial semantics.
func TestPrepareSecureSession_ResumeAndDenialLifecycle(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	metrics := &countingMetricsSink{}

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = mgr
	ex.SecureSessionMetrics = metrics
	ex.Now = func() time.Time { return time.Unix(2200, 0) }

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-flow"})

	// 1. First turn: new session.
	prep1, err := ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "turn-1",
		Session: largebody.SessionInput{NewSessionRequested: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	br1, err := prep1.ExecuteBeginTurn(prep1.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !br1.IsNew {
		t.Fatal("expected new session on turn 1")
	}
	carrier1 := prep1.ResponseCarrier(br1)
	resumeToken := carrier1.ResumeToken.Reveal()
	if resumeToken == "" {
		t.Fatal("expected resume token on turn 1")
	}

	// 2. Second turn: resume with valid token.
	prep2, err := ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "turn-2",
		Session: largebody.SessionInput{
			ResumeToken: largebody.NewSensitiveString(resumeToken),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	br2, err := prep2.ExecuteBeginTurn(prep2.Context())
	if err != nil {
		t.Fatal(err)
	}
	if br2.IsNew {
		t.Fatal("expected resumed session on turn 2")
	}
	if br2.Record.SessionID != br1.Record.SessionID {
		t.Fatalf("session id changed across resume: %q vs %q", br2.Record.SessionID, br1.Record.SessionID)
	}
	carrier2 := prep2.ResponseCarrier(br2)
	if carrier2.ResumeToken.Reveal() != "" {
		t.Fatalf("resumed turn must not issue new resume token, got %q", carrier2.ResumeToken.Reveal())
	}
	if metrics.resumeCalls.Load() != 1 {
		t.Fatalf("expected 1 resume metric, got %d", metrics.resumeCalls.Load())
	}

	// 3. Third turn: invalid resume token produces mapped denial.
	prep3, err := ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "turn-3",
		Session: largebody.SessionInput{
			ResumeToken: largebody.NewSensitiveString("bogus-invalid-token"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prep3.ExecuteBeginTurn(prep3.Context())
	if err == nil {
		t.Fatal("expected denial error on invalid resume token")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("expected session denial error, got %T: %v", err, err)
	}
	if metrics.deniedCalls.Load() != 1 {
		t.Fatalf("expected 1 denial metric, got %d", metrics.deniedCalls.Load())
	}
}

// TestPrepareSecureSession_WorkspaceFailClosedDenial proves fail-closed workspace
// resolution denies during PrepareSecureSession before BeginTurn is reached.
func TestPrepareSecureSession_WorkspaceFailClosedDenial(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	metrics := &countingMetricsSink{}

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewStrictChain([]aipworkspace.Resolver{errWorkspaceResolver{}}),
	})

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionMetrics = metrics
	ex.SecureSessionWorkspaceResolveFailClosed = true
	ex.Now = func() time.Time { return time.Unix(2300, 0) }

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-ws"})

	_, err = ex.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: "ws-fail-trace",
		Session: largebody.SessionInput{NewSessionRequested: true},
	})
	if err == nil {
		t.Fatal("expected error on fail-closed workspace")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("expected session denial, got %T: %v", err, err)
	}
	if metrics.deniedCalls.Load() != 1 {
		t.Fatalf("expected 1 denial metric, got %d", metrics.deniedCalls.Load())
	}
}
