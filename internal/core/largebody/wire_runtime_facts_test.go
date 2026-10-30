package largebody_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const testBudget = largebody.DefaultMaxSemanticFactBytes

func TestTask12_1_WireRuntimeFacts_EveryFieldHasNamedConsumer(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()
	consumers := facts.AuditNamedConsumers()

	if len(consumers) == 0 {
		t.Fatal("AuditNamedConsumers must return non-empty consumer mappings")
	}

	// Required 8 domains from Task 12.1 specification:
	requiredDomains := []string{
		"route",
		"protocol",
		"max_output",
		"identity",
		"session",
		"source",
		"rewrite",
		"economic",
	}

	for _, domain := range requiredDomains {
		found := false
		for key, consumer := range consumers {
			if strings.HasPrefix(key, domain+".") || key == domain {
				if strings.TrimSpace(consumer) == "" {
					t.Fatalf("domain fact %q has empty named consumer", key)
				}
				found = true
			}
		}
		if !found {
			t.Errorf("required domain %q has no entries in AuditNamedConsumers", domain)
		}
	}
}

func TestTask12_1_WireRuntimeFacts_NoShadowCallSchema(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()
	if err := facts.AssertNoShadowCall(); err != nil {
		t.Fatalf("AssertNoShadowCall failed: %v", err)
	}
}

func TestTask12_1_WireRuntimeFacts_ConsumerCoverageIsMechanical(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()
	if err := facts.AssertConsumerCoverage(); err != nil {
		t.Fatalf("AssertConsumerCoverage failed: %v", err)
	}
}

func TestTask12_1_WireRuntimeFacts_EightDomainsCoverage(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()
	if err := facts.Validate(testBudget); err != nil {
		t.Fatalf("valid wire turn facts rejected: %v", err)
	}

	// Verify the 8 domains are populated
	if facts.Route.RouteSelector == "" {
		t.Error("Route.RouteSelector is empty")
	}
	if facts.Protocol.Operation == "" {
		t.Error("Protocol.Operation is empty")
	}
	if facts.MaxOutput.MaxOutputTokens <= 0 {
		t.Error("MaxOutput.MaxOutputTokens must be positive")
	}
	if facts.Identity.RequestID == "" {
		t.Error("Identity.RequestID is empty")
	}
	if facts.Session.Input.AuthoritativeSessionID == "" {
		t.Error("Session.Input.AuthoritativeSessionID is empty")
	}
	if facts.Source.SourceDigest.IsZero() {
		t.Error("Source.SourceDigest is zero")
	}
	if facts.Rewrite.Semantics.Kind() == largebody.RewriteKindNone && facts.Rewrite.ReplacementModel != "" {
		t.Error("inconsistent Rewrite semantics")
	}
	if facts.Economic.BillingCallID == "" {
		t.Error("Economic.BillingCallID is empty")
	}
}

func TestTask12_1_WireRuntimeFacts_ValidationBudgetBounds(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()

	// 1. Oversized RouteSelector
	badRoute := facts
	badRoute.Route.RouteSelector = strings.Repeat("x", int(testBudget)+1)
	if err := badRoute.Validate(testBudget); err == nil {
		t.Error("oversized route selector must be rejected")
	}

	// 2. Negative MaxOutputTokens
	badOutput := facts
	badOutput.MaxOutput.MaxOutputTokens = -1
	if err := badOutput.Validate(testBudget); err == nil {
		t.Error("negative max output tokens must be rejected")
	}

	// 3. Zero Identity CanonicalDigest
	badIdent := facts
	badIdent.Identity.CanonicalDigest = largebody.IdentityDigest{}
	if err := badIdent.Validate(testBudget); err == nil {
		t.Error("zero canonical digest must be rejected")
	}

	// 4. Zero Source Digest
	badSource := facts
	badSource.Source.SourceDigest = largebody.SourceDigest{}
	if err := badSource.Validate(testBudget); err == nil {
		t.Error("zero source digest must be rejected")
	}

	// 5. Negative BodyBytes
	badBytes := facts
	badBytes.Source.BodyBytes = -5
	if err := badBytes.Validate(testBudget); err == nil {
		t.Error("negative body bytes must be rejected")
	}

	// 6. Oversized Economic AccountID
	badEcon := facts
	badEcon.Economic.AccountID = strings.Repeat("a", int(testBudget)+1)
	if err := badEcon.Validate(testBudget); err == nil {
		t.Error("oversized account id must be rejected")
	}
}

func TestTask12_1_WireRuntimeFacts_ConstructFromProofAndAssessment(t *testing.T) {
	t.Parallel()
	proof := largebody.Proof{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "gpt-5",
		ClientModel:     "gpt-5",
		MaxOutputTokens: 4096,
		Facts: largebody.ProtocolFacts{
			RequirementsID: "req-responses-v1",
			ControlCount:   1,
		},
		Mode:      largebody.BodyModeIdentityJSON,
		Rewrite:   largebody.NewNoRewrite(),
		Identity:  largebody.NewIdentityDigest([32]byte{1, 2, 3}),
		Turn:      largebody.ClientTurnShape{TotalContentBytes: 1024},
		Session:   largebody.SessionInput{AuthoritativeSessionID: "sess-123"},
		Source:    largebody.NewSourceDigest([32]byte{4, 5, 6}),
		BodyBytes: 1024,
	}

	stamp, err := largebody.BindAssessmentStamp("gen-test-1", proof)
	if err != nil {
		t.Fatalf("BindAssessmentStamp error: %v", err)
	}

	facts, err := largebody.NewWireTurnFactsFromProof(proof, stamp, "req-123", "trace-123", "bill-call-123")
	if err != nil {
		t.Fatalf("NewWireTurnFactsFromProof error: %v", err)
	}

	if facts.Route.RouteSelector != "gpt-5" {
		t.Errorf("got selector %q, want gpt-5", facts.Route.RouteSelector)
	}
	if facts.Protocol.Operation != aipapi.OperationOpenAIResponses {
		t.Errorf("got op %v, want %v", facts.Protocol.Operation, aipapi.OperationOpenAIResponses)
	}
	if facts.MaxOutput.MaxOutputTokens != 4096 {
		t.Errorf("got max output %d, want 4096", facts.MaxOutput.MaxOutputTokens)
	}
	if facts.Identity.RequestID != "req-123" {
		t.Errorf("got req id %q, want req-123", facts.Identity.RequestID)
	}
	if facts.Identity.TraceID != "trace-123" {
		t.Errorf("got trace id %q, want trace-123", facts.Identity.TraceID)
	}
	if facts.Identity.CanonicalDigest != proof.Identity {
		t.Errorf("canonical digest mismatch")
	}
	if facts.Source.SourceDigest != proof.Source {
		t.Errorf("source digest mismatch")
	}
	if facts.Source.BodyBytes != 1024 {
		t.Errorf("body bytes mismatch")
	}
	if facts.Economic.BillingCallID != "bill-call-123" {
		t.Errorf("got billing call id %q, want bill-call-123", facts.Economic.BillingCallID)
	}
	if facts.Economic.MaxOutputQuantity != 4096 {
		t.Errorf("got max output quantity %d, want 4096", facts.Economic.MaxOutputQuantity)
	}
	if facts.Economic.RequestCount != 1 {
		t.Errorf("got request count %d, want 1", facts.Economic.RequestCount)
	}
}

func TestTask12_1_WireRuntimeFacts_DeriveExistingWireFactTypes(t *testing.T) {
	t.Parallel()
	facts := largebody.DefaultTestWireTurnFacts()

	// 1. Derive WireRequestFacts
	wireReq := facts.ToWireRequestFacts("candidate-backend-model")
	if err := wireReq.Validate(testBudget); err != nil {
		t.Fatalf("derived WireRequestFacts invalid: %v", err)
	}
	if wireReq.CandidateModel != "candidate-backend-model" {
		t.Errorf("got candidate model %q", wireReq.CandidateModel)
	}
	if wireReq.ClientModel != facts.Route.ClientModel {
		t.Errorf("got client model %q", wireReq.ClientModel)
	}
	if wireReq.MaxOutputTokens != facts.MaxOutput.MaxOutputTokens {
		t.Errorf("got max output %d", wireReq.MaxOutputTokens)
	}

	// 2. Derive WireDomainFacts
	wireDomain := facts.ToWireDomainFacts(false, "candidate-backend-model")
	if err := wireDomain.Validate(testBudget); err != nil {
		t.Fatalf("derived WireDomainFacts invalid: %v", err)
	}
	if len(wireDomain.CandidateModels) != 1 || wireDomain.CandidateModels[0] != "candidate-backend-model" {
		t.Errorf("unexpected candidate models: %v", wireDomain.CandidateModels)
	}

	// 3. Derive ResponseFacts
	respFacts := facts.ToResponseFacts("effective-backend-model")
	if err := respFacts.Validate(testBudget); err != nil {
		t.Fatalf("derived ResponseFacts invalid: %v", err)
	}
	if respFacts.RequestID != facts.Identity.RequestID {
		t.Errorf("got request id %q, want %q", respFacts.RequestID, facts.Identity.RequestID)
	}
	if respFacts.EffectiveModel != "effective-backend-model" {
		t.Errorf("got effective model %q", respFacts.EffectiveModel)
	}

	// 4. Derive SessionResponseCarrier
	carrier := facts.ToSessionResponseCarrier()
	if err := carrier.Validate(testBudget); err != nil {
		t.Fatalf("derived SessionResponseCarrier invalid: %v", err)
	}
	if carrier.AuthoritativeSessionID != facts.Session.Input.AuthoritativeSessionID {
		t.Errorf("carrier session id mismatch")
	}
}
