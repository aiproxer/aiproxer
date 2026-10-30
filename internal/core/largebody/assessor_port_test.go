package largebody_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// stubAssessorExecutor implements LargeBodyExecutor with the new Task 11.1 signatures.
type stubAssessorExecutor struct {
	canonicalOnlyExecutor
	assessCalls  int
	executeCalls int
	lastProof    largebody.Proof
	lastAccepted largebody.Assessment
	assessResult largebody.Assessment
	assessErr    error
	execResult   largebody.ExecutionResult
	execErr      error
}

func (s *stubAssessorExecutor) AssessLargeBody(_ context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	s.assessCalls++
	s.lastProof = proof
	return s.assessResult, s.assessErr
}

func (s *stubAssessorExecutor) ExecuteLargeBody(_ context.Context, accepted largebody.Assessment, _ largebody.Source) (largebody.ExecutionResult, error) {
	s.executeCalls++
	s.lastAccepted = accepted
	return s.execResult, s.execErr
}

var (
	_ largebody.LargeBodyAssessor     = (*stubAssessorExecutor)(nil)
	_ largebody.LargeBodyWireExecutor = (*stubAssessorExecutor)(nil)
	_ largebody.LargeBodyExecutor     = (*stubAssessorExecutor)(nil)
)

func TestTask11_1_AssessorPort_MethodSignature(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[largebody.LargeBodyAssessor]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("LargeBodyAssessor must be an interface, got %s", typ.Kind())
	}
	method, ok := typ.MethodByName("AssessLargeBody")
	if !ok {
		t.Fatal("LargeBodyAssessor missing AssessLargeBody method")
	}
	// AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	if method.Type.NumIn() != 2 {
		t.Fatalf("AssessLargeBody must take 2 arguments (ctx, proof), got %d", method.Type.NumIn())
	}
	in1 := method.Type.In(1)
	if in1 != reflect.TypeFor[largebody.Proof]() {
		t.Fatalf("AssessLargeBody second arg must be largebody.Proof, got %s", in1)
	}
	if method.Type.NumOut() != 2 {
		t.Fatalf("AssessLargeBody must return 2 values (Assessment, error), got %d", method.Type.NumOut())
	}
	out0 := method.Type.Out(0)
	if out0 != reflect.TypeFor[largebody.Assessment]() {
		t.Fatalf("AssessLargeBody first return value must be largebody.Assessment, got %s", out0)
	}
}

func TestTask11_1_WireExecutorPort_MethodSignature(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[largebody.LargeBodyWireExecutor]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("LargeBodyWireExecutor must be an interface, got %s", typ.Kind())
	}
	method, ok := typ.MethodByName("ExecuteLargeBody")
	if !ok {
		t.Fatal("LargeBodyWireExecutor missing ExecuteLargeBody method")
	}
	// ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
	if method.Type.NumIn() != 3 {
		t.Fatalf("ExecuteLargeBody must take 3 arguments (ctx, accepted, src), got %d", method.Type.NumIn())
	}
	in1 := method.Type.In(1)
	if in1 != reflect.TypeFor[largebody.Assessment]() {
		t.Fatalf("ExecuteLargeBody second arg must be largebody.Assessment, got %s", in1)
	}
	in2 := method.Type.In(2)
	if in2 != reflect.TypeFor[largebody.Source]() {
		t.Fatalf("ExecuteLargeBody third arg must be largebody.Source, got %s", in2)
	}
	if method.Type.NumOut() != 2 {
		t.Fatalf("ExecuteLargeBody must return 2 values (ExecutionResult, error), got %d", method.Type.NumOut())
	}
	out0 := method.Type.Out(0)
	if out0 != reflect.TypeFor[largebody.ExecutionResult]() {
		t.Fatalf("ExecuteLargeBody first return value must be largebody.ExecutionResult, got %s", out0)
	}
}

func TestTask11_1_LargeBodyExecutor_EmbedsPorts(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[largebody.LargeBodyExecutor]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("LargeBodyExecutor must be an interface, got %s", typ.Kind())
	}
	assessorTyp := reflect.TypeFor[largebody.LargeBodyAssessor]()
	if !typ.Implements(assessorTyp) {
		t.Fatal("LargeBodyExecutor must implement LargeBodyAssessor")
	}
	wireExecTyp := reflect.TypeFor[largebody.LargeBodyWireExecutor]()
	if !typ.Implements(wireExecTyp) {
		t.Fatal("LargeBodyExecutor must implement LargeBodyWireExecutor")
	}
}

func TestTask11_1_AsLargeBodyAssessor_Probing(t *testing.T) {
	t.Parallel()
	if got, ok := largebody.AsLargeBodyAssessor(nil); ok || got != nil {
		t.Fatalf("AsLargeBodyAssessor(nil) = (%v, %v), want (nil, false)", got, ok)
	}
	canonical := &canonicalOnlyExecutor{}
	if got, ok := largebody.AsLargeBodyAssessor(canonical); ok || got != nil {
		t.Fatalf("AsLargeBodyAssessor(canonical) = (%v, %v), want (nil, false)", got, ok)
	}
	stub := &stubAssessorExecutor{}
	got, ok := largebody.AsLargeBodyAssessor(stub)
	if !ok || got == nil {
		t.Fatalf("AsLargeBodyAssessor(stub) = (%v, %v), want (non-nil, true)", got, ok)
	}
}

func TestTask11_1_Assessment_BoundedFactsOnly(t *testing.T) {
	t.Parallel()
	// Requirements 6, 22: Assessment contains opaque stamp and bounded facts only.
	// Frontend cannot synthesize route/backend internals.
	typ := reflect.TypeFor[largebody.Assessment]()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("Assessment must be a struct, got %s", typ.Kind())
	}

	allowedFields := map[string]bool{
		"Decision":           true,
		"Reason":             true,
		"Stamp":              true,
		"WireRequest":        true,
		"WireDomain":         true,
		"CompactionFacts":    true,
		"CompactionComplete": true,
	}

	for field := range typ.Fields() {
		if !allowedFields[field.Name] {
			t.Fatalf("Assessment contains unexpected field %q; must contain opaque stamp and bounded facts only", field.Name)
		}
	}

	// Verify Stamp opacity: AssessmentStamp fields must remain unexported.
	stampTyp := reflect.TypeFor[largebody.AssessmentStamp]()
	for field := range stampTyp.Fields() {
		if field.PkgPath == "" {
			t.Fatalf("AssessmentStamp field %q is exported; stamp must stay opaque (Requirement 6.7)", field.Name)
		}
	}
}

func TestTask11_1_Assessment_ConstructorsAndValidation(t *testing.T) {
	t.Parallel()
	// Declined assessment constructor
	declined, err := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
	if err != nil {
		t.Fatalf("NewDeclinedAssessment error: %v", err)
	}
	if !declined.Declined() {
		t.Fatal("declined assessment must report Declined() == true")
	}
	if declined.Accepted() {
		t.Fatal("declined assessment must report Accepted() == false")
	}
	if declined.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("declined.Reason = %v, want %v", declined.Reason, largebody.DeclineReasonAuthorityBlocker)
	}
	if err := declined.Validate(1024); err != nil {
		t.Fatalf("declined.Validate error: %v", err)
	}

	// Invalid decline: DeclineReasonNone
	if _, err := largebody.NewDeclinedAssessment(largebody.DeclineReasonNone); err == nil {
		t.Fatal("NewDeclinedAssessment(DeclineReasonNone) must fail")
	}

	// Valid stamp for accepted assessment
	stamp, err := largebody.NewAssessmentStamp(
		"gen-42",
		"openai-chat",
		largebody.NewSourceDigest(digestOf(1)),
		512,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(digestOf(2)),
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-4o",
		CandidateModel:  "gpt-4o-mini",
		MaxOutputTokens: 4096,
	}

	wireDomain := largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		CandidateModels: []string{"gpt-4o-mini"},
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}
	if !accepted.Accepted() {
		t.Fatal("accepted assessment must report Accepted() == true")
	}
	if accepted.Declined() {
		t.Fatal("accepted assessment must report Declined() == false")
	}
	if accepted.Reason != largebody.DeclineReasonNone {
		t.Fatalf("accepted.Reason = %v, want DeclineReasonNone", accepted.Reason)
	}
	if err := accepted.Validate(1024); err != nil {
		t.Fatalf("accepted.Validate error: %v", err)
	}

	// Zero stamp cannot create accepted assessment
	if _, err := largebody.NewAcceptedAssessment(largebody.AssessmentStamp{}, wireReq, wireDomain); err == nil {
		t.Fatal("NewAcceptedAssessment with zero stamp must fail")
	}
}

func TestTask11_1_FrontendSuppliesProofOnly_NoInternals(t *testing.T) {
	t.Parallel()
	// Verify Proof structure: contains only client/frontend-derived facts.
	// No generation ID, no route candidates, no backend credentials/internals.
	proofTyp := reflect.TypeFor[largebody.Proof]()
	forbiddenInProof := []string{"GenerationID", "RouteDomain", "Candidates", "Backends", "Credentials"}
	for _, forbidden := range forbiddenInProof {
		if _, ok := proofTyp.FieldByName(forbidden); ok {
			t.Fatalf("Proof must not contain %q; frontend supplies proof only, never route/backend internals", forbidden)
		}
	}

	// Invoke AssessLargeBody with proof only via interface
	exec := &stubAssessorExecutor{
		assessResult: largebody.Assessment{
			Decision: largebody.AssessmentDecisionDecline,
			Reason:   largebody.DeclineReasonProofUncertain,
		},
	}
	var assessor largebody.LargeBodyAssessor = exec

	proof := validProof()
	assessment, err := assessor.AssessLargeBody(context.Background(), proof)
	if err != nil {
		t.Fatalf("AssessLargeBody error: %v", err)
	}
	if exec.assessCalls != 1 {
		t.Fatalf("assessCalls = %d, want 1", exec.assessCalls)
	}
	if exec.lastProof.ProfileID != proof.ProfileID {
		t.Fatalf("lastProof.ProfileID = %q, want %q", exec.lastProof.ProfileID, proof.ProfileID)
	}
	if !assessment.Declined() {
		t.Fatal("expected declined assessment")
	}
}
