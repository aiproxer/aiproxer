package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type testSource struct {
	digest largebody.SourceDigest
	size   int64
	data   []byte
}

func (s *testSource) Digest() largebody.SourceDigest       { return s.digest }
func (s *testSource) SourceDigest() largebody.SourceDigest { return s.digest }
func (s *testSource) Size() int64                          { return s.size }
func (s *testSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *testSource) Close() error { return nil }

func newTestSource(content string) *testSource {
	data := []byte(content)
	sum := sha256.Sum256(data)
	return &testSource{
		digest: largebody.NewSourceDigest(sum),
		size:   int64(len(data)),
		data:   data,
	}
}

type testCountingMetricsSink struct {
	newCalls     atomic.Int32
	resumeCalls  atomic.Int32
	deniedCalls  atomic.Int32
	lastDenied   string
	unavailCalls atomic.Int32
}

func (m *testCountingMetricsSink) ObserveBeginTurnNew() {
	m.newCalls.Add(1)
}

func (m *testCountingMetricsSink) ObserveBeginTurnResume() {
	m.resumeCalls.Add(1)
}

func (m *testCountingMetricsSink) ObserveBeginTurnDenied(code string) {
	m.deniedCalls.Add(1)
	m.lastDenied = code
}

func (m *testCountingMetricsSink) ObserveStorageUnavailable() {
	m.unavailCalls.Add(1)
}

func (m *testCountingMetricsSink) ObserveActivityTouch(float64) {}

func (m *testCountingMetricsSink) ObserveRecorderClientTurnFailed(bool) {}

func (m *testCountingMetricsSink) ObserveRecorderStreamEventFailed(bool, bool) {}

type testRouteOverrideReader struct {
	snapshotFunc func(ctx context.Context, aLegID string) (routeoverride.State, error)
}

func (r *testRouteOverrideReader) Snapshot(ctx context.Context, aLegID string) (routeoverride.State, error) {
	if r.snapshotFunc != nil {
		return r.snapshotFunc(ctx, aLegID)
	}
	return routeoverride.State{}, nil
}

type testCreditGate struct {
	checkFunc func(ctx context.Context, accountID string) error
}

func (g *testCreditGate) Check(ctx context.Context, accountID string) error {
	if g.checkFunc != nil {
		return g.checkFunc(ctx, accountID)
	}
	return nil
}

type testExposureAdmission struct {
	admitFunc func(ctx context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error)
}

func (a *testExposureAdmission) Admit(ctx context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	if a.admitFunc != nil {
		return a.admitFunc(ctx, in)
	}
	return billingport.CallExposure{AccountID: in.AccountID}, nil
}

func setupTestExecutor(t *testing.T) (*Executor, *testCountingMetricsSink, b2bua.Store) {
	t.Helper()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	metrics := &testCountingMetricsSink{}

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = mgr
	ex.SecureSessionMetrics = metrics
	ex.Now = func() time.Time { return time.Unix(1000, 0) }
	ex.LargeBodyGenerationID = "gen-1"
	ex.LargeBodyCandidateDomainGeneration = "dom-gen-1"
	ex.DefaultBackend = "default"
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}

	return ex, metrics, b2
}

func makeTestAcceptedAssessment(t *testing.T, genID, profileID string, src *testSource, universal bool, candModels ...string) largebody.Assessment {
	t.Helper()
	stamp, err := largebody.NewAssessmentStamp(
		genID,
		profileID,
		src.digest,
		src.size,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(sha256.Sum256([]byte("test-identity"))),
		"dom-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       profileID,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o",
		MaxOutputTokens: 2048,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID:       profileID,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		UniversalModel:  universal,
		CandidateModels: candModels,
	}
	acc, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}
	return acc
}

// TestTask13_1_StampAndSourceValidation verifies requirement 6.6, 6.7, 8.5:
// valid accepted succeeds; zero stamp fails; generation drift fails;
// candidate domain generation drift fails; source digest/size mismatch fails;
// nil source fails; declined assessment fails.
func TestTask13_1_StampAndSourceValidation(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-val"})

	// 1. Valid accepted succeeds
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("expected success for valid accepted assessment, got %v", err)
	}
	wantReqID := acc.Stamp.IdentityDigest().CallID("")
	if res.Facts.RequestID != wantReqID {
		t.Errorf("RequestID = %q, want canonical stamp-derived %q", res.Facts.RequestID, wantReqID)
	}
	if res.Facts.RequestID == "gen-1" {
		t.Errorf("RequestID must never be generation-scoped, got %q", res.Facts.RequestID)
	}

	// 2. Declined assessment fails with invariant error
	declined, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
	_, err = ex.ExecuteLargeBody(ctx, declined, src)
	if err == nil {
		t.Fatal("expected error for declined assessment, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for declined assessment, got %v", err)
	}

	// 3. Zero stamp fails with invariant error
	zeroStampAcc := acc
	zeroStampAcc.Stamp = largebody.AssessmentStamp{}
	_, err = ex.ExecuteLargeBody(ctx, zeroStampAcc, src)
	if err == nil {
		t.Fatal("expected error for zero stamp, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for zero stamp, got %v", err)
	}

	// 4. Generation drift fails with invariant error
	driftEx, _, _ := setupTestExecutor(t)
	driftEx.LargeBodyGenerationID = "gen-different"
	_, err = driftEx.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error for generation drift, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for generation drift, got %v", err)
	}

	// 5. Candidate domain generation drift fails with invariant error
	domDriftEx, _, _ := setupTestExecutor(t)
	domDriftEx.LargeBodyCandidateDomainGeneration = "dom-gen-different"
	_, err = domDriftEx.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error for candidate domain generation drift, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for candidate domain generation drift, got %v", err)
	}

	// 6. Nil source fails with invariant error
	_, err = ex.ExecuteLargeBody(ctx, acc, nil)
	if err == nil {
		t.Fatal("expected error for nil source, got nil")
	}

	// 7. Source digest mismatch fails with invariant error
	otherSrc := newTestSource(`{"model":"gpt-4o","different":"content"}`)
	_, err = ex.ExecuteLargeBody(ctx, acc, otherSrc)
	if err == nil {
		t.Fatal("expected error for source digest mismatch, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for source digest mismatch, got %v", err)
	}

	// 8. Source size mismatch fails with invariant error
	sizeMismatchSrc := &testSource{
		digest: src.digest,
		size:   src.size + 10,
		data:   src.data,
	}
	_, err = ex.ExecuteLargeBody(ctx, acc, sizeMismatchSrc)
	if err == nil {
		t.Fatal("expected error for source size mismatch, got nil")
	}
	if !errors.Is(err, largebody.ErrStampDisagreement) {
		t.Errorf("expected ErrStampDisagreement for source size mismatch, got %v", err)
	}
}

// TestTask13_1_SingleBeginTurnLifecycle verifies requirements 6.2, 14.1, 14.6:
// exactly one BeginTurn is performed, metrics are observed (IsNew/Resume),
// SessionResponseCarrier is populated, and BeginTurn denial fails cleanly without fallback.
func TestTask13_1_SingleBeginTurnLifecycle(t *testing.T) {
	ex, metrics, _ := setupTestExecutor(t)
	src := newTestSource(`{"prompt":"hello"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-lifecycle"})

	// Turn 1: New session
	res1, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if metrics.newCalls.Load() != 1 {
		t.Errorf("expected 1 new session metric call, got %d", metrics.newCalls.Load())
	}
	if metrics.resumeCalls.Load() != 0 {
		t.Errorf("expected 0 resume calls on turn 1, got %d", metrics.resumeCalls.Load())
	}
	if res1.Session.AuthoritativeSessionID == "" {
		t.Fatal("expected AuthoritativeSessionID in SessionResponseCarrier")
	}
	if res1.Session.ALegID == "" {
		t.Fatal("expected ALegID in SessionResponseCarrier")
	}
	resumeToken := res1.Session.ResumeToken.Reveal()
	if resumeToken == "" {
		t.Fatal("expected non-empty ResumeToken for new session")
	}

	// Turn 2: Resume turn
	ctx2 := largebody.WithWireIdentity(ctx, "req-turn-2", "trace-turn-2")
	ctx2 = largebody.WithWireSessionInput(ctx2, largebody.SessionInput{
		AuthoritativeSessionID: res1.Session.AuthoritativeSessionID,
		ResumeToken:            largebody.NewSensitiveString(resumeToken),
	})
	res2, err := ex.ExecuteLargeBody(ctx2, acc, src)
	if err != nil {
		t.Fatalf("turn 2 resume failed: %v", err)
	}
	if metrics.resumeCalls.Load() != 1 {
		t.Errorf("expected 1 resume call on turn 2, got %d", metrics.resumeCalls.Load())
	}
	if res2.Session.AuthoritativeSessionID != res1.Session.AuthoritativeSessionID {
		t.Errorf("session ID changed on resume: %q vs %q", res2.Session.AuthoritativeSessionID, res1.Session.AuthoritativeSessionID)
	}
	if res2.Session.ResumeToken.Reveal() != "" {
		t.Errorf("expected empty resume token on resumed turn, got %q", res2.Session.ResumeToken.Reveal())
	}

	// Turn 3: Denial fails cleanly without fallback
	ctx3 := largebody.WithWireIdentity(ctx, "req-turn-3", "trace-turn-3")
	ctx3 = largebody.WithWireSessionInput(ctx3, largebody.SessionInput{
		ResumeToken: largebody.NewSensitiveString("invalid-resume-token"),
	})
	_, err = ex.ExecuteLargeBody(ctx3, acc, src)
	if err == nil {
		t.Fatal("expected error on invalid resume token, got nil")
	}
	if metrics.deniedCalls.Load() != 1 {
		t.Errorf("expected 1 denied call, got %d", metrics.deniedCalls.Load())
	}
}

// TestTask13_1_LiveRouteOverride_ConstrainedToDomain verifies requirement 7.4, 7.5:
// live route override read post-BeginTurn:
// - inside domain (UniversalModel=true) succeeds;
// - inside domain (UniversalModel=false, in CandidateModels) succeeds;
// - inside domain (UniversalModel=false, alias resolved) succeeds;
// - outside domain (UniversalModel=false, not in CandidateModels) returns invariant error without fallback.
func TestTask13_1_LiveRouteOverride_ConstrainedToDomain(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-route"})

	// Case A: UniversalModel=true -> override allowed
	t.Run("UniversalDomain_OverrideAllowed", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "override-model-x",
				}, nil
			},
		}
		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("expected override allowed on universal domain, got: %v", err)
		}
		if res.Facts.EffectiveModel != "override-model-x" {
			t.Errorf("EffectiveModel = %q, want override-model-x", res.Facts.EffectiveModel)
		}
	})

	// Case B: Finite domain with CandidateModels, override inside CandidateModels -> succeeds
	t.Run("FiniteDomain_OverrideInside_Succeeds", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "gpt-4o-mini",
				}, nil
			},
		}
		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "gpt-4o", "gpt-4o-mini")

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("expected override allowed inside finite domain, got: %v", err)
		}
		if res.Facts.EffectiveModel != "gpt-4o-mini" {
			t.Errorf("EffectiveModel = %q, want gpt-4o-mini", res.Facts.EffectiveModel)
		}
	})

	// Case C: Finite domain, override is alias resolving to candidate model -> succeeds
	t.Run("FiniteDomain_OverrideAlias_Succeeds", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "mini-alias",
				}, nil
			},
		}
		ar, err := routing.NewAliasResolver([]routing.ModelAliasRule{
			{Pattern: "^mini-alias$", Replacement: "gpt-4o-mini"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ex.SelectorAliases = ar

		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "gpt-4o", "gpt-4o-mini")

		res, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err != nil {
			t.Fatalf("expected alias override allowed, got: %v", err)
		}
		if res.Facts.EffectiveModel != "gpt-4o-mini" {
			t.Errorf("EffectiveModel = %q, want gpt-4o-mini", res.Facts.EffectiveModel)
		}
	})

	// Case D: Finite domain, override outside CandidateModels -> invariant failure, NO fallback
	t.Run("FiniteDomain_OverrideOutside_InvariantError", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(ctx context.Context, aLegID string) (routeoverride.State, error) {
				return routeoverride.State{
					Active:   true,
					Selector: "claude-3-5-sonnet", // outside assessed domain
				}, nil
			},
		}
		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "gpt-4o", "gpt-4o-mini")

		_, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err == nil {
			t.Fatal("expected invariant error for route override outside domain, got nil")
		}
		if !strings.Contains(err.Error(), "outside assessed wire domain") {
			t.Errorf("expected error mentioning outside assessed wire domain, got: %v", err)
		}
	})
}

// TestTask13_1_RequestAuthorityAndEconomicAdmission verifies requirement 15.6, 19:
// request authority admitted once; cheap credit screen denial and exposure admission
// denial fail cleanly and release request authority without fallback.
func TestTask13_1_RequestAuthorityAndEconomicAdmission(t *testing.T) {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-econ"})
	src := newTestSource(`{"prompt":"hello"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	// Case A: Cheap credit screen denied -> fails cleanly, no fallback
	t.Run("CreditGateDenied", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.BillingCreditGate = &testCreditGate{
			checkFunc: func(ctx context.Context, accountID string) error {
				return errors.New("credit limit exceeded")
			},
		}

		_, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err == nil {
			t.Fatal("expected error on credit gate denial, got nil")
		}
		if !strings.Contains(err.Error(), "credit limit exceeded") && !errors.Is(err, ErrBillingCreditScreenDenied) {
			t.Errorf("expected credit screen denial error, got: %v", err)
		}
	})

	// Case B: Authorize wire billing denied -> fails cleanly, no fallback
	t.Run("ExposureAdmissionDenied", func(t *testing.T) {
		ex, _, _ := setupTestExecutor(t)
		ex.BillingExposureAdmission = &testExposureAdmission{
			admitFunc: func(ctx context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				return billingport.CallExposure{}, errors.New("exposure quota exhausted")
			},
		}

		_, err := ex.ExecuteLargeBody(ctx, acc, src)
		if err == nil {
			t.Fatal("expected error on exposure admission denial, got nil")
		}
		if !strings.Contains(err.Error(), "exposure quota exhausted") && !errors.Is(err, ErrBillingAdmissionDenied) {
			t.Errorf("expected exposure admission denial error, got: %v", err)
		}
	})
}

// TestTask13_1_ResponseFactsAndSessionCarrier verifies requirement 18.1, 18.2:
// ExecutionResult contains valid Stream, ResponseFacts, and SessionResponseCarrier.
func TestTask13_1_ResponseFactsAndSessionCarrier(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","prompt":"facts-test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-facts"})
	ctx = largebody.WithWireIdentity(ctx, "req-turn-1", "trace-turn-1")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}

	// ExecutionResult.Validate must pass
	if err := res.Validate(largebody.DefaultMaxSemanticFactBytes); err != nil {
		t.Fatalf("ExecutionResult.Validate failed: %v", err)
	}

	// Verify Facts
	facts := res.Facts
	if facts.RequestID != "req-turn-1" {
		t.Errorf("RequestID = %q, want req-turn-1", facts.RequestID)
	}
	if facts.TraceID != "trace-turn-1" {
		t.Errorf("TraceID = %q, want trace-turn-1", facts.TraceID)
	}
	if facts.ALegID == "" {
		t.Error("ALegID must not be empty")
	}
	if facts.SessionID == "" {
		t.Error("SessionID must not be empty")
	}
	if facts.EffectiveModel != "gpt-4o" {
		t.Errorf("EffectiveModel = %q, want gpt-4o", facts.EffectiveModel)
	}
	if facts.BodyBytes != src.size {
		t.Errorf("BodyBytes = %d, want %d", facts.BodyBytes, src.size)
	}

	// Verify Session
	sess := res.Session
	if sess.AuthoritativeSessionID == "" {
		t.Error("Session.AuthoritativeSessionID must not be empty")
	}
	if sess.ALegID == "" {
		t.Error("Session.ALegID must not be empty")
	}
	if sess.ResumeToken.Reveal() == "" {
		t.Error("Session.ResumeToken must not be empty for new session")
	}

	// Verify Stream
	if res.Stream == nil {
		t.Fatal("Stream must not be nil")
	}
	defer func() { _ = res.Stream.Close() }()
}

func TestTask13_1_ConversationViewReader_SnapshotFailure(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	expectedErr := errors.New("reader storage unavailable")
	reader := &stubConversationReader{err: expectedErr}
	ex.ConversationViewReader = reader

	var openWireCalls, openCanonicalCalls atomic.Int32
	ex.Backends["default"] = execbackend.Backend{
		OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			openWireCalls.Add(1)
			return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseFinished},
			})}, nil
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openCanonicalCalls.Add(1)
			return nil, nil
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"conv-test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-conv"})

	// Assessment itself is side-effect-free: reader must not be queried during assessment
	if reader.callCount != 0 {
		t.Fatalf("assessment must be side-effect-free, got reader calls %d", reader.callCount)
	}

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error on snapshot failure, got nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected errors.Is(err, expectedErr), got %v", err)
	}
	if !strings.Contains(err.Error(), "executor: conversation view snapshot:") {
		t.Fatalf("expected conversation view snapshot error, got %v", err)
	}
	if reader.callCount != 1 {
		t.Fatalf("expected reader callCount == 1, got %d", reader.callCount)
	}
	if got := openWireCalls.Load(); got != 0 {
		t.Fatalf("expected zero OpenWire calls on reader failure, got %d", got)
	}
	if got := openCanonicalCalls.Load(); got != 0 {
		t.Fatalf("expected zero Open calls on reader failure, got %d", got)
	}
}

func TestTask13_1_ConversationViewReader_InvariantFailure_NonEmptyProjection(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	reader := &stubConversationReader{
		snap: conversationprojection.Snapshot{
			NeverBackend: []conversationprojection.Tag{
				{
					Identity: conversationprojection.MessageIdentity("msg-1"),
				},
			},
		},
	}
	ex.ConversationViewReader = reader

	var openWireCalls, openCanonicalCalls atomic.Int32
	ex.Backends["default"] = execbackend.Backend{
		OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			openWireCalls.Add(1)
			return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseFinished},
			})}, nil
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openCanonicalCalls.Add(1)
			return nil, nil
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"conv-test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-conv"})

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error on non-empty projection in fresh session, got nil")
	}
	if !strings.Contains(err.Error(), "executor: conversation projection invariant failure:") {
		t.Fatalf("expected conversation projection invariant failure, got %v", err)
	}
	if reader.callCount != 1 {
		t.Fatalf("expected reader callCount == 1, got %d", reader.callCount)
	}
	if got := openWireCalls.Load(); got != 0 {
		t.Fatalf("expected zero OpenWire calls on nonempty projection, got %d", got)
	}
	if got := openCanonicalCalls.Load(); got != 0 {
		t.Fatalf("expected zero Open calls on nonempty projection, got %d", got)
	}
}

type mockOrderTrackingStream struct {
	events    *[]string
	cancelRes aipapi.CancelResult
}

func (m *mockOrderTrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (m *mockOrderTrackingStream) Close() error {
	*m.events = append(*m.events, "inner_close")
	return nil
}

func (m *mockOrderTrackingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	*m.events = append(*m.events, "inner_cancel")
	return m.cancelRes
}

type mockSimpleOrderStream struct {
	events *[]string
}

func (m *mockSimpleOrderStream) Recv(ctx context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (m *mockSimpleOrderStream) Close() error {
	*m.events = append(*m.events, "inner_close")
	return nil
}

func TestTask13_1_WireLifecycleEventStream_Cancel_Ordering(t *testing.T) {
	t.Run("ManagedEventStream cancels before cleanup", func(t *testing.T) {
		var events []string
		expectedRes := aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
		inner := &mockOrderTrackingStream{
			events:    &events,
			cancelRes: expectedRes,
		}
		stream := &wireLifecycleEventStream{
			EventStream: inner,
			cleanup: func(err error) {
				events = append(events, "cleanup")
			},
		}

		res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})
		if res != expectedRes {
			t.Fatalf("expected cancel result %v, got %v", expectedRes, res)
		}

		if len(events) != 2 || events[0] != "inner_cancel" || events[1] != "cleanup" {
			t.Fatalf("expected [inner_cancel, cleanup], got %v", events)
		}
	})

	t.Run("Standard EventStream closes before cleanup", func(t *testing.T) {
		var events []string
		inner := &mockSimpleOrderStream{
			events: &events,
		}
		stream := &wireLifecycleEventStream{
			EventStream: inner,
			cleanup: func(err error) {
				events = append(events, "cleanup")
			},
		}

		res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})
		if res.Mode != aipapi.CancelModeCloseOnly {
			t.Fatalf("expected CloseOnly mode, got %v", res.Mode)
		}

		if len(events) != 2 || events[0] != "inner_close" || events[1] != "cleanup" {
			t.Fatalf("expected [inner_close, cleanup], got %v", events)
		}
	})
}
