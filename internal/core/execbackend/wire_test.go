package execbackend_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func validWireRequestFacts(t *testing.T) largebody.WireRequestFacts {
	t.Helper()
	return largebody.WireRequestFacts{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ClientModel:     "gpt-5",
		CandidateModel:  "gpt-5",
		MaxOutputTokens: 1000,
	}
}

func validWireDomainFacts(t *testing.T) largebody.WireDomainFacts {
	t.Helper()
	return largebody.WireDomainFacts{
		ProfileID:       "openai-responses-v1",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		CandidateModels: []string{"gpt-5"},
	}
}

func certifiedModelRewriteFacts(t *testing.T) largebody.WireRequestFacts {
	t.Helper()
	span := largebody.Span{Offset: 10, Length: 5}
	rewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatalf("failed to create model token rewrite: %v", err)
	}
	facts := validWireRequestFacts(t)
	facts.Rewrite = rewrite
	facts.CandidateModel = "gpt-5-native"
	return facts
}

type testWireBackendImpl struct {
	reqResult    largebody.WireRequestSupport
	domainResult largebody.WireDomainSupport
	reqCalled    bool
	domainCalled bool
}

func (m *testWireBackendImpl) ResolveWireRequest(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
	m.reqCalled = true
	return m.reqResult
}

func (m *testWireBackendImpl) ResolveWireDomain(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
	m.domainCalled = true
	return m.domainResult
}

var (
	_ largebody.WireBackend         = (*testWireBackendImpl)(nil)
	_ largebody.WireBackendProvider = execbackend.Backend{}
)

func TestEffectiveWireRequestSupport_NilYieldsCanonical(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{} // no ResolveWireRequest, no WireBackend

	res := execbackend.EffectiveWireRequestSupport(context.Background(), be, validWireRequestFacts(t), routing.AttemptCandidate{})
	if res.Compatible {
		t.Fatalf("nil resolver must yield Compatible: false (canonical), got %v", res)
	}
	if res.Reason != largebody.WireSupportReasonUnsupported {
		t.Fatalf("expected reason unsupported, got %v", res.Reason)
	}
}

func TestEffectiveWireDomainSupport_NilYieldsCanonical(t *testing.T) {
	t.Parallel()
	be := execbackend.Backend{} // no ResolveWireDomain, no WireBackend

	res := execbackend.EffectiveWireDomainSupport(context.Background(), be, validWireDomainFacts(t))
	if res.Compatible {
		t.Fatalf("nil domain resolver must yield Compatible: false (canonical), got %v", res)
	}
	if res.Reason != largebody.WireSupportReasonUnsupported {
		t.Fatalf("expected reason unsupported, got %v", res.Reason)
	}
}

func TestEffectiveWireRequestSupport_RewriteNeedOnlyWhenCertified(t *testing.T) {
	t.Parallel()

	t.Run("declares rewrite need when certified -> compatible", func(t *testing.T) {
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
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if !res.Compatible || !res.NeedsModelRewrite {
			t.Fatalf("certified rewrite should be compatible with NeedsModelRewrite=true, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonNone {
			t.Fatalf("expected reason None, got %v", res.Reason)
		}
	})

	t.Run("declares rewrite need without certification -> fails closed to canonical", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible:        true,
					NeedsModelRewrite: true,
				}
			},
		}
		facts := validWireRequestFacts(t) // RewriteKindNone
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("uncertified rewrite need must fail closed to canonical, got %v", res)
		}
		if res.NeedsModelRewrite {
			t.Fatalf("NeedsModelRewrite must be forced false when not certified, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonRewriteUnsupported {
			t.Fatalf("expected reason RewriteUnsupported, got %v", res.Reason)
		}
	})

	t.Run("does not declare rewrite need when not needed -> compatible", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
				return largebody.WireRequestSupport{
					Compatible:        true,
					NeedsModelRewrite: false,
				}
			},
		}
		facts := validWireRequestFacts(t)
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if !res.Compatible || res.NeedsModelRewrite {
			t.Fatalf("expected compatible with NeedsModelRewrite=false, got %v", res)
		}
	})
}

func TestEffectiveWireRequestSupport_UnknownYieldsCanonical(t *testing.T) {
	t.Parallel()

	be := execbackend.Backend{
		ResolveWireRequest: func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
			return largebody.WireRequestSupport{Compatible: true}
		},
	}

	t.Run("unknown delivery mode", func(t *testing.T) {
		t.Parallel()
		facts := validWireRequestFacts(t)
		facts.Delivery = "unknown_mode"
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("unknown delivery mode must yield canonical, got %v", res)
		}
	})

	t.Run("unsupported body mode", func(t *testing.T) {
		t.Parallel()
		facts := validWireRequestFacts(t)
		facts.BodyMode = "gzip_json"
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("unsupported body mode must yield canonical, got %v", res)
		}
	})

	t.Run("empty profile id", func(t *testing.T) {
		t.Parallel()
		facts := validWireRequestFacts(t)
		facts.ProfileID = ""
		res := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("empty profile id must yield canonical, got %v", res)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := execbackend.EffectiveWireRequestSupport(ctx, be, validWireRequestFacts(t), routing.AttemptCandidate{})
		if res.Compatible {
			t.Fatalf("canceled context must yield canonical, got %v", res)
		}
	})
}

func TestEffectiveWireDomainSupport_UniversalAndFiniteDomains(t *testing.T) {
	t.Parallel()

	t.Run("universal domain without AnyAcceptedModel fails closed to canonical", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireDomain: func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
				return largebody.WireDomainSupport{
					Compatible:       true,
					AnyAcceptedModel: false,
				}
			},
		}
		facts := largebody.WireDomainFacts{
			ProfileID:      "openai-responses-v1",
			Operation:      aipapi.OperationOpenAIResponses,
			Delivery:       aipapi.DeliveryModeStreaming,
			BodyMode:       largebody.BodyModeIdentityJSON,
			UniversalModel: true,
		}
		res := execbackend.EffectiveWireDomainSupport(context.Background(), be, facts)
		if res.Compatible {
			t.Fatalf("universal domain without AnyAcceptedModel must fail closed, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonModelUnsupported {
			t.Fatalf("expected reason ModelUnsupported, got %v", res.Reason)
		}
	})

	t.Run("universal domain with AnyAcceptedModel succeeds", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireDomain: func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
				return largebody.WireDomainSupport{
					Compatible:       true,
					AnyAcceptedModel: true,
				}
			},
		}
		facts := largebody.WireDomainFacts{
			ProfileID:      "openai-responses-v1",
			Operation:      aipapi.OperationOpenAIResponses,
			Delivery:       aipapi.DeliveryModeStreaming,
			BodyMode:       largebody.BodyModeIdentityJSON,
			UniversalModel: true,
		}
		res := execbackend.EffectiveWireDomainSupport(context.Background(), be, facts)
		if !res.Compatible || !res.AnyAcceptedModel {
			t.Fatalf("universal domain with AnyAcceptedModel should succeed, got %v", res)
		}
		if res.Reason != largebody.WireSupportReasonNone {
			t.Fatalf("expected reason None, got %v", res.Reason)
		}
	})

	t.Run("finite domain with certified models succeeds", func(t *testing.T) {
		t.Parallel()
		be := execbackend.Backend{
			ResolveWireDomain: func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
				return largebody.WireDomainSupport{
					Compatible: true,
				}
			},
		}
		facts := validWireDomainFacts(t)
		res := execbackend.EffectiveWireDomainSupport(context.Background(), be, facts)
		if !res.Compatible {
			t.Fatalf("finite domain should be compatible, got %v", res)
		}
	})
}

func TestBackend_WireBackendInterfaceDelegation(t *testing.T) {
	t.Parallel()

	impl := &testWireBackendImpl{
		reqResult: largebody.WireRequestSupport{
			Compatible: true,
		},
		domainResult: largebody.WireDomainSupport{
			Compatible: true,
		},
	}

	be := execbackend.Backend{
		WireBackend: impl,
	}

	// Backend adapts to largebody.WireBackend via AsWireBackend
	wb := be.AsWireBackend()
	resReq := wb.ResolveWireRequest(context.Background(), validWireRequestFacts(t), routing.AttemptCandidate{})
	if !resReq.Compatible || !impl.reqCalled {
		t.Fatalf("expected delegation to WireBackend, got %v (called=%t)", resReq, impl.reqCalled)
	}

	resDomain := wb.ResolveWireDomain(context.Background(), validWireDomainFacts(t))
	if !resDomain.Compatible || !impl.domainCalled {
		t.Fatalf("expected delegation to WireBackend domain, got %v (called=%t)", resDomain, impl.domainCalled)
	}

	// ResolveWireRequest function field takes precedence over WireBackend interface
	funcCalled := false
	be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
		funcCalled = true
		return largebody.WireRequestSupport{Compatible: true}
	}
	resReq2 := be.ResolveWireRequest(context.Background(), validWireRequestFacts(t), routing.AttemptCandidate{})
	if !resReq2.Compatible || !funcCalled {
		t.Fatalf("ResolveWireRequest function field must take precedence, called=%t", funcCalled)
	}
}
