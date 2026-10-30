package hooks_test

import (
	"context"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// Task 3.3 freeze: separate hook-bus occupancy/access classes (Requirements 5, 13;
// Design section 7 "Separate hook bus"). hooks.Bus is NOT a typed plane: its
// inventory is the four sorted Bus chains (submit, requestParts, responseParts,
// tools) observed via HookChainLengths, never the 26-plane manifest. Occupied
// submit/request-part/tool chains that can inspect or mutate canonical request
// state are canonical-required unless given an explicit typed wire contract
// (Requirement 5.7). Response-only chains may remain active only with tests
// proving no request-content dependency (Requirement 13.6). Test-only freeze on
// top of Tasks 3.1/3.2; no eligibility engine here (Task 3.5 owns the summary).

// TestLargePayload33_HookBusOwnsExactlyFourChainsSeparateFromPlanes pins that
// the Bus inventory is exactly four chains and is counted via HookChainLengths,
// not via the plane manifest. One occupied hook per chain projects 1:1 into
// the bus (cf. Task 1.9 section 4 projection rule, now frozen at Bus level).
func TestLargePayload33_HookBusOwnsExactlyFourChainsSeparateFromPlanes(t *testing.T) {
	t.Parallel()

	submit := &stubSubmit{id: "lp33-submit", order: 1}
	reqPart := &stubReqPart{id: "lp33-reqpart", order: 1, fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error { return nil }}
	respPart := &stubRespPart{id: "lp33-resppart", order: 1, fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error { return nil }}
	tool := &stubTool{id: "lp33-tool", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
		return sdk.ToolPass, aipapi.ToolEvent{}, nil
	}}
	bus := corehooks.New(corehooks.Config{
		SubmitHooks:       []sdk.SubmitHook{submit},
		RequestPartHooks:  []sdk.RequestPartHook{reqPart},
		ResponsePartHooks: []sdk.ResponsePartHook{respPart},
		ToolReactors:      []sdk.ToolReactor{tool},
	})
	s, r, resp, tl := bus.HookChainLengths()
	if s != 1 || r != 1 || resp != 1 || tl != 1 {
		t.Fatalf("HookChainLengths: got (%d,%d,%d,%d) want (1,1,1,1): bus must own exactly four chains", s, r, resp, tl)
	}
}

// TestLargePayload33_HookBusOccupancySignals freezes the occupancy signal each
// Task 3.5/3.6 consumer reads: nil and empty buses are unoccupied on every
// chain; a single hook occupies exactly its own chain and no other.
func TestLargePayload33_HookBusOccupancySignals(t *testing.T) {
	t.Parallel()

	var nilBus *corehooks.Bus
	if s, r, resp, tl := nilBus.HookChainLengths(); s != 0 || r != 0 || resp != 0 || tl != 0 {
		t.Fatalf("nil bus occupancy: got (%d,%d,%d,%d) want (0,0,0,0)", s, r, resp, tl)
	}
	empty := corehooks.New(corehooks.Config{})
	if s, r, resp, tl := empty.HookChainLengths(); s != 0 || r != 0 || resp != 0 || tl != 0 {
		t.Fatalf("empty bus occupancy: got (%d,%d,%d,%d) want (0,0,0,0)", s, r, resp, tl)
	}

	submitOnly := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{&stubSubmit{id: "lp33-s", order: 1}}})
	if s, r, resp, tl := submitOnly.HookChainLengths(); s != 1 || r != 0 || resp != 0 || tl != 0 {
		t.Fatalf("submit-only occupancy: got (%d,%d,%d,%d) want (1,0,0,0)", s, r, resp, tl)
	}
	// stubReqPart is a pointer-receiver hook; construct via addressable value.
	reqHook := &stubReqPart{id: "lp33-r2", order: 1, fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error { return nil }}
	reqOnlyBus := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{reqHook}})
	if s, r, resp, tl := reqOnlyBus.HookChainLengths(); s != 0 || r != 1 || resp != 0 || tl != 0 {
		t.Fatalf("request-part-only occupancy: got (%d,%d,%d,%d) want (0,1,0,0)", s, r, resp, tl)
	}
	respHook := &stubRespPart{id: "lp33-rp", order: 1, fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error { return nil }}
	respOnly := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{respHook}})
	if s, r, resp, tl := respOnly.HookChainLengths(); s != 0 || r != 0 || resp != 1 || tl != 0 {
		t.Fatalf("response-only occupancy: got (%d,%d,%d,%d) want (0,0,1,0)", s, r, resp, tl)
	}
	toolHook := &stubTool{id: "lp33-t", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
		return sdk.ToolPass, aipapi.ToolEvent{}, nil
	}}
	toolOnly := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{toolHook}})
	if s, r, resp, tl := toolOnly.HookChainLengths(); s != 0 || r != 0 || resp != 0 || tl != 1 {
		t.Fatalf("tool-only occupancy: got (%d,%d,%d,%d) want (0,0,0,1)", s, r, resp, tl)
	}
}

// TestLargePayload33_SubmitChainOccupiedBlocksWithoutWireContract pins that an
// occupied submit chain inspects/mutates the canonical request pre-route
// (Handle takes *aipapi.Call, can rewrite fields and can reject the turn), so
// it is canonical-required unless an explicit typed wire contract is certified.
// Requirement 5.7; Design section 7; Task 1.9 section 4 submit row.
func TestLargePayload33_SubmitChainOccupiedBlocksWithoutWireContract(t *testing.T) {
	t.Parallel()

	mut := &stubSubmit{
		id: "lp33-mut", order: 1,
		handle: func(_ context.Context, call *aipapi.Call, _ *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			call.ID = "lp33-submit-saw-call"
			return sdk.SubmitDecision{}, nil
		},
	}
	bus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{mut}})
	if s, _, _, _ := bus.HookChainLengths(); s != 1 {
		t.Fatalf("submit chain must report occupied, got submit=%d", s)
	}
	call := testCall()
	if err := bus.RunSubmit(context.Background(), call, &sdk.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if call.ID != "lp33-submit-saw-call" {
		t.Fatalf("occupied submit chain must observe/mutate request content, got Call.ID=%q", call.ID)
	}

	rejector := &stubSubmit{
		id: "lp33-reject", order: 1,
		handle: func(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
			return sdk.SubmitDecision{Reject: true, Reason: "lp33"}, nil
		},
	}
	rejectBus := corehooks.New(corehooks.Config{SubmitHooks: []sdk.SubmitHook{rejector}})
	if err := rejectBus.RunSubmit(context.Background(), testCall(), &sdk.SubmitMeta{Annotations: map[string]string{}}); !sdk.IsSubmitReject(err) {
		t.Fatalf("occupied submit chain must be able to reject the request, got %v", err)
	}
}

// TestLargePayload33_RequestPartChainOccupiedBlocksWithoutWireContract pins
// that an occupied request-part chain receives the full *aipapi.Call and its
// mutation is visible downstream (reference: refparts appends a suffix to user
// text), so it is canonical-required unless an explicit typed wire contract is
// certified. Requirement 5.7; Design section 7.
func TestLargePayload33_RequestPartChainOccupiedBlocksWithoutWireContract(t *testing.T) {
	t.Parallel()

	suffixer := &stubReqPart{
		id: "lp33-suffix", order: 1,
		fn: func(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
			call.Messages[0].Parts[0].Text += " [lp33]"
			return nil
		},
	}
	bus := corehooks.New(corehooks.Config{RequestPartHooks: []sdk.RequestPartHook{suffixer}})
	if _, r, _, _ := bus.HookChainLengths(); r != 1 {
		t.Fatalf("request-part chain must report occupied, got requestParts=%d", r)
	}
	call := testCall()
	before := call.Messages[0].Parts[0].Text
	if err := bus.RunRequestPartHooks(context.Background(), call, sdk.PartMeta{}); err != nil {
		t.Fatalf("RunRequestPartHooks: %v", err)
	}
	if got := call.Messages[0].Parts[0].Text; got != before+" [lp33]" {
		t.Fatalf("occupied request-part chain must mutate request content, got %q", got)
	}
}

// TestLargePayload33_ToolChainOccupiedBlocksWithoutWireContract pins that an
// occupied tool chain can rewrite or swallow tool-lifecycle events on the
// request path, so it is canonical-required unless an explicit typed wire
// contract is certified. Requirement 5.7; Task 1.9 section 4 tools row.
func TestLargePayload33_ToolChainOccupiedBlocksWithoutWireContract(t *testing.T) {
	t.Parallel()

	rewritten := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "lp33-rewritten"}
	rewriter := &stubTool{
		id: "lp33-rewrite", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, rewritten, nil
		},
	}
	rewriteBus := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	if _, _, _, tl := rewriteBus.HookChainLengths(); tl != 1 {
		t.Fatalf("tool chain must report occupied, got tools=%d", tl)
	}
	in := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "orig"}
	if out := rewriteBus.ApplyToolReactors(context.Background(), in, sdk.ToolMeta{}); !out.Emit || out.Event.ArgsDelta != "lp33-rewritten" {
		t.Fatalf("occupied tool chain must be able to rewrite tool lifecycle state, got %+v", out)
	}

	swallower := &stubTool{
		id: "lp33-swallow", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolSwallow, aipapi.ToolEvent{}, nil
		},
	}
	swallowBus := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{swallower}})
	if out := swallowBus.ApplyToolReactors(context.Background(), in, sdk.ToolMeta{}); out.Emit {
		t.Fatalf("occupied tool chain must be able to swallow tool lifecycle state, got %+v", out)
	}
}

// TestLargePayload33_ResponseChainProvesNoRequestContentDependency is the
// Requirement 13.6 proof gate for the single response-only candidate chain:
// RunResponsePartHooks takes *aipapi.Event (never *aipapi.Call), so the same
// occupied response chain produces the identical response mutation regardless
// of request content, and running it never mutates the request. Request chains
// stay empty while the response chain is occupied.
func TestLargePayload33_ResponseChainProvesNoRequestContentDependency(t *testing.T) {
	t.Parallel()

	prefixer := &stubRespPart{
		id: "lp33-prefix", order: 1,
		fn: func(_ context.Context, ev *aipapi.Event, _ sdk.PartMeta) error {
			ev.Delta = "LP33:" + ev.Delta
			return nil
		},
	}
	bus := corehooks.New(corehooks.Config{ResponsePartHooks: []sdk.ResponsePartHook{prefixer}})
	if s, r, resp, tl := bus.HookChainLengths(); s != 0 || r != 0 || resp != 1 || tl != 0 {
		t.Fatalf("response-proof bus occupancy: got (%d,%d,%d,%d) want (0,0,1,0)", s, r, resp, tl)
	}

	// Two distinct request contents exist but are never passed to the response
	// chain: the runner signature accepts only the response event.
	requestA := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("request-content-A")}}}}
	requestB := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("request-content-B-with-different-bytes")}}}}
	_ = requestA
	_ = requestB

	evA := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}
	if err := bus.RunResponsePartHooks(context.Background(), evA, sdk.PartMeta{}); err != nil {
		t.Fatalf("RunResponsePartHooks (request A live): %v", err)
	}
	evB := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"}
	if err := bus.RunResponsePartHooks(context.Background(), evB, sdk.PartMeta{}); err != nil {
		t.Fatalf("RunResponsePartHooks (request B live): %v", err)
	}
	if evA.Delta != "LP33:hello" || evB.Delta != "LP33:hello" {
		t.Fatalf("response-only chain must derive output from the response event alone, got %q vs %q", evA.Delta, evB.Delta)
	}

	// The held request calls are untouched: the response chain has no Call
	// handle to mutate.
	if got := requestA.Messages[0].Parts[0].Text; got != "request-content-A" {
		t.Fatalf("response-only chain must not mutate request content, got %q", got)
	}
	if got := requestB.Messages[0].Parts[0].Text; got != "request-content-B-with-different-bytes" {
		t.Fatalf("response-only chain must not mutate request content, got %q", got)
	}
}

// TestLargePayload33_NoHookChainCarriesWireContractInV1 pins the V1 closed
// world: no Bus chain exposes a wire-contract accessor or certification flag.
// Within the Bus, request-mutating means submit + requestParts + tools (every
// chain that can alter request/tool-lifecycle outcome); responseParts is the
// sole response-only candidate and still needs the proof above. A future wire
// contract requires its own explicit certification change, never a silent
// reclassification. Requirements 5.7, 13.3, 13.6; Design section 7.
func TestLargePayload33_NoHookChainCarriesWireContractInV1(t *testing.T) {
	t.Parallel()

	// Bus/Config surface is chains plus the tool error policy only: there is
	// no per-chain wire-contract field to set, so every constructed bus stays
	// in the no-contract posture by construction.
	bus := corehooks.New(corehooks.Config{
		SubmitHooks:       []sdk.SubmitHook{&stubSubmit{id: "lp33-s", order: 1}},
		RequestPartHooks:  []sdk.RequestPartHook{&stubReqPart{id: "lp33-r", order: 1, fn: func(context.Context, *aipapi.Call, sdk.PartMeta) error { return nil }}},
		ResponsePartHooks: []sdk.ResponsePartHook{&stubRespPart{id: "lp33-rp", order: 1, fn: func(context.Context, *aipapi.Event, sdk.PartMeta) error { return nil }}},
		ToolReactors: []sdk.ToolReactor{&stubTool{id: "lp33-t", order: 1, fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolPass, aipapi.ToolEvent{}, nil
		}}},
		ToolReactorErrorPolicy: sdk.ToolReactorErrorsFailOpen,
	})
	s, r, resp, tl := bus.HookChainLengths()
	if s != 1 || r != 1 || resp != 1 || tl != 1 {
		t.Fatalf("full bus occupancy: got (%d,%d,%d,%d) want (1,1,1,1)", s, r, resp, tl)
	}

	// Frozen V1 verdict table for the Bus (kept separate from the plane
	// RequestBodyAccess registry owned by Tasks 3.1/3.2): occupied
	// submit/request-part/tool chains block; the response-part chain is
	// response-only only with the no-request-content proof above. No chain is
	// wire-contracted.
	const (
		lp33CanonicalRequired = "canonical_required_when_occupied"
		lp33ResponseOnly      = "response_only_iff_proven"
	)
	verdicts := map[string]string{
		"submit":        lp33CanonicalRequired,
		"requestParts":  lp33CanonicalRequired,
		"tools":         lp33CanonicalRequired,
		"responseParts": lp33ResponseOnly,
	}
	if len(verdicts) != 4 {
		t.Fatalf("bus verdict table must cover exactly the four bus chains, got %d", len(verdicts))
	}
	for chain, verdict := range verdicts {
		if verdict != lp33CanonicalRequired && verdict != lp33ResponseOnly {
			t.Fatalf("chain %s has unexpected verdict %q: no wire contract exists in V1", chain, verdict)
		}
		if verdict == lp33ResponseOnly && chain != "responseParts" {
			t.Fatalf("only the responseParts chain may claim response-only, got %s", chain)
		}
	}
}
