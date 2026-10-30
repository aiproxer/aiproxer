package largebody_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// testFactBudget is deliberately tiny so bound enforcement stays cheap:
// any count or string length above it must be rejected.
const testFactBudget = 64

type stubSource struct{ size int64 }

func (s stubSource) Size() int64 { return s.size }
func (s stubSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("body")), nil
}
func (s stubSource) Close() error { return nil }

type stubStream struct{}

func (stubStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, io.EOF }
func (stubStream) Close() error                               { return nil }

func validProof() largebody.Proof {
	span, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		panic(err)
	}
	return largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "openai/responses",
		ClientModel:     "gpt-5",
		MaxOutputTokens: 1024,
		Facts:           largebody.ProtocolFacts{RequirementsID: "responses-create-v1", ControlCount: 2},
		Mode:            largebody.BodyModeIdentityJSON,
		Rewrite:         span,
		ModelSpan:       mustSpan(100, 12),
		Identity:        largebody.NewIdentityDigest(digestOf(1)),
		Turn: largebody.ClientTurnShape{
			Items: []largebody.ClientTurnItemShape{
				{
					Kind:    aipapi.ItemKindMessage,
					Role:    aipapi.RoleUser,
					Ordinal: 0,
					Parts: []largebody.ClientTurnPartShape{
						{Kind: aipapi.ContentPartText, ContentBytes: 1 << 20},
					},
				},
			},
			TotalContentBytes: 1 << 20,
		},
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "sess-1",
			ClientSessionID:        "client-1",
			ALegID:                 "a-leg-1",
		},
		Source:               largebody.NewSourceDigest(digestOf(2)),
		BodyBytes:            1 << 20,
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}
}

func mustSpan(offset, length int64) largebody.Span {
	span := largebody.Span{Offset: offset, Length: length}
	if err := span.Validate(); err != nil {
		panic(err)
	}
	return span
}

func digestOf(fill byte) [32]byte {
	var sum [32]byte
	for i := range sum {
		sum[i] = fill + byte(i)
	}
	return sum
}

func validStamp() largebody.AssessmentStamp {
	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-responses-v1",
		largebody.NewSourceDigest(digestOf(2)),
		1<<20,
		largebody.BodyModeIdentityJSON,
		largebody.NewNoRewrite(),
		largebody.NewIdentityDigest(digestOf(1)),
	)
	if err != nil {
		panic(err)
	}
	return stamp
}

func TestSource_ContractShape(t *testing.T) {
	t.Parallel()
	var src largebody.Source = stubSource{size: 8}
	if got := src.Size(); got != 8 {
		t.Fatalf("Size() = %d, want 8", got)
	}
	rc, err := src.Open()
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replay reader: %v", err)
	}
	if closeErr := rc.Close(); closeErr != nil {
		t.Fatalf("reader Close() error = %v", closeErr)
	}
	if closeErr := src.Close(); closeErr != nil {
		t.Fatalf("source Close() error = %v", closeErr)
	}
	if string(body) != "body" {
		t.Fatalf("replay bytes = %q, want %q", body, "body")
	}
}

func TestSpan_Validate(t *testing.T) {
	t.Parallel()
	if err := (largebody.Span{Offset: 0, Length: 8}).Validate(); err != nil {
		t.Fatalf("valid span rejected: %v", err)
	}
	for _, span := range []largebody.Span{{Offset: -1, Length: 8}, {Offset: 0, Length: -1}} {
		if err := span.Validate(); err == nil {
			t.Fatalf("span %+v must be rejected", span)
		}
	}
	overflow := largebody.Span{Offset: math.MaxInt64 - 4, Length: 8}
	if err := overflow.Validate(); err == nil {
		t.Fatal("overflowing span must be rejected")
	}
	if _, err := overflow.End(); err == nil {
		t.Fatal("overflowing span End() must fail")
	}
	end, err := (largebody.Span{Offset: 100, Length: 12}).End()
	if err != nil || end != 112 {
		t.Fatalf("End() = %d, %v; want 112, nil", end, err)
	}
}

func TestCheckedSpliceLength(t *testing.T) {
	t.Parallel()
	got, err := largebody.CheckedSpliceLength(100, 5, 200)
	if err != nil || got != 305 {
		t.Fatalf("CheckedSpliceLength = %d, %v; want 305, nil", got, err)
	}
	for _, lens := range [][3]int64{{-1, 5, 200}, {100, -1, 200}, {100, 5, -1}, {math.MaxInt64, 1, 0}, {math.MaxInt64 - 4, 4, 1}} {
		if _, err := largebody.CheckedSpliceLength(lens[0], lens[1], lens[2]); err == nil {
			t.Fatalf("CheckedSpliceLength(%v) must fail", lens)
		}
	}
}

func TestBodyMode_ValidateAndString(t *testing.T) {
	t.Parallel()
	if err := largebody.BodyModeIdentityJSON.Validate(); err != nil {
		t.Fatalf("identity body mode rejected: %v", err)
	}
	if err := largebody.BodyMode("").Validate(); err == nil {
		t.Fatal("unknown body mode must be rejected")
	}
	if err := largebody.BodyMode("provider-native").Validate(); err == nil {
		t.Fatal("unregistered body mode must be rejected")
	}
	if got := largebody.BodyModeIdentityJSON.String(); got == "" || strings.Contains(got, " ") {
		t.Fatalf("body mode label = %q, want bounded static label", got)
	}
}

func TestRewriteSemantics_Immutable(t *testing.T) {
	t.Parallel()
	none := largebody.NewNoRewrite()
	if none.NeedsModelRewrite() {
		t.Fatal("no-rewrite must not need a model rewrite")
	}
	if err := none.Validate(); err != nil {
		t.Fatalf("no-rewrite rejected: %v", err)
	}
	rewrite, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		t.Fatalf("model token rewrite rejected: %v", err)
	}
	if !rewrite.NeedsModelRewrite() {
		t.Fatal("model token rewrite must need a model rewrite")
	}
	if got := rewrite.Span(); got != mustSpan(100, 12) {
		t.Fatalf("Span() = %+v, want offset 100 length 12", got)
	}
	if _, err := largebody.NewModelTokenRewrite(largebody.Span{Offset: -1, Length: 4}); err == nil {
		t.Fatal("rewrite with invalid span must be rejected")
	}
	if _, err := largebody.NewModelTokenRewrite(largebody.Span{Offset: 4, Length: 0}); err == nil {
		t.Fatal("rewrite with empty span must be rejected")
	}
	typ := reflect.TypeFor[largebody.RewriteSemantics]()
	for field := range typ.Fields() {
		if field.PkgPath == "" {
			t.Fatalf("RewriteSemantics field %q is exported; semantics must be immutable", field.Name)
		}
	}
}

func TestSensitiveString_Redaction(t *testing.T) {
	t.Parallel()
	const secret = "aip-resume-token-abc123"
	s := largebody.NewSensitiveString(secret)
	if got := s.Reveal(); got != secret {
		t.Fatalf("Reveal() = %q, want secret", got)
	}
	renderings := map[string]string{
		"String":   s.String(),
		"GoString": s.GoString(),
		"SprintfV": fmt.Sprintf("%v", s),
		"SprintfS": fmt.Sprintf("%s", s),
		"SprintfQ": fmt.Sprintf("%q", s),
	}
	for name, rendered := range renderings {
		if strings.Contains(rendered, secret) {
			t.Fatalf("%s leaks the secret: %q", name, rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Fatalf("%s = %q, want explicit redaction marker", name, rendered)
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("MarshalJSON error = %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("MarshalJSON leaks the secret: %s", raw)
	}
	text, err := s.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error = %v", err)
	}
	if strings.Contains(string(text), secret) {
		t.Fatalf("MarshalText leaks the secret: %s", text)
	}
	if largebody.NewSensitiveString("").IsZero() != true {
		t.Fatal("empty sensitive string must report IsZero")
	}
	if s.IsZero() {
		t.Fatal("non-empty sensitive string must not report IsZero")
	}
}

func TestProof_Validate_Bounded(t *testing.T) {
	t.Parallel()
	if err := validProof().Validate(testFactBudget); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if err := validProof().Validate(0); err == nil {
		t.Fatal("non-positive fact budget must be rejected")
	}
	oversize := strings.Repeat("m", testFactBudget+1)
	cases := map[string]func(*largebody.Proof){
		"profile":  func(p *largebody.Proof) { p.ProfileID = oversize },
		"selector": func(p *largebody.Proof) { p.RouteSelector = oversize },
		"model":    func(p *largebody.Proof) { p.ClientModel = oversize },
	}
	for name, mutate := range cases {
		proof := validProof()
		mutate(&proof)
		if err := proof.Validate(testFactBudget); err == nil {
			t.Fatalf("oversize %s must be rejected", name)
		}
	}
	empty := validProof()
	empty.ProfileID = ""
	if err := empty.Validate(testFactBudget); err == nil {
		t.Fatal("empty profile ID must be rejected")
	}
	badDelivery := validProof()
	badDelivery.Delivery = "eventually"
	if err := badDelivery.Validate(testFactBudget); err == nil {
		t.Fatal("unknown delivery mode must be rejected")
	}
	zeroBody := validProof()
	zeroBody.BodyBytes = 0
	if err := zeroBody.Validate(testFactBudget); err == nil {
		t.Fatal("non-positive body size must be rejected")
	}
	rewriteMismatch := validProof()
	rewriteMismatch.Rewrite = largebody.NewNoRewrite()
	if err := rewriteMismatch.Validate(testFactBudget); err == nil {
		t.Fatal("proof needing a rewrite span without rewrite semantics must be rejected")
	}
}

func TestProof_AggregateFactBytes_IncludesResumeTokenAndIncompleteCompaction(t *testing.T) {
	t.Parallel()
	p := validProof()
	baseBytes := p.AggregateFactBytes()

	// 1. Adding resume token increments aggregate bytes
	p.Session.ResumeToken = largebody.NewSensitiveString("secret-resume-token-123")
	withTokenBytes := p.AggregateFactBytes()
	if withTokenBytes != baseBytes+int64(len("secret-resume-token-123")) {
		t.Fatalf("expected withTokenBytes=%d, got %d", baseBytes+int64(len("secret-resume-token-123")), withTokenBytes)
	}

	// 2. Compaction facts when CompactionComplete == false must be counted unconditionally
	p.CompactionComplete = false
	p.CompactionFacts.StartRuleID = "rule-abc"
	p.CompactionFacts.ItemHashes = [][32]byte{digestOf(10), digestOf(20)}
	withCompactionBytes := p.AggregateFactBytes()
	expectedCompactionBytes := int64(len("rule-abc") + 2*32)
	if withCompactionBytes != withTokenBytes+expectedCompactionBytes {
		t.Fatalf("incomplete compaction facts must be counted unconditionally: expected %d, got %d", withTokenBytes+expectedCompactionBytes, withCompactionBytes)
	}

	// 3. Required capabilities bytes must be counted
	p.RequiredCapabilities = append(p.RequiredCapabilities, aipapi.CapabilityTools)
	withCapsBytes := p.AggregateFactBytes()
	if withCapsBytes != withCompactionBytes+int64(len(aipapi.CapabilityTools)) {
		t.Fatalf("required capabilities bytes must be counted: expected %d, got %d", withCompactionBytes+int64(len(aipapi.CapabilityTools)), withCapsBytes)
	}

	// 4. Bounded classification evidence must be charged to the aggregate: the
	// accepted client identity length plus the fixed category-bitset width.
	//
	// The assertion is against an independently recomputed total rather than a
	// delta, because the carrier's fixed width is charged even when the evidence
	// is absent. A delta from a zero carrier would therefore only ever prove the
	// variable part; recomputing the whole cost model also fails if the carrier's
	// contribution is dropped from AggregateFactBytes altogether.
	p.ClassificationEvidence = sessionclassification.Evidence{Operation: p.Operation}
	const codingAgentUserAgent = "codex_cli_rs/1.2.3"
	p.ClassificationEvidence.ClientUserAgent = codingAgentUserAgent
	withEvidenceBytes := p.AggregateFactBytes()
	if want := expectedProofFactBytes(p); withEvidenceBytes != want {
		t.Fatalf("aggregate fact bytes must charge the bounded classification evidence: expected %d, got %d",
			want, withEvidenceBytes)
	}

	// The variable part must scale exactly with the accepted identity length.
	longerIdentity := p
	longerIdentity.ClassificationEvidence.ClientUserAgent = codingAgentUserAgent + "/next"
	if got, want := longerIdentity.AggregateFactBytes(), withEvidenceBytes+int64(len("/next")); got != want {
		t.Fatalf("aggregate fact bytes must scale with the client identity length: expected %d, got %d", want, got)
	}

	// 5. A proof whose evidence is absent is still charged the fixed carrier
	// width, so the carrier can never be free metadata.
	absentEvidence := p
	absentEvidence.ClassificationEvidence = sessionclassification.Evidence{}
	withoutIdentity := withEvidenceBytes - int64(len(codingAgentUserAgent))
	if got := absentEvidence.AggregateFactBytes(); got != withoutIdentity {
		t.Fatalf("absent evidence must still cost the fixed carrier width: expected %d, got %d", withoutIdentity, got)
	}

	// 6. A fully saturated category bitset must cost exactly the same as an
	// empty one. Category bits are presence bits, so neither tool count nor body
	// size can inflate proof metadata through classification
	// (requirements 5.2, 5.5).
	saturated := p
	saturated.ClassificationEvidence.ToolCategories = sessionclassification.DefinedToolCategoryBits
	if saturated.AggregateFactBytes() != withEvidenceBytes {
		t.Fatalf("a saturated category bitset must not change proof metadata cost: expected %d, got %d",
			withEvidenceBytes, saturated.AggregateFactBytes())
	}

	// 7. Accumulating every canonical tool name through the shared helper must
	// also leave the cost unchanged: the helper keeps presence bits, never names.
	accumulated := p
	for _, toolName := range []string{
		"read_file", "grep", "bash", "edit", "delete_file", "web_search", "not-a-canonical-tool",
	} {
		accumulated.ClassificationEvidence.ToolCategories = accumulated.ClassificationEvidence.ToolCategories.AddToolName(toolName)
	}
	if accumulated.AggregateFactBytes() != withEvidenceBytes {
		t.Fatalf("accumulated tool names must not change proof metadata cost: expected %d, got %d",
			withEvidenceBytes, accumulated.AggregateFactBytes())
	}
	if accumulated.ClassificationEvidence.ToolCategories != sessionclassification.DefinedToolCategoryBits {
		t.Fatalf("every canonical tool name must map into the defined category bits: got %d, want %d",
			accumulated.ClassificationEvidence.ToolCategories, sessionclassification.DefinedToolCategoryBits)
	}
}

// expectedProofFactBytes recomputes Proof.AggregateFactBytes from the documented
// accounting formula, independently of the production implementation, so that
// dropping or mis-sizing any accounted term fails the test rather than being
// absorbed by a delta.
func expectedProofFactBytes(p largebody.Proof) int64 {
	total := int64(len(p.ProfileID) + len(p.Operation) + len(p.RouteSelector) + len(p.ClientModel) + len(p.Facts.RequirementsID))
	total += p.Turn.MetadataBytes()
	total += int64(len(p.Session.AuthoritativeSessionID) + len(p.Session.ClientSessionID) + len(p.Session.ALegID) + p.Session.ResumeToken.ByteLen())
	total += int64(len(p.CompactionFacts.ItemHashes)*32 + len(p.CompactionFacts.StartRuleID)) // 32 == compactionfacts.ItemHashSizeBytes
	for _, capability := range p.RequiredCapabilities {
		total += int64(len(capability))
	}
	// Bounded classification evidence: accepted client identity length plus the
	// fixed category-bitset width. Operation is deliberately not re-charged; the
	// proof already charges it above and the string backing is shared.
	total += int64(len(p.ClassificationEvidence.ClientUserAgent)) + sessionclassification.ToolCategorySetBytes
	return total
}

// TestProof_Validate_EnforcesBoundedClassificationEvidence proves the carrier is
// validated as part of the proof rather than merely stored: matching evidence is
// accepted, evidence describing a different operation is refused, and neither an
// over-budget identity nor an undefined category bit may pass (requirements 5.2,
// 5.4, 5.5).
func TestProof_Validate_EnforcesBoundedClassificationEvidence(t *testing.T) {
	t.Parallel()

	// Absent evidence stays valid: insufficient evidence must leave the session
	// unknown rather than force canonical materialization (requirement 5.4).
	absent := validProof()
	if err := absent.Validate(testFactBudget); err != nil {
		t.Fatalf("proof without classification evidence must stay valid: %v", err)
	}

	populated := validProof()
	populated.ClassificationEvidence = sessionclassification.Evidence{
		Operation:       populated.Operation,
		ClientUserAgent: "codex_cli_rs/1.2.3",
		ToolCategories: sessionclassification.ToolCategorySet(0).
			AddToolName("read_file").AddToolName("edit_file").AddToolName("bash"),
	}
	if err := populated.Validate(testFactBudget); err != nil {
		t.Fatalf("proof with matching bounded classification evidence must be accepted: %v", err)
	}

	mismatched := validProof()
	mismatched.ClassificationEvidence = sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIChatCompletions,
		ClientUserAgent: "codex_cli_rs/1.2.3",
	}
	err := mismatched.Validate(testFactBudget)
	if err == nil {
		t.Fatal("evidence describing a different operation must be rejected")
	}
	if !strings.Contains(err.Error(), "classification evidence operation") {
		t.Fatalf("expected an operation-mismatch error, got %v", err)
	}

	overBudget := validProof()
	overBudget.ClassificationEvidence = sessionclassification.Evidence{Operation: overBudget.Operation}
	overBudget.ClassificationEvidence.ClientUserAgent = strings.Repeat("a", testFactBudget+1)
	err = overBudget.Validate(testFactBudget)
	if err == nil {
		t.Fatal("an over-budget client identity must be rejected at the proof level")
	}
	if !strings.Contains(err.Error(), "classification evidence") {
		t.Fatalf("expected a classification-evidence bound error, got %v", err)
	}

	undefinedBits := validProof()
	undefinedBits.ClassificationEvidence = sessionclassification.Evidence{Operation: undefinedBits.Operation}
	undefinedBits.ClassificationEvidence.ToolCategories |= 1 << 15
	err = undefinedBits.Validate(testFactBudget)
	if err == nil {
		t.Fatal("tool category bits outside the defined set must be rejected")
	}
	if !strings.Contains(err.Error(), "undefined bits") {
		t.Fatalf("expected an undefined category-bit error, got %v", err)
	}
}

// TestWireTurnFacts_Validate_RejectsClassificationEvidenceForAnotherOperation
// proves the wire facts refuse evidence describing an operation other than the
// one being executed, so a committed wire turn can never be classified by
// another request's evidence (requirements 5.3, 5.5).
func TestWireTurnFacts_Validate_RejectsClassificationEvidenceForAnotherOperation(t *testing.T) {
	t.Parallel()

	facts := largebody.DefaultTestWireTurnFacts()
	if err := facts.Validate(testBudget); err != nil {
		t.Fatalf("reference wire facts must be valid: %v", err)
	}

	mismatched := facts
	mismatched.Session.ClassificationEvidence.Operation = aipapi.OperationOpenAIChatCompletions
	err := mismatched.Validate(testBudget)
	if err == nil {
		t.Fatal("wire evidence for another operation must be rejected")
	}
	if !strings.Contains(err.Error(), "classification evidence operation") {
		t.Fatalf("expected an operation-mismatch error, got %v", err)
	}

	// Matching evidence, an over-budget identity and an undefined bit are all
	// rejected by the session domain that owns the carrier.
	overBudget := facts
	overBudget.Session.ClassificationEvidence.ClientUserAgent = strings.Repeat("a", int(testBudget)+1)
	if err := overBudget.Validate(testBudget); err == nil {
		t.Fatal("wire facts must reject an over-budget client identity")
	}

	undefinedBits := facts
	undefinedBits.Session.ClassificationEvidence.ToolCategories |= 1 << 15
	if err := undefinedBits.Validate(testBudget); err == nil {
		t.Fatal("wire facts must reject undefined tool category bits")
	}
}

// TestNewWireTurnFactsFromProof_CarriesPopulatedClassificationEvidence proves the
// proof carrier reaches the wire facts unchanged, so the canonical and wire paths
// observe the same bounded evidence and therefore classify identically
// (requirements 5.3, 5.5, 12.7).
func TestNewWireTurnFactsFromProof_CarriesPopulatedClassificationEvidence(t *testing.T) {
	t.Parallel()

	proof := validProof()
	proof.ClassificationEvidence = sessionclassification.Evidence{
		Operation:       proof.Operation,
		ClientUserAgent: "roo-code/3.2.1",
		ToolCategories: sessionclassification.ToolCategorySet(0).
			AddToolName("read_file").AddToolName("edit_file").AddToolName("bash"),
	}
	if err := proof.Validate(testFactBudget); err != nil {
		t.Fatalf("proof with populated classification evidence must be valid: %v", err)
	}

	stamp, err := largebody.BindAssessmentStamp("gen-session-classification-propagation", proof)
	if err != nil {
		t.Fatalf("BindAssessmentStamp: %v", err)
	}
	facts, err := largebody.NewWireTurnFactsFromProof(proof, stamp, "req-1", "trace-1", "bill-1")
	if err != nil {
		t.Fatalf("NewWireTurnFactsFromProof: %v", err)
	}

	if facts.Session.ClassificationEvidence != proof.ClassificationEvidence {
		t.Fatalf("wire facts must carry the proof carrier unchanged: got %+v, want %+v",
			facts.Session.ClassificationEvidence, proof.ClassificationEvidence)
	}
	if facts.Session.ClassificationEvidence.IsZero() {
		t.Fatal("propagated classification evidence must not be empty")
	}
	if err := facts.Validate(testBudget); err != nil {
		t.Fatalf("derived wire facts with propagated evidence must be valid: %v", err)
	}
	if err := facts.AssertNoShadowCall(); err != nil {
		t.Fatalf("propagated evidence must keep the wire facts Call-free: %v", err)
	}
}

func TestProof_Validate_StreamingRequiresCapabilityStreaming(t *testing.T) {
	t.Parallel()
	p := validProof()
	p.Delivery = aipapi.DeliveryModeStreaming
	p.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityTools} // missing streaming
	err := p.Validate(testFactBudget)
	if err == nil {
		t.Fatal("expected error when streaming delivery lacks CapabilityStreaming in RequiredCapabilities")
	}
	if !strings.Contains(err.Error(), "requires CapabilityStreaming") {
		t.Fatalf("expected error message to mention CapabilityStreaming, got %v", err)
	}
}

func TestClientTurnShape_BoundedNoPromptText(t *testing.T) {
	t.Parallel()
	if err := validProof().Turn.Validate(testFactBudget); err != nil {
		t.Fatalf("valid turn shape rejected: %v", err)
	}
	badRole := validProof().Turn
	badRole.Items[0].Role = "narrator"
	if err := badRole.Validate(testFactBudget); err == nil {
		t.Fatal("unknown role must be rejected")
	}
	badKind := validProof().Turn
	badKind.Items[0].Parts[0].Kind = "prompt_text"
	if err := badKind.Validate(testFactBudget); err == nil {
		t.Fatal("unknown part kind must be rejected")
	}
	negative := validProof().Turn
	negative.Items[0].Parts[0].ContentBytes = -1
	if err := negative.Validate(testFactBudget); err == nil {
		t.Fatal("negative content bytes must be rejected")
	}
	many := validProof().Turn
	many.Items = make([]largebody.ClientTurnItemShape, testFactBudget+1)
	if err := many.Validate(testFactBudget); err == nil {
		t.Fatal("item count above budget must be rejected")
	}
}

func TestDigests_DistinctNamespaces(t *testing.T) {
	t.Parallel()
	identity := largebody.NewIdentityDigest(digestOf(1))
	source := largebody.NewSourceDigest(digestOf(1))
	if identity.IsZero() || source.IsZero() {
		t.Fatal("non-zero digests must not report IsZero")
	}
	if reflect.TypeFor[largebody.IdentityDigest]() == reflect.TypeFor[largebody.SourceDigest]() {
		t.Fatal("identity and source digests must be distinct types")
	}
	if identity.String() == "" || source.String() == "" {
		t.Fatal("digest labels must render without retaining content")
	}
	if identity.String() != source.String() {
		t.Fatal("same bytes should render the same hex in either namespace; namespaces differ by type, not rendering")
	}
	if largebody.NewIdentityDigest([32]byte{}).IsZero() != true {
		t.Fatal("zero identity digest must report IsZero")
	}
}

func TestAssessment_Lifecycle(t *testing.T) {
	t.Parallel()
	req := largebody.AssessmentRequest{Proof: validProof(), GenerationID: "gen-1"}
	if err := req.Validate(testFactBudget); err != nil {
		t.Fatalf("valid assessment request rejected: %v", err)
	}
	emptyGen := req
	emptyGen.GenerationID = ""
	if err := emptyGen.Validate(testFactBudget); err == nil {
		t.Fatal("empty generation binding must be rejected")
	}
	declined := largebody.Assessment{Decision: largebody.AssessmentDecisionDecline, Reason: largebody.DeclineReasonAuthorityBlocker}
	if err := declined.Validate(testFactBudget); err != nil {
		t.Fatalf("valid decline rejected: %v", err)
	}
	declineNoReason := largebody.Assessment{Decision: largebody.AssessmentDecisionDecline}
	if err := declineNoReason.Validate(testFactBudget); err == nil {
		t.Fatal("decline without a bounded reason must be rejected")
	}
	accepted := largebody.Assessment{Decision: largebody.AssessmentDecisionAccept, Reason: largebody.DeclineReasonNone, Stamp: validStamp()}
	if err := accepted.Validate(testFactBudget); err != nil {
		t.Fatalf("valid accept rejected: %v", err)
	}
	acceptNoStamp := largebody.Assessment{Decision: largebody.AssessmentDecisionAccept, Reason: largebody.DeclineReasonNone}
	if err := acceptNoStamp.Validate(testFactBudget); err == nil {
		t.Fatal("accept without a stamp must be rejected")
	}
	unknown := largebody.Assessment{}
	if err := unknown.Validate(testFactBudget); err == nil {
		t.Fatal("unknown decision must be rejected")
	}
	if got := largebody.AssessmentDecisionAccept.String(); got != "accept" {
		t.Fatalf("accept label = %q", got)
	}
	if got := largebody.DeclineReasonAuthorityBlocker.String(); got != "authority_blocker" {
		t.Fatalf("decline label = %q", got)
	}
}

func TestAssessmentStamp_BindsFacts(t *testing.T) {
	t.Parallel()
	stamp := validStamp()
	if stamp.GenerationID() != "gen-1" || stamp.ProfileID() != "openai-responses-v1" {
		t.Fatalf("stamp binds %+v, want gen-1/openai-responses-v1", stamp)
	}
	if stamp.BodyBytes() != 1<<20 || stamp.BodyMode() != largebody.BodyModeIdentityJSON {
		t.Fatalf("stamp binds wrong body contract: %+v", stamp)
	}
	if _, err := largebody.NewAssessmentStamp("", "p", largebody.NewSourceDigest(digestOf(2)), 8, largebody.BodyModeIdentityJSON, largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp without generation binding must be rejected")
	}
	if _, err := largebody.NewAssessmentStamp("g", "p", largebody.NewSourceDigest(digestOf(2)), 0, largebody.BodyModeIdentityJSON, largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp with non-positive body size must be rejected")
	}
	if _, err := largebody.NewAssessmentStamp("g", "p", largebody.NewSourceDigest(digestOf(2)), 8, largebody.BodyMode(""), largebody.NewNoRewrite(), largebody.NewIdentityDigest(digestOf(1))); err == nil {
		t.Fatal("stamp with unknown body mode must be rejected")
	}
	typ := reflect.TypeFor[largebody.AssessmentStamp]()
	for field := range typ.Fields() {
		if field.PkgPath == "" {
			t.Fatalf("AssessmentStamp field %q is exported; the stamp must stay opaque", field.Name)
		}
	}
}

func TestWireFacts_Bounded(t *testing.T) {
	t.Parallel()
	req := largebody.WireRequestFacts{
		ProfileID:      "openai-responses-v1",
		Operation:      aipapi.OperationOpenAIResponses,
		Delivery:       aipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		Rewrite:        largebody.NewNoRewrite(),
		ClientModel:    "gpt-5",
		CandidateModel: "gpt-5",
	}
	if err := req.Validate(testFactBudget); err != nil {
		t.Fatalf("valid wire request facts rejected: %v", err)
	}
	badOp := req
	badOp.Operation = ""
	if err := badOp.Validate(testFactBudget); err == nil {
		t.Fatal("empty operation must be rejected")
	}
	oversize := req
	oversize.CandidateModel = strings.Repeat("m", testFactBudget+1)
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize candidate model must be rejected")
	}
	domain := largebody.WireDomainFacts{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		CandidateModels: []string{"gpt-5"},
	}
	if err := domain.Validate(testFactBudget); err != nil {
		t.Fatalf("valid finite domain rejected: %v", err)
	}
	universal := largebody.WireDomainFacts{
		ProfileID:      "openai-responses-v1",
		Operation:      aipapi.OperationOpenAIResponses,
		Delivery:       aipapi.DeliveryModeStreaming,
		BodyMode:       largebody.BodyModeIdentityJSON,
		UniversalModel: true,
	}
	if err := universal.Validate(testFactBudget); err != nil {
		t.Fatalf("valid universal domain rejected: %v", err)
	}
	ambiguous := universal
	ambiguous.CandidateModels = []string{"gpt-5"}
	if err := ambiguous.Validate(testFactBudget); err == nil {
		t.Fatal("universal domain with enumerated models must be rejected")
	}
	empty := universal
	empty.UniversalModel = false
	if err := empty.Validate(testFactBudget); err == nil {
		t.Fatal("empty finite domain must be rejected")
	}
}

func TestRewritePlan_CheckedMath(t *testing.T) {
	t.Parallel()
	plan := largebody.RewritePlan{
		Rewrite:          mustRewrite(),
		ReplacementModel: "gpt-5-mini",
		RewrittenLength:  1<<20 + 4,
	}
	if err := plan.Validate(testFactBudget); err != nil {
		t.Fatalf("valid rewrite plan rejected: %v", err)
	}
	noRewrite := plan
	noRewrite.Rewrite = largebody.NewNoRewrite()
	if err := noRewrite.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without certified rewrite semantics must be rejected")
	}
	emptyModel := plan
	emptyModel.ReplacementModel = ""
	if err := emptyModel.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without replacement model must be rejected")
	}
	badLength := plan
	badLength.RewrittenLength = 0
	if err := badLength.Validate(testFactBudget); err == nil {
		t.Fatal("rewrite plan without rewritten length must be rejected")
	}
}

func mustRewrite() largebody.RewriteSemantics {
	rewrite, err := largebody.NewModelTokenRewrite(mustSpan(100, 12))
	if err != nil {
		panic(err)
	}
	return rewrite
}

func TestExecutionResult_Bounded(t *testing.T) {
	t.Parallel()
	result := largebody.ExecutionResult{
		Stream: stubStream{},
		Facts: largebody.ResponseFacts{
			RequestID:       "req-1",
			TraceID:         "trace-1",
			ALegID:          "a-leg-1",
			SessionID:       "sess-1",
			Operation:       aipapi.OperationOpenAIResponses,
			Delivery:        aipapi.DeliveryModeStreaming,
			EffectiveModel:  "gpt-5",
			Source:          largebody.NewSourceDigest(digestOf(2)),
			BodyBytes:       1 << 20,
			RewrittenLength: 0,
		},
		Session: largebody.SessionResponseCarrier{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "a-leg-1",
			ResumeToken:            largebody.NewSensitiveString("aip-resume-1"),
		},
	}
	if err := result.Validate(testFactBudget); err != nil {
		t.Fatalf("valid execution result rejected: %v", err)
	}
	nilStream := result
	nilStream.Stream = nil
	if err := nilStream.Validate(testFactBudget); err == nil {
		t.Fatal("execution result without a stream must be rejected")
	}
	oversize := result
	facts := oversize.Facts
	facts.EffectiveModel = strings.Repeat("m", testFactBudget+1)
	oversize.Facts = facts
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize effective model must be rejected")
	}
	missingRequest := result
	facts = missingRequest.Facts
	facts.RequestID = ""
	missingRequest.Facts = facts
	if err := missingRequest.Validate(testFactBudget); err == nil {
		t.Fatal("response facts without request identity must be rejected")
	}
}

func TestSessionCarrier_NeverLogsSecrets(t *testing.T) {
	t.Parallel()
	const token = "aip-resume-token-secret"
	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess-9",
		ALegID:                 "a-leg-9",
		ResumeToken:            largebody.NewSensitiveString(token),
	}
	for name, rendered := range map[string]string{
		"SprintfV": fmt.Sprintf("%v", carrier),
		"SprintfS": fmt.Sprintf("%s", carrier),
		"SprintfQ": fmt.Sprintf("%q", carrier),
		"GoString": carrier.GoString(),
		"String":   carrier.String(),
	} {
		for _, secret := range []string{token, "sess-9", "a-leg-9"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s leaks carrier value %q: %q", name, secret, rendered)
			}
		}
	}
	raw, err := json.Marshal(carrier)
	if err != nil {
		t.Fatalf("carrier MarshalJSON error = %v", err)
	}
	for _, secret := range []string{token, "sess-9", "a-leg-9"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("carrier JSON leaks %q: %s", secret, raw)
		}
	}
	if err := carrier.Validate(testFactBudget); err != nil {
		t.Fatalf("valid carrier rejected: %v", err)
	}
}

func TestSessionInput_MarshalRedactsResumeToken(t *testing.T) {
	t.Parallel()
	const token = "aip-resume-token-secret"
	input := largebody.SessionInput{
		AuthoritativeSessionID: "sess-1",
		ResumeToken:            largebody.NewSensitiveString(token),
		NewSessionRequested:    true,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("session input MarshalJSON error = %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("session input JSON leaks resume token: %s", raw)
	}
	if err := input.Validate(testFactBudget); err != nil {
		t.Fatalf("valid session input rejected: %v", err)
	}
	oversize := input
	oversize.AuthoritativeSessionID = strings.Repeat("s", testFactBudget+1)
	if err := oversize.Validate(testFactBudget); err == nil {
		t.Fatal("oversize session ID must be rejected")
	}
}

func TestPackage_HasNoSmugglingVectors(t *testing.T) {
	t.Parallel()
	structs := []reflect.Type{
		reflect.TypeFor[largebody.Span](),
		reflect.TypeFor[largebody.RewriteSemantics](),
		reflect.TypeFor[largebody.SensitiveString](),
		reflect.TypeFor[largebody.ProtocolFacts](),
		reflect.TypeFor[largebody.SessionInput](),
		reflect.TypeFor[largebody.ClientTurnPartShape](),
		reflect.TypeFor[largebody.ClientTurnItemShape](),
		reflect.TypeFor[largebody.ClientTurnShape](),
		reflect.TypeFor[largebody.IdentityDigest](),
		reflect.TypeFor[largebody.SourceDigest](),
		reflect.TypeFor[largebody.Proof](),
		reflect.TypeFor[largebody.AssessmentRequest](),
		reflect.TypeFor[largebody.Assessment](),
		reflect.TypeFor[largebody.AssessmentStamp](),
		reflect.TypeFor[largebody.WireRequestFacts](),
		reflect.TypeFor[largebody.WireDomainFacts](),
		reflect.TypeFor[largebody.RewritePlan](),
		reflect.TypeFor[largebody.ExecutionResult](),
		reflect.TypeFor[largebody.ResponseFacts](),
		reflect.TypeFor[largebody.SessionResponseCarrier](),
	}
	allowedContentNames := map[string]bool{
		"ContentBytes": true, "TotalContentBytes": true, "ControlCount": true,
		"BodyBytes": true, "BodyMode": true, "bodyBytes": true,
	}
	for _, typ := range structs {
		if strings.Contains(typ.Name(), "Call") {
			t.Fatalf("type %s mirrors aipapi.Call; DTOs must stay narrowly scoped", typ.Name())
		}
		for field := range typ.Fields() {
			if field.PkgPath == "" && field.Type.Kind() == reflect.Map {
				t.Fatalf("type %s field %s is an unbounded map", typ.Name(), field.Name)
			}
			lower := strings.ToLower(field.Name)
			switch {
			// Note: "file" is intentionally absent from this denylist because
			// it false-positives on ProfileID; temp-file smuggling is covered
			// by temp/path/dir plus the no-map rule and the import ban below.
			case strings.Contains(lower, "header"),
				strings.Contains(lower, "prompt"),
				strings.Contains(lower, "temp"),
				strings.Contains(lower, "path"),
				strings.Contains(lower, "dir"):
				t.Fatalf("type %s field %s smuggles forbidden state", typ.Name(), field.Name)
			case strings.Contains(lower, "text") ||
				strings.Contains(lower, "content") ||
				strings.Contains(lower, "body"):
				if !allowedContentNames[field.Name] {
					t.Fatalf("type %s field %s smuggles payload content", typ.Name(), field.Name)
				}
			}
		}
	}
}
