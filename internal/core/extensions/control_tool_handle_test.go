// Bounded control-handler stage for task 4.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Handler Semantics; requirements 3.5, 5.1-5.7, 11.1-11.3,
// 12.3-12.4).
//
// Generic extensions only. Nothing here names a concrete feature: the fixture is
// the same anonymous proxy-owned control tool any feature generation would
// contribute, and every case drives the one generic handler seam with frozen
// request/attempt provenance.
//
// The privacy property under test is the bounded failure: whatever a misbehaving
// provider puts in an error, a panic value, or an outcome, nothing outside this
// stage may observe it. Each hostile fixture plants a distinct secret so a
// regression that leaks provider text through a wrapper, a log line, or a public
// metadata field fails loudly.
package extensions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

const (
	handleProviderID = "generic-control-handler-test"
	handleToolName   = "proxy_control_handler"
	handleToolDesc   = "Call this only when the assigned proxy-local control action is complete."
	handleSchema     = `{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`
	handleArgs       = `{"note":"done"}`
	handleMaxArgs    = 4096
	handleCallID     = "call-handler-1"
)

// Distinct secrets so an assertion can name which hostile surface leaked.
const (
	handleErrorSecret  = "provider-error-secret-AAA111"
	handlePanicSecret  = "provider-panic-secret-BBB222"
	handleResultSecret = "provider-result-secret-CCC333"
	handleCallSecret   = "provider-call-secret-DDD444"
)

// handleFixtureProvider is the generation-admitted generic control provider. It
// counts every live method so a handler stage that re-reads identity or spec
// instead of validating the frozen values fails its own assertion, and it can be
// armed with any hostile outcome.
type handleFixtureProvider struct {
	outcome controltool.Outcome
	err     error
	panicV  any
	// cancelBeforeReturn cancels the request context from inside Handle, so a
	// provider that returns an apparently valid outcome after cancellation is
	// reproducible.
	cancelBeforeReturn context.CancelFunc

	mu       sync.Mutex
	idCalls  int
	specCall int
	calls    []handleObservedCall
	argBytes []string
}

type handleObservedCall struct {
	call controltool.CompletedCall
	meta controltool.Meta
}

func (p *handleFixtureProvider) ID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idCalls++
	return handleProviderID
}

func (p *handleFixtureProvider) Spec() controltool.Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.specCall++
	return handleFixtureSpec()
}

func (p *handleFixtureProvider) Handle(_ context.Context, call controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	p.calls = append(p.calls, handleObservedCall{
		call: controltool.CompletedCall{ToolCallID: call.ToolCallID, ToolName: call.ToolName, ArgsJSON: append([]byte(nil), call.ArgsJSON...)},
		meta: meta,
	})
	p.argBytes = append(p.argBytes, string(call.ArgsJSON))
	cancel := p.cancelBeforeReturn
	p.mu.Unlock()

	if p.panicV != nil {
		panic(p.panicV)
	}
	if p.err != nil {
		return controltool.Outcome{}, p.err
	}
	if cancel != nil {
		cancel()
	}
	return p.outcome, nil
}

func (p *handleFixtureProvider) liveReads() (id, spec int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.idCalls, p.specCall
}

func (p *handleFixtureProvider) rebaseLiveReads() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idCalls, p.specCall = 0, 0
}

func (p *handleFixtureProvider) observed() []handleObservedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]handleObservedCall, len(p.calls))
	copy(out, p.calls)
	return out
}

func (p *handleFixtureProvider) handleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func handleFixtureSpec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        handleToolName,
			Description: handleToolDesc,
			Parameters:  json.RawMessage(handleSchema),
		},
		Instruction: controltool.Instruction{
			Role: aipapi.RoleSystem,
			Text: "Call proxy_control_handler only when the assigned control action is complete.",
		},
		MaxArgsBytes: handleMaxArgs,
	}
}

// handleFixtureMeta is frozen request/attempt provenance with deep-owned views,
// exactly like the activation the request path captured.
func handleFixtureMeta() controltool.Meta {
	return controltool.Meta{
		TraceID:      "trace-handler-1",
		ALegID:       "aleg-handler-1",
		BLegID:       "bleg-handler-1",
		CandidateKey: "openai:gpt-4",
		AttemptSeq:   2,
	}
}

func handleFixtureCall() controltool.CompletedCall {
	return controltool.CompletedCall{
		ToolCallID: handleCallID,
		ToolName:   handleToolName,
		ArgsJSON:   []byte(handleArgs),
	}
}

func handleRequest(provider controltool.Provider) extensions.ControlToolHandleRequest {
	return extensions.ControlToolHandleRequest{
		ProviderID:   handleProviderID,
		Provider:     provider,
		Call:         handleFixtureCall(),
		Meta:         handleFixtureMeta(),
		MaxArgsBytes: handleMaxArgs,
	}
}

// newHandleProvider composes the provider the way a feature generation does and
// then rebases the live-read counters, so every assertion observes only
// request-time calls rather than composition's own validation.
func newHandleProvider(t *testing.T, outcome controltool.Outcome) *handleFixtureProvider {
	t.Helper()
	provider := &handleFixtureProvider{outcome: outcome}
	require.NoError(t, controltool.ValidateProvider(provider),
		"generation composition must accept the generic control spec")
	provider.rebaseLiveReads()
	return provider
}

// assertBoundedFailure asserts the bounded seam: the static sentinel, no provider
// text, no panic error in the chain, and no leak of the planted secret.
func assertBoundedFailure(t *testing.T, err error, secret string) {
	t.Helper()
	require.Error(t, err, "a handled control failure must not be silent")
	assert.ErrorIs(t, err, extensions.ErrControlHandleFailed,
		"every provider-side failure must collapse into the static sentinel; err=%v", err)
	var pe *safety.PanicError
	assert.False(t, errors.As(err, &pe),
		"an isolated provider panic must not escape with its server stack attached; err=%v", err)
	if secret == "" {
		return
	}
	assert.NotContains(t, err.Error(), secret,
		"no provider text may reach the returned error; err=%v", err)
}

// TestHandleControlTool_validOutcomesAreReturnedUnchanged pins the success seam:
// a bounded, valid OutcomeInvalid and OutcomeComplete are returned to the caller
// exactly as the provider produced them, so the later terminal owner (tasks 5.1
// and 5.2) reads the provider's own bounded values rather than a re-derived copy.
func TestHandleControlTool_validOutcomesAreReturnedUnchanged(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		outcome controltool.Outcome
	}{
		{name: "complete", outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "finished", ReasonCode: "control_complete"}},
		{name: "invalid", outcome: controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "model_mistake"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := newHandleProvider(t, tc.outcome)
			outcome, err := extensions.HandleControlTool(context.Background(), handleRequest(provider))
			require.NoError(t, err, "a valid bounded outcome must be returned")
			assert.Equal(t, tc.outcome, outcome, "a valid outcome must pass through unchanged")
			require.NoError(t, controltool.ValidateOutcome(outcome), "the SDK oracle must accept the returned outcome")

			id, spec := provider.liveReads()
			assert.Zero(t, id, "the handler stage must never read the provider's live identity")
			assert.Zero(t, spec, "the handler stage must never read the provider's live spec")
		})
	}
}

// TestHandleControlTool_providerFailureModesAreBoundedAndContentFree is the core
// privacy case (requirements 11.2, 12.3): a provider error carrying arbitrary
// text, a handler that panics with a secret, an unknown outcome kind, and every
// invalid or oversized outcome field all fail with the same static,
// content-free sentinel. None of the planted secrets may appear in the returned
// error, and no isolated-panic error with its server stack may escape.
func TestHandleControlTool_providerFailureModesAreBoundedAndContentFree(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		arm     func(p *handleFixtureProvider)
		secret  string
		handled bool
	}{
		{
			name:    "provider_error_carries_unbounded_text",
			arm:     func(p *handleFixtureProvider) { p.err = errors.New("upstream said " + handleErrorSecret) },
			secret:  handleErrorSecret,
			handled: true,
		},
		{
			name:    "provider_panics_with_a_secret",
			arm:     func(p *handleFixtureProvider) { p.panicV = "panic payload " + handlePanicSecret },
			secret:  handlePanicSecret,
			handled: true,
		},
		{
			name: "unknown_outcome_kind",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeKind(9), ReasonCode: "weird"}
			},
			secret:  "",
			handled: true,
		},
		{
			name: "invalid_outcome_carries_client_output",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid, ResultText: handleResultSecret, ReasonCode: "r"}
			},
			secret:  handleResultSecret,
			handled: true,
		},
		{
			name: "complete_outcome_with_empty_result",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeComplete, ReasonCode: "r"}
			},
			secret:  "",
			handled: true,
		},
		{
			name: "complete_outcome_with_oversized_result",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: strings.Repeat("x", controltool.MaxResultTextBytes+1), ReasonCode: "r"}
			},
			secret:  "",
			handled: true,
		},
		{
			name: "complete_outcome_with_invalid_utf8_result",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: string([]byte{0xff, 0xfe}), ReasonCode: "r"}
			},
			secret:  "",
			handled: true,
		},
		{
			name: "complete_outcome_with_nul_result",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok\x00bad", ReasonCode: "r"}
			},
			secret:  "",
			handled: true,
		},
		{
			name:    "missing_reason_code",
			arm:     func(p *handleFixtureProvider) { p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid} },
			secret:  "",
			handled: true,
		},
		{
			name: "reason_code_is_not_content_free",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "result was " + handleResultSecret}
			},
			secret:  handleResultSecret,
			handled: true,
		},
		{
			name: "reason_code_is_oversized",
			arm: func(p *handleFixtureProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: strings.Repeat("a", controltool.MaxReasonCodeBytes+1)}
			},
			secret:  "",
			handled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "unused"})
			tc.arm(provider)

			outcome, err := extensions.HandleControlTool(context.Background(), handleRequest(provider))
			assertBoundedFailure(t, err, tc.secret)
			assert.Equal(t, controltool.Outcome{}, outcome,
				"a failed or invalid outcome must never leave a partial value behind")
			if tc.handled {
				assert.Positive(t, provider.handleCount(),
					"the provider must actually have run; otherwise this case proves nothing")
			}

			id, spec := provider.liveReads()
			assert.Zero(t, id, "the handler stage must never read the provider's live identity")
			assert.Zero(t, spec, "the handler stage must never read the provider's live spec")
		})
	}
}

// TestHandleControlTool_frozenInputIsValidatedBeforeTheProviderRuns pins that
// identity, provenance, and the args budget are checked from the supplied frozen
// values, using the existing pure SDK validators rather than any duplicated
// policy, and that an invalid frozen input fails without ever invoking the
// provider. A response path that handed over an oversized call or an unnamed
// owner must fail here rather than reach provider-owned decoding.
func TestHandleControlTool_frozenInputIsValidatedBeforeTheProviderRuns(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		mutate func(in *extensions.ControlToolHandleRequest)
	}{
		{
			name:   "unnamed_provider_identity",
			mutate: func(in *extensions.ControlToolHandleRequest) { in.ProviderID = "" },
		},
		{
			name: "oversized_provider_identity",
			mutate: func(in *extensions.ControlToolHandleRequest) {
				in.ProviderID = strings.Repeat("p", controltool.MaxProviderIDBytes+1)
			},
		},
		{
			name:   "negative_attempt_sequence",
			mutate: func(in *extensions.ControlToolHandleRequest) { in.Meta.AttemptSeq = -1 },
		},
		{
			name: "oversized_b_leg_id",
			mutate: func(in *extensions.ControlToolHandleRequest) {
				in.Meta.BLegID = strings.Repeat("b", controltool.MaxIdentifierBytes+1)
			},
		},
		{
			name:   "missing_call_identity",
			mutate: func(in *extensions.ControlToolHandleRequest) { in.Call.ToolCallID = "" },
		},
		{
			name: "args_exceed_the_frozen_budget",
			mutate: func(in *extensions.ControlToolHandleRequest) {
				in.Call.ArgsJSON = []byte(`{"note":"` + strings.Repeat("x", handleMaxArgs) + `"}`)
			},
		},
		{
			name:   "args_budget_is_required",
			mutate: func(in *extensions.ControlToolHandleRequest) { in.MaxArgsBytes = 0 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
			req := handleRequest(provider)
			tc.mutate(&req)

			outcome, err := extensions.HandleControlTool(context.Background(), req)
			require.Error(t, err, "an invalid frozen input must fail closed")
			assert.Equal(t, controltool.Outcome{}, outcome, "no outcome may survive a rejected frozen input")
			assert.Zero(t, provider.handleCount(),
				"an invalid frozen input must be rejected before the provider is invoked")
		})
	}
}

// TestHandleControlTool_nilProviderFailsClosed pins the wiring guard: a live
// capture can only exist for an active activation, so a nil provider is a
// defect, and it fails with the SDK's own invalid-provider sentinel rather than
// panicking on a nil interface.
func TestHandleControlTool_nilProviderFailsClosed(t *testing.T) {
	t.Parallel()

	req := handleRequest(nil)
	outcome, err := extensions.HandleControlTool(context.Background(), req)
	require.Error(t, err, "an absent provider must fail closed")
	assert.ErrorIs(t, err, controltool.ErrInvalidProvider, "the SDK sentinel must classify the wiring defect; err=%v", err)
	assert.Equal(t, controltool.Outcome{}, outcome, "no outcome may be produced without a provider")
}

// TestHandleControlTool_cancellationIsPreserved pins that the caller's own
// cancellation stays distinguishable, both before and after the provider runs. A
// provider that cancels the context and then returns an apparently valid outcome
// must not be able to smuggle that outcome out of a canceled turn.
func TestHandleControlTool_cancellationIsPreserved(t *testing.T) {
	t.Parallel()

	t.Run("already_canceled_before_the_provider_runs", func(t *testing.T) {
		t.Parallel()

		provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		outcome, err := extensions.HandleControlTool(ctx, handleRequest(provider))
		require.ErrorIs(t, err, context.Canceled, "cancellation must stay distinguishable")
		assert.Equal(t, controltool.Outcome{}, outcome, "a canceled turn must not produce an outcome")
		assert.Zero(t, provider.handleCount(), "an already-canceled context must not invoke the provider")
	})

	t.Run("canceled_while_the_provider_runs", func(t *testing.T) {
		t.Parallel()

		provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		provider.cancelBeforeReturn = cancel

		outcome, err := extensions.HandleControlTool(ctx, handleRequest(provider))
		require.ErrorIs(t, err, context.Canceled,
			"a provider that returns a valid outcome after cancellation must not defeat cancellation")
		assert.Equal(t, controltool.Outcome{}, outcome,
			"an outcome produced after cancellation must be discarded")
		assert.Positive(t, provider.handleCount(), "the provider must actually have run; otherwise this case proves nothing")
	})

	t.Run("deadline_expired", func(t *testing.T) {
		t.Parallel()

		provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		t.Cleanup(cancel)

		outcome, err := extensions.HandleControlTool(ctx, handleRequest(provider))
		require.ErrorIs(t, err, context.DeadlineExceeded, "an expired deadline must stay distinguishable")
		assert.Equal(t, controltool.Outcome{}, outcome, "an expired turn must not produce an outcome")
		assert.Zero(t, provider.handleCount(), "an expired context must not invoke the provider")
	})
}

// TestHandleControlTool_providerReceivesOwnedBoundedBytes pins that the handler
// receives a value copy of the completed call and the frozen provenance, and that
// the bytes it observes are the caller's owned bounded bytes rather than a
// mutable alias of a canonical wire event.
func TestHandleControlTool_providerReceivesOwnedBoundedBytes(t *testing.T) {
	t.Parallel()

	provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
	req := handleRequest(provider)

	_, err := extensions.HandleControlTool(context.Background(), req)
	require.NoError(t, err)
	observed := provider.observed()
	require.Len(t, observed, 1, "the provider must be invoked exactly once")

	got := observed[0].call
	assert.Equal(t, handleCallID, got.ToolCallID, "the provider must receive the captured call identity")
	assert.Equal(t, handleToolName, got.ToolName, "the provider must receive the frozen tool name")
	assert.Equal(t, handleArgs, string(got.ArgsJSON), "the provider must receive the owned bounded argument bytes")
	assert.Equal(t, handleFixtureMeta(), got0Meta(observed[0]),
		"the provider must receive the frozen request/attempt provenance unchanged")

	// The caller's bytes stay owned by the caller: a later mutation of the
	// request value cannot reach the provider through a shared alias, because the
	// provider already received its own copy.
	require.NoError(t, controltool.ValidateCompletedCall(got, handleMaxArgs),
		"the SDK oracle must accept the handed-off call")
}

func got0Meta(observed handleObservedCall) controltool.Meta { return observed.meta }

// TestHandleControlTool_genericSeamNamesNoFeature pins requirement 12.1 at this
// stage: the request and failure vocabulary stays generic. This is a cheap guard
// against a provider-name or feature-name switch leaking into the handler seam.
func TestHandleControlTool_genericSeamNamesNoFeature(t *testing.T) {
	t.Parallel()

	provider := newHandleProvider(t, controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "r"})
	_, err := extensions.HandleControlTool(context.Background(), handleRequest(provider))
	require.NoError(t, err)

	text := strings.ToLower(extensions.ErrControlHandleFailed.Error())
	for _, forbidden := range []string{"attempt_completion", "alg", "loop guard", "loopguard"} {
		assert.NotContains(t, text, forbidden,
			"the static handler failure must stay provider-neutral; got %q", text)
	}
}
