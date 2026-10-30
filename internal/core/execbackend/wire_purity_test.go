package execbackend_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ---------------------------------------------------------------------------
// Test Doubles proving resolvers perform NO I/O, store, or mutable session ops
// (Requirement 6.2, 6.8, Requirement 8.5; design section 8, 9).
// ---------------------------------------------------------------------------

// panicRoundTripper panics immediately on any HTTP network I/O attempt.
type panicRoundTripper struct {
	calls atomic.Int64
}

func (p *panicRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	p.calls.Add(1)
	panic("purity violation: provider HTTP network I/O attempted during wire resolution")
}

// panicStoreDouble panics immediately on any database or store read/write.
type panicStoreDouble struct {
	reads  atomic.Int64
	writes atomic.Int64
}

func (s *panicStoreDouble) Read(key string) ([]byte, error) {
	s.reads.Add(1)
	panic("purity violation: store read attempted during wire resolution")
}

func (s *panicStoreDouble) Write(key string, data []byte) error {
	s.writes.Add(1)
	panic("purity violation: store write attempted during wire resolution")
}

// panicSessionDouble panics immediately on any mutable session/turn operation.
type panicSessionDouble struct {
	turns atomic.Int64
}

func (s *panicSessionDouble) BeginTurn(ctx context.Context) error {
	s.turns.Add(1)
	panic("purity violation: BeginTurn/session mutation attempted during wire resolution")
}

func (s *panicSessionDouble) FetchALeg(ctx context.Context, id string) (any, error) {
	s.turns.Add(1)
	panic("purity violation: A-leg fetch attempted during wire resolution")
}

// panicPluginDouble panics immediately on any plugin hook execution.
type panicPluginDouble struct {
	invocations atomic.Int64
}

func (p *panicPluginDouble) ExecuteHook(name string, payload any) error {
	p.invocations.Add(1)
	panic("purity violation: unbounded plugin hook attempted during wire resolution")
}

// pureWireBackend implements largebody.WireBackend purely, holding references
// to test doubles to guarantee none of them are called during resolution.
type pureWireBackend struct {
	transport    *panicRoundTripper
	store        *panicStoreDouble
	session      *panicSessionDouble
	plugin       *panicPluginDouble
	supportedPID string
	modelCatalog map[string]bool
	allowRewrite bool
	universal    bool
	reqCalls     int64
	domainCalls  int64
}

func newPureWireBackend(profileID string, models []string, allowRewrite bool, universal bool) *pureWireBackend {
	cat := make(map[string]bool, len(models))
	for _, m := range models {
		cat[m] = true
	}
	return &pureWireBackend{
		transport:    &panicRoundTripper{},
		store:        &panicStoreDouble{},
		session:      &panicSessionDouble{},
		plugin:       &panicPluginDouble{},
		supportedPID: profileID,
		modelCatalog: cat,
		allowRewrite: allowRewrite,
		universal:    universal,
	}
}

func (b *pureWireBackend) ResolveWireRequest(
	ctx context.Context,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
) largebody.WireRequestSupport {
	atomic.AddInt64(&b.reqCalls, 1)

	// Enforce protocol binding via profile identity.
	if facts.ProfileID != b.supportedPID {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}

	// Body mode must be identity JSON.
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	// Candidate model check.
	targetModel := cand.Primary.Model
	if targetModel == "" {
		targetModel = facts.CandidateModel
	}
	if !b.universal && !b.modelCatalog[targetModel] {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonModelUnsupported,
		}
	}

	// Model rewrite determination.
	needsRewrite := false
	if targetModel != facts.ClientModel {
		if !b.allowRewrite {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonRewriteUnsupported,
			}
		}
		needsRewrite = true
	}

	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: needsRewrite,
		Reason:            largebody.WireSupportReasonNone,
	}
}

func (b *pureWireBackend) ResolveWireDomain(
	ctx context.Context,
	facts largebody.WireDomainFacts,
) largebody.WireDomainSupport {
	atomic.AddInt64(&b.domainCalls, 1)

	// Enforce protocol binding via profile identity.
	if facts.ProfileID != b.supportedPID {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}

	// Body mode must be identity JSON.
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	if facts.UniversalModel {
		if !b.universal {
			return largebody.WireDomainSupport{
				Compatible:       false,
				AnyAcceptedModel: false,
				Reason:           largebody.WireSupportReasonModelUnsupported,
			}
		}
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		}
	}

	// Finite model domain.
	for _, m := range facts.CandidateModels {
		if !b.modelCatalog[m] {
			return largebody.WireDomainSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonModelUnsupported,
			}
		}
	}

	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: b.universal,
		Reason:           largebody.WireSupportReasonNone,
	}
}

var _ largebody.WireBackend = (*pureWireBackend)(nil)

// ---------------------------------------------------------------------------
// Purity Proof Tests (Task 8.3, Requirements 6, 8, 9)
// ---------------------------------------------------------------------------

func TestWireResolvers_Purity_NoIOOrMutation(t *testing.T) {
	t.Parallel()

	wb := newPureWireBackend("openai-responses-v1", []string{"gpt-5", "gpt-5-mini"}, true, false)
	be := execbackend.Backend{WireBackend: wb}

	ctx := context.Background()
	reqFacts := validWireRequestFacts(t)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5"}}

	// 1. ResolveWireRequest must execute without triggering any test-double traps.
	resReq := execbackend.EffectiveWireRequestSupport(ctx, be, reqFacts, cand)
	if !resReq.Compatible {
		t.Fatalf("expected compatible, got %v", resReq)
	}
	if err := resReq.Validate(); err != nil {
		t.Fatalf("invalid WireRequestSupport: %v", err)
	}

	// 2. ResolveWireDomain must execute without triggering any test-double traps.
	domFacts := validWireDomainFacts(t)
	resDom := execbackend.EffectiveWireDomainSupport(ctx, be, domFacts)
	if !resDom.Compatible {
		t.Fatalf("expected domain compatible, got %v", resDom)
	}
	if err := resDom.Validate(); err != nil {
		t.Fatalf("invalid WireDomainSupport: %v", err)
	}

	// 3. Verify zero I/O, zero store calls, zero session calls, zero plugin calls.
	if calls := wb.transport.calls.Load(); calls != 0 {
		t.Fatalf("provider network I/O called %d times during resolution", calls)
	}
	if reads := wb.store.reads.Load(); reads != 0 {
		t.Fatalf("store reads called %d times during resolution", reads)
	}
	if writes := wb.store.writes.Load(); writes != 0 {
		t.Fatalf("store writes called %d times during resolution", writes)
	}
	if turns := wb.session.turns.Load(); turns != 0 {
		t.Fatalf("session/turn mutation called %d times during resolution", turns)
	}
	if hooks := wb.plugin.invocations.Load(); hooks != 0 {
		t.Fatalf("plugin hooks called %d times during resolution", hooks)
	}

	// Verify resolution calls were actually recorded.
	if atomic.LoadInt64(&wb.reqCalls) != 1 {
		t.Fatalf("expected 1 req call, got %d", wb.reqCalls)
	}
	if atomic.LoadInt64(&wb.domainCalls) != 1 {
		t.Fatalf("expected 1 domain call, got %d", wb.domainCalls)
	}
}

func TestWireResolvers_Purity_CanceledContextImmediateDecline(t *testing.T) {
	t.Parallel()

	wb := newPureWireBackend("openai-responses-v1", []string{"gpt-5"}, true, false)
	be := execbackend.Backend{WireBackend: wb}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before call

	resReq := execbackend.EffectiveWireRequestSupport(ctx, be, validWireRequestFacts(t), routing.AttemptCandidate{})
	if resReq.Compatible {
		t.Fatal("canceled context must yield Compatible: false")
	}
	if resReq.Reason != largebody.WireSupportReasonUnsupported {
		t.Fatalf("expected reason unsupported, got %v", resReq.Reason)
	}

	resDom := execbackend.EffectiveWireDomainSupport(ctx, be, validWireDomainFacts(t))
	if resDom.Compatible {
		t.Fatal("canceled context must yield Compatible: false")
	}
	if resDom.Reason != largebody.WireSupportReasonUnsupported {
		t.Fatalf("expected reason unsupported, got %v", resDom)
	}

	// Backend callbacks must NOT be called on canceled context.
	if atomic.LoadInt64(&wb.reqCalls) != 0 {
		t.Fatalf("backend ResolveWireRequest called on canceled context")
	}
	if atomic.LoadInt64(&wb.domainCalls) != 0 {
		t.Fatalf("backend ResolveWireDomain called on canceled context")
	}
}

// ---------------------------------------------------------------------------
// Protocol Binding via Profile Identity (8.1 follow-up, Requirement 8.1)
// ---------------------------------------------------------------------------

func TestWireResolvers_ProtocolBinding_ProfileIdentity(t *testing.T) {
	t.Parallel()

	wb := newPureWireBackend("openai-responses-v1", []string{"gpt-5"}, true, false)
	be := execbackend.Backend{WireBackend: wb}
	ctx := context.Background()

	t.Run("matching profile identity succeeds", func(t *testing.T) {
		t.Parallel()
		req := validWireRequestFacts(t)
		req.ProfileID = "openai-responses-v1"
		res := execbackend.EffectiveWireRequestSupport(ctx, be, req, routing.AttemptCandidate{})
		if !res.Compatible {
			t.Fatalf("matching profile id must be compatible, got %v", res)
		}
	})

	t.Run("mismatched profile identity fails closed to incompatible", func(t *testing.T) {
		t.Parallel()
		for _, mismatched := range []string{
			"anthropic-messages-v1",
			"gemini-generate-content-v1",
			"openai-chat-v1",
			"unknown-protocol-profile",
		} {
			req := validWireRequestFacts(t)
			req.ProfileID = mismatched
			res := execbackend.EffectiveWireRequestSupport(ctx, be, req, routing.AttemptCandidate{})
			if res.Compatible {
				t.Fatalf("mismatched profile %q must not be compatible, got %v", mismatched, res)
			}
			if res.Reason != largebody.WireSupportReasonProfileUnsupported {
				t.Fatalf("expected reason profile_unsupported for %q, got %v", mismatched, res.Reason)
			}

			dom := validWireDomainFacts(t)
			dom.ProfileID = mismatched
			domRes := execbackend.EffectiveWireDomainSupport(ctx, be, dom)
			if domRes.Compatible {
				t.Fatalf("domain: mismatched profile %q must not be compatible, got %v", mismatched, domRes)
			}
			if domRes.Reason != largebody.WireSupportReasonProfileUnsupported {
				t.Fatalf("domain: expected reason profile_unsupported for %q, got %v", mismatched, domRes.Reason)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Configured Semantic-Fact Budget (8.1 follow-up: replaces 1024 magic budget)
// ---------------------------------------------------------------------------

func TestWireResolvers_SemanticFactBudget_ConfiguredAndDefault(t *testing.T) {
	t.Parallel()

	wb := newPureWireBackend("openai-responses-v1", []string{"gpt-5"}, true, false)
	be := execbackend.Backend{WireBackend: wb}

	t.Run("default budget allows facts > 1024 bytes (replaces 1024 magic)", func(t *testing.T) {
		t.Parallel()
		// Create facts where a field exceeds the old 1024 magic budget but is well within DefaultMaxSemanticFactBytes (256 KiB).
		longModel := strings.Repeat("m", 2048)
		wbLong := newPureWireBackend("openai-responses-v1", []string{longModel}, true, false)
		beLong := execbackend.Backend{WireBackend: wbLong}

		req := validWireRequestFacts(t)
		req.ClientModel = longModel
		req.CandidateModel = longModel

		res := execbackend.EffectiveWireRequestSupport(context.Background(), beLong, req, routing.AttemptCandidate{})
		if !res.Compatible {
			t.Fatalf("fact with 2048-byte model must pass under DefaultMaxSemanticFactBytes, got %v", res)
		}

		dom := validWireDomainFacts(t)
		dom.CandidateModels = []string{longModel}
		resDom := execbackend.EffectiveWireDomainSupport(context.Background(), beLong, dom)
		if !resDom.Compatible {
			t.Fatalf("domain fact with 2048-byte model must pass under DefaultMaxSemanticFactBytes, got %v", resDom)
		}
	})

	t.Run("context-configured budget is enforced", func(t *testing.T) {
		t.Parallel()
		// Configure a tiny budget of 500 bytes via WithSemanticFactBudget.
		ctxSmall := largebody.WithSemanticFactBudget(context.Background(), 500)

		// 600-byte model exceeds the 500-byte budget -> must fail validation to canonical.
		req600 := validWireRequestFacts(t)
		req600.ClientModel = strings.Repeat("a", 600)
		req600.CandidateModel = strings.Repeat("a", 600)

		res600 := execbackend.EffectiveWireRequestSupport(ctxSmall, be, req600, routing.AttemptCandidate{})
		if res600.Compatible {
			t.Fatal("fact exceeding configured budget must be incompatible")
		}
		if res600.Reason != largebody.WireSupportReasonUnsupported {
			t.Fatalf("expected unsupported reason, got %v", res600.Reason)
		}

		dom600 := validWireDomainFacts(t)
		dom600.CandidateModels = []string{strings.Repeat("a", 600)}
		resDom600 := execbackend.EffectiveWireDomainSupport(ctxSmall, be, dom600)
		if resDom600.Compatible {
			t.Fatal("domain fact exceeding configured budget must be incompatible")
		}

		// 400-byte model fits within the 500-byte budget -> passes validation.
		model400 := strings.Repeat("b", 400)
		wb400 := newPureWireBackend("openai-responses-v1", []string{model400}, true, false)
		be400 := execbackend.Backend{WireBackend: wb400}

		req400 := validWireRequestFacts(t)
		req400.ClientModel = model400
		req400.CandidateModel = model400

		res400 := execbackend.EffectiveWireRequestSupport(ctxSmall, be400, req400, routing.AttemptCandidate{})
		if !res400.Compatible {
			t.Fatalf("fact fitting configured budget must be compatible, got %v", res400)
		}
	})
}

// ---------------------------------------------------------------------------
// Domain Coverage & Rewrite Contract (Requirements 7, 8, 9; design section 9)
// ---------------------------------------------------------------------------

func TestWireResolvers_DomainCoverage_ExactAndUniversal(t *testing.T) {
	t.Parallel()

	catalog := []string{"gpt-5", "gpt-5-mini", "o3"}

	t.Run("finite domain covers exact candidate model", func(t *testing.T) {
		t.Parallel()
		wb := newPureWireBackend("openai-responses-v1", catalog, true, false)
		be := execbackend.Backend{WireBackend: wb}
		ctx := context.Background()

		// In-catalog candidate with same model (no rewrite needed) -> compatible
		candSame := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5"}}
		factsSame := validWireRequestFacts(t)
		factsSame.ClientModel = "gpt-5"
		factsSame.CandidateModel = "gpt-5"
		resSame := execbackend.EffectiveWireRequestSupport(ctx, be, factsSame, candSame)
		if !resSame.Compatible || resSame.NeedsModelRewrite {
			t.Fatalf("same model must be compatible without rewrite, got %v", resSame)
		}

		// In-catalog candidate with different model + certified rewrite -> compatible with rewrite
		candDiff := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-5-mini"}}
		factsDiff := certifiedModelRewriteFacts(t)
		factsDiff.ClientModel = "gpt-5"
		factsDiff.CandidateModel = "gpt-5-mini"
		resDiff := execbackend.EffectiveWireRequestSupport(ctx, be, factsDiff, candDiff)
		if !resDiff.Compatible || !resDiff.NeedsModelRewrite {
			t.Fatalf("different catalog model with certified rewrite must be compatible with rewrite, got %v", resDiff)
		}

		// In-catalog candidate with different model but NO certified rewrite -> fails closed to canonical
		factsDiffNoRewrite := validWireRequestFacts(t)
		factsDiffNoRewrite.ClientModel = "gpt-5"
		factsDiffNoRewrite.CandidateModel = "gpt-5-mini"
		resDiffNoRewrite := execbackend.EffectiveWireRequestSupport(ctx, be, factsDiffNoRewrite, candDiff)
		if resDiffNoRewrite.Compatible {
			t.Fatalf("different model without certified rewrite must be incompatible, got %v", resDiffNoRewrite)
		}
		if resDiffNoRewrite.Reason != largebody.WireSupportReasonRewriteUnsupported {
			t.Fatalf("expected reason rewrite_unsupported, got %v", resDiffNoRewrite.Reason)
		}

		// Out-of-catalog candidate -> incompatible (model unsupported)
		outCand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-opus"}}
		outFacts := validWireRequestFacts(t)
		outFacts.CandidateModel = "claude-3-opus"
		outRes := execbackend.EffectiveWireRequestSupport(ctx, be, outFacts, outCand)
		if outRes.Compatible {
			t.Fatalf("out-of-catalog model must be incompatible, got %v", outRes)
		}
		if outRes.Reason != largebody.WireSupportReasonModelUnsupported {
			t.Fatalf("expected reason model_unsupported, got %v", outRes.Reason)
		}
	})

	t.Run("finite domain covers candidate model slice", func(t *testing.T) {
		t.Parallel()
		wb := newPureWireBackend("openai-responses-v1", catalog, true, false)
		be := execbackend.Backend{WireBackend: wb}
		ctx := context.Background()

		// Subset of catalog -> compatible
		domGood := validWireDomainFacts(t)
		domGood.CandidateModels = []string{"gpt-5", "o3"}
		resGood := execbackend.EffectiveWireDomainSupport(ctx, be, domGood)
		if !resGood.Compatible {
			t.Fatalf("subset domain should be compatible, got %v", resGood)
		}

		// Contains unknown model -> incompatible
		domBad := validWireDomainFacts(t)
		domBad.CandidateModels = []string{"gpt-5", "unknown-model"}
		resBad := execbackend.EffectiveWireDomainSupport(ctx, be, domBad)
		if resBad.Compatible {
			t.Fatalf("domain with unknown model must be incompatible, got %v", resBad)
		}
		if resBad.Reason != largebody.WireSupportReasonModelUnsupported {
			t.Fatalf("expected reason model_unsupported, got %v", resBad.Reason)
		}
	})

	t.Run("universal domain requires AnyAcceptedModel", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		// Universal backend (AnyAcceptedModel=true)
		wbUniv := newPureWireBackend("openai-responses-v1", nil, true, true)
		beUniv := execbackend.Backend{WireBackend: wbUniv}
		domUniv := largebody.WireDomainFacts{
			ProfileID:      "openai-responses-v1",
			Operation:      aipapi.OperationOpenAIResponses,
			Delivery:       aipapi.DeliveryModeStreaming,
			BodyMode:       largebody.BodyModeIdentityJSON,
			UniversalModel: true,
		}
		resUniv := execbackend.EffectiveWireDomainSupport(ctx, beUniv, domUniv)
		if !resUniv.Compatible || !resUniv.AnyAcceptedModel {
			t.Fatalf("universal domain with universal backend must succeed, got %v", resUniv)
		}

		// Non-universal backend (AnyAcceptedModel=false) -> fails closed
		wbNonUniv := newPureWireBackend("openai-responses-v1", catalog, true, false)
		beNonUniv := execbackend.Backend{WireBackend: wbNonUniv}
		resNonUniv := execbackend.EffectiveWireDomainSupport(ctx, beNonUniv, domUniv)
		if resNonUniv.Compatible {
			t.Fatalf("universal domain on non-universal backend must fail closed, got %v", resNonUniv)
		}
		if resNonUniv.Reason != largebody.WireSupportReasonModelUnsupported {
			t.Fatalf("expected reason model_unsupported, got %v", resNonUniv.Reason)
		}
	})
}

func TestWireResolvers_BodyModeAndRewriteContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("unsupported body mode declines with BodyModeUnsupported", func(t *testing.T) {
		t.Parallel()
		wb := newPureWireBackend("openai-responses-v1", []string{"gpt-5"}, true, false)
		be := execbackend.Backend{WireBackend: wb}

		req := validWireRequestFacts(t)
		req.BodyMode = "compressed_json" // non-identity
		res := execbackend.EffectiveWireRequestSupport(ctx, be, req, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatal("unsupported body mode must be incompatible")
		}

		dom := validWireDomainFacts(t)
		dom.BodyMode = "compressed_json"
		resDom := execbackend.EffectiveWireDomainSupport(ctx, be, dom)
		if resDom.Compatible {
			t.Fatal("domain: unsupported body mode must be incompatible")
		}
	})

	t.Run("backend requires rewrite without certified rewrite in facts -> fails closed", func(t *testing.T) {
		t.Parallel()
		// Backend returns NeedsModelRewrite: true
		be := execbackend.Backend{
			ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible:        true,
					NeedsModelRewrite: true,
				}
			},
		}

		// Facts has RewriteKindNone (no certified rewrite)
		facts := validWireRequestFacts(t)
		res := execbackend.EffectiveWireRequestSupport(ctx, be, facts, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("NeedsModelRewrite without certified rewrite must fail closed, got %v", res)
		}
		if res.NeedsModelRewrite {
			t.Fatalf("NeedsModelRewrite must be forced to false, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonRewriteUnsupported {
			t.Fatalf("expected reason rewrite_unsupported, got %v", res.Reason)
		}
		if err := res.Validate(); err != nil {
			t.Fatalf("res.Validate() failed: %v", err)
		}
	})

	t.Run("backend requires rewrite with certified rewrite in facts -> compatible", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible:        true,
					NeedsModelRewrite: true,
				}
			},
		}

		facts := certifiedModelRewriteFacts(t)
		res := execbackend.EffectiveWireRequestSupport(ctx, be, facts, routing.AttemptCandidate{})
		if !res.Compatible || !res.NeedsModelRewrite {
			t.Fatalf("certified rewrite must be compatible with NeedsModelRewrite=true, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonNone {
			t.Fatalf("expected reason none, got %v", res.Reason)
		}
		if err := res.Validate(); err != nil {
			t.Fatalf("res.Validate() failed: %v", err)
		}
	})

	t.Run("backend does not require rewrite with certified rewrite in facts -> compatible without rewrite", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible:        true,
					NeedsModelRewrite: false,
				}
			},
		}

		facts := certifiedModelRewriteFacts(t)
		res := execbackend.EffectiveWireRequestSupport(ctx, be, facts, routing.AttemptCandidate{})
		if !res.Compatible || res.NeedsModelRewrite {
			t.Fatalf("expected compatible with NeedsModelRewrite=false, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonNone {
			t.Fatalf("expected reason none, got %v", res.Reason)
		}
	})
}
