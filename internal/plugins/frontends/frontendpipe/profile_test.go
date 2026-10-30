package frontendpipe_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// stubExecutor implements aipsdk.ExecutorView without largebody.LargeBodyExecutor.
type stubExecutor struct{}

func (s *stubExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return nil, nil
}

func (s *stubExecutor) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	return nil
}

func (s *stubExecutor) WallClock() func() time.Time {
	return nil
}

// stubLargeBodyExecutor implements aipsdk.ExecutorView AND largebody.LargeBodyExecutor.
type stubLargeBodyExecutor struct {
	stubExecutor
}

func (s *stubLargeBodyExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	return largebody.Assessment{}, nil
}

func (s *stubLargeBodyExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	return largebody.ExecutionResult{}, nil
}

var (
	_ aipsdk.ExecutorView         = (*stubLargeBodyExecutor)(nil)
	_ largebody.LargeBodyExecutor = (*stubLargeBodyExecutor)(nil)
)

// testProfile implements frontendpipe.FrontendProfile for testing.
type testProfile struct {
	id          string
	compileFunc func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error)
}

func (p *testProfile) ProfileID() string {
	if p.id != "" {
		return p.id
	}
	return "test_profile_v1"
}

func (p *testProfile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	if p.compileFunc != nil {
		return p.compileFunc(ctx, in)
	}
	return frontendpipe.ProofOutput{}, errors.New("not implemented")
}

var _ frontendpipe.FrontendProfile = (*testProfile)(nil)

func TestCandidatePrerequisites_NilProfileOrCapability(t *testing.T) {
	t.Parallel()

	// 1. Nil spec => not candidate
	if exec, ok := frontendpipe.CandidatePrerequisites[struct{}](nil); ok || exec != nil {
		t.Fatalf("nil spec: want (nil, false), got (%v, %v)", exec, ok)
	}

	// 2. Spec with nil Profile => canonical with no spool
	specNoProfile := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: &stubLargeBodyExecutor{},
		},
		Profile: nil,
	}
	if exec, ok := frontendpipe.CandidatePrerequisites(specNoProfile); ok || exec != nil {
		t.Fatalf("nil Profile: want (nil, false), got (%v, %v)", exec, ok)
	}

	// 3. Spec with Profile but nil Exec => canonical with no spool
	specNilExec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: nil,
		},
		Profile: &testProfile{},
	}
	if exec, ok := frontendpipe.CandidatePrerequisites(specNilExec); ok || exec != nil {
		t.Fatalf("nil Exec: want (nil, false), got (%v, %v)", exec, ok)
	}

	// 4. Spec with Profile and plain Exec (missing LargeBodyExecutor capability) => canonical with no spool
	specPlainExec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: &stubExecutor{},
		},
		Profile: &testProfile{},
	}
	if exec, ok := frontendpipe.CandidatePrerequisites(specPlainExec); ok || exec != nil {
		t.Fatalf("missing LargeBodyExecutor: want (nil, false), got (%v, %v)", exec, ok)
	}

	// 5. Spec with Profile AND LargeBodyExecutor capability => accepted for candidate evaluation
	lbe := &stubLargeBodyExecutor{}
	specValid := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: lbe,
		},
		Profile: &testProfile{},
	}
	exec, ok := frontendpipe.CandidatePrerequisites(specValid)
	if !ok || exec == nil {
		t.Fatalf("valid Profile + LargeBodyExecutor: want (exec, true), got (%v, %v)", exec, ok)
	}
	if exec != lbe {
		t.Fatalf("exec mismatch: want %p, got %p", lbe, exec)
	}
}

func TestResponseStateSeeds_ValidationAndHelpers(t *testing.T) {
	t.Parallel()

	const maxBudget = 256
	validDigest := largebody.NewIdentityDigest([32]byte{1, 2, 3, 4})
	sess := largebody.SessionInput{
		AuthoritativeSessionID: "sess_123",
		ALegID:                 "aleg_456",
	}

	seeds := frontendpipe.NewResponseStateSeeds(
		validDigest,
		"req_custom_1",
		"openai/gpt-4o",
		"gpt-4o",
		true,
		sess,
		"canc_789",
	)

	// Valid seeds pass validation
	if err := seeds.Validate(maxBudget); err != nil {
		t.Fatalf("valid seeds: unexpected error: %v", err)
	}

	// Helper getters
	if got := seeds.EffectiveCallID(); got != "req_custom_1" {
		t.Errorf("EffectiveCallID with explicit ID: want req_custom_1, got %s", got)
	}
	if got := seeds.EffectiveTimestamp(); got != validDigest.Unix() {
		t.Errorf("EffectiveTimestamp: want %d, got %d", validDigest.Unix(), got)
	}

	// Fallback to deterministic call ID when explicit ID is empty
	seedsNoExplicit := frontendpipe.NewResponseStateSeeds(
		validDigest,
		"",
		"openai/gpt-4o",
		"gpt-4o",
		false,
		sess,
		"",
	)
	if got := seedsNoExplicit.EffectiveCallID(); got != validDigest.CallID("") {
		t.Errorf("EffectiveCallID without explicit ID: want %s, got %s", validDigest.CallID(""), got)
	}

	// Non-positive budget rejects
	if err := seeds.Validate(0); err == nil {
		t.Error("budget 0: want error, got nil")
	}
	if err := seeds.Validate(-1); err == nil {
		t.Error("budget -1: want error, got nil")
	}

	// Oversized fields reject
	giantStr := strings.Repeat("x", maxBudget+1)

	tests := []struct {
		name   string
		mutate func(s *frontendpipe.ResponseStateSeeds)
	}{
		{"DeterministicCallID", func(s *frontendpipe.ResponseStateSeeds) { s.DeterministicCallID = giantStr }},
		{"DeterministicToken", func(s *frontendpipe.ResponseStateSeeds) { s.DeterministicToken = giantStr }},
		{"ExplicitRequestID", func(s *frontendpipe.ResponseStateSeeds) { s.ExplicitRequestID = giantStr }},
		{"RouteSelector", func(s *frontendpipe.ResponseStateSeeds) { s.RouteSelector = giantStr }},
		{"ClientModel", func(s *frontendpipe.ResponseStateSeeds) { s.ClientModel = giantStr }},
		{"CancellationID", func(s *frontendpipe.ResponseStateSeeds) { s.CancellationID = giantStr }},
		{"SessionID", func(s *frontendpipe.ResponseStateSeeds) { s.SessionID = giantStr }},
		{"ALegID", func(s *frontendpipe.ResponseStateSeeds) { s.ALegID = giantStr }},
	}

	for _, tc := range tests {
		t.Run("oversized_"+tc.name, func(t *testing.T) {
			t.Parallel()
			bad := seeds
			tc.mutate(&bad)
			if err := bad.Validate(maxBudget); err == nil {
				t.Fatalf("oversized %s: want validation error, got nil", tc.name)
			}
		})
	}
}

func TestFrontendWireState_ValidationAndConsistency(t *testing.T) {
	t.Parallel()

	const maxBudget = 256
	profileID := "test_profile_v1"
	digest := largebody.NewIdentityDigest([32]byte{5, 6, 7, 8})
	srcDigest := largebody.NewSourceDigest([32]byte{9, 10, 11, 12})
	sess := largebody.SessionInput{
		AuthoritativeSessionID: "sess_auth",
		ALegID:                 "aleg_wire",
	}

	proof := largebody.Proof{
		ProfileID:       profileID,
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "test/selector",
		ClientModel:     "test-model",
		MaxOutputTokens: 1024,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "std_reqs",
			ControlCount:   0,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		ModelSpan: largebody.Span{},
		Identity:  digest,
		Turn: largebody.ClientTurnShape{
			TotalContentBytes: 100,
		},
		Session:              sess,
		Source:               srcDigest,
		BodyBytes:            1024,
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}

	seeds := frontendpipe.NewResponseStateSeeds(
		digest,
		"",
		"test/selector",
		"test-model",
		true,
		sess,
		"canc_wire",
	)

	validState := frontendpipe.FrontendWireState{
		ProfileID: profileID,
		Proof:     proof,
		Seeds:     seeds,
	}

	if err := validState.Validate(maxBudget); err != nil {
		t.Fatalf("valid FrontendWireState: unexpected error: %v", err)
	}

	// Test ProofOutput convenience getters and validation
	out := frontendpipe.ProofOutput{State: validState}
	if out.Proof().ProfileID != profileID {
		t.Errorf("out.Proof() mismatch: want %s, got %s", profileID, out.Proof().ProfileID)
	}
	if out.Seeds().ClientModel != "test-model" {
		t.Errorf("out.Seeds() mismatch: want test-model, got %s", out.Seeds().ClientModel)
	}
	if err := out.Validate(maxBudget); err != nil {
		t.Fatalf("out.Validate(): unexpected error: %v", err)
	}

	// Consistency checks:
	// 1. ProfileID mismatch between WireState and Proof
	badProfileID := validState
	badProfileID.ProfileID = "other_profile"
	if err := badProfileID.Validate(maxBudget); err == nil {
		t.Error("profile id mismatch: want error, got nil")
	}

	// 2. Empty ProfileID
	emptyProfileID := validState
	emptyProfileID.ProfileID = ""
	if err := emptyProfileID.Validate(maxBudget); err == nil {
		t.Error("empty profile id: want error, got nil")
	}

	// 3. RouteSelector mismatch between Seeds and Proof
	badRoute := validState
	badRoute.Seeds.RouteSelector = "different/selector"
	if err := badRoute.Validate(maxBudget); err == nil {
		t.Error("route selector mismatch: want error, got nil")
	}

	// 4. ClientModel mismatch between Seeds and Proof
	badModel := validState
	badModel.Seeds.ClientModel = "different-model"
	if err := badModel.Validate(maxBudget); err == nil {
		t.Error("client model mismatch: want error, got nil")
	}

	// 5. Stream flag mismatch between Seeds and Proof
	badStream := validState
	badStream.Seeds.Stream = false // Proof has DeliveryModeStreaming
	if err := badStream.Validate(maxBudget); err == nil {
		t.Error("stream flag mismatch: want error, got nil")
	}

	// 6. DeterministicCallID mismatch with Proof.Identity
	badCallID := validState
	badCallID.Seeds.DeterministicCallID = "call_wrongtoken"
	if err := badCallID.Validate(maxBudget); err == nil {
		t.Error("deterministic call ID mismatch: want error, got nil")
	}

	// 7. DeterministicTimestamp mismatch with Proof.Identity
	badTS := validState
	badTS.Seeds.DeterministicTimestamp = 99999
	if err := badTS.Validate(maxBudget); err == nil {
		t.Error("deterministic timestamp mismatch: want error, got nil")
	}

	// 8. SessionID mismatch with Proof.Session
	badSessID := validState
	badSessID.Seeds.SessionID = "other_sess"
	if err := badSessID.Validate(maxBudget); err == nil {
		t.Error("session ID mismatch: want error, got nil")
	}

	// 9. ALegID mismatch with Proof.Session
	badALegID := validState
	badALegID.Seeds.ALegID = "other_aleg"
	if err := badALegID.Validate(maxBudget); err == nil {
		t.Error("aleg ID mismatch: want error, got nil")
	}
}

func TestServeHTTP_PlumbingPreservesCanonical(t *testing.T) {
	t.Parallel()

	var executedCall *aipapi.Call
	exec := &stubMockExec{
		onExecute: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			executedCall = call
			return nil, nil
		},
	}

	spec := &frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec: exec,
		},
		Wire: &mockWire{},
		MatchPath: func(p string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{
					ID: "call_canonical_1",
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("hello")},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return nil
		},
		// Profile is provided, but in 7.1 candidate gates are NOT yet wired into ServeHTTP,
		// so canonical execution must be completely unaffected and run normally.
		Profile: &testProfile{},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(`{"model":"gpt-4o","prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP with Profile set: want HTTP 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if executedCall == nil || executedCall.ID != "call_canonical_1" {
		t.Fatalf("ServeHTTP: expected canonical decode to execute call_canonical_1, got %v", executedCall)
	}
}

type stubMockExec struct {
	onExecute func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
}

func (m *stubMockExec) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if m.onExecute != nil {
		return m.onExecute(ctx, call)
	}
	return nil, nil
}

func (m *stubMockExec) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	return nil
}

func (m *stubMockExec) WallClock() func() time.Time {
	return nil
}

type mockWire struct{}

func (mockWire) WriteBodyTooLarge(w http.ResponseWriter) error { return nil }

func (mockWire) WriteReadBodyFailed(w http.ResponseWriter) error { return nil }

func (mockWire) WriteExecutorNotConfigured(w http.ResponseWriter) error { return nil }

func (mockWire) WritePreflightCanceled(w http.ResponseWriter) error { return nil }

func (mockWire) WriteInvalidJSON(w http.ResponseWriter) error { return nil }

func (mockWire) WriteAdmissionReject(w http.ResponseWriter, d decodeqos.Decision) error { return nil }

func (mockWire) WriteInvalidRequest(w http.ResponseWriter) error { return nil }

func (mockWire) WriteExecuteError(w http.ResponseWriter, out execerr.Outcome) error { return nil }

func (mockWire) WriteEncodeFailed(w http.ResponseWriter) error { return nil }
