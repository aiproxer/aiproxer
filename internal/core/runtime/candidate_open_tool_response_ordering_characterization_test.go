package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Characterization of CURRENT candidate-open and tool-response ordering on this
// branch (task 1.1, agent-loop-explicit-completion-protocol). These tests pin
// today's production order so later control-tool projection/interception can
// hook the real seams. They must not change production behavior.
//
// Adjacent-spec snapshot at implementation time:
//   - b-leg-path-virtualization has no production symbols under internal/core/runtime;
//     assembler still sits after BTP observation and before tool policy.
//   - large-payload wire attempts bypass evaluateCandidate admission; the tests
//     below pin the canonical Call/Recv path the protocol will use.
//   - attempt-local assembler/accounting already live on attemptSession and are
//     replaced rather than reused (see TestAttemptSessionReplacementDoesNotReuseAttemptLocalResources).

const (
	algTransformProbeTool = "alg-transform-probe"
	algHookProbeTool      = "alg-hook-probe"
	algNeverBackendText   = "alg-never-backend"
	algSteeringText       = "alg-cv-steering"
	algClampTokens        = 42
)

type algOrderLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *algOrderLog) add(step string) {
	l.mu.Lock()
	l.steps = append(l.steps, step)
	l.mu.Unlock()
}

func (l *algOrderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.steps))
	copy(out, l.steps)
	return out
}

func algIndexOf(steps []string, prefix string) int {
	return algIndexOfBetween(steps, prefix, -1, -1)
}

func algIndexOfBetween(steps []string, prefix string, afterIdx, beforeIdx int) int {
	start := max(afterIdx+1, 0)
	end := len(steps)
	if beforeIdx >= 0 && beforeIdx < end {
		end = beforeIdx
	}
	for i := start; i < end; i++ {
		if strings.HasPrefix(steps[i], prefix) {
			return i
		}
	}
	return -1
}

func algIndexOfExactBetween(steps []string, exact string, afterIdx, beforeIdx int) int {
	start := max(afterIdx+1, 0)
	end := len(steps)
	if beforeIdx >= 0 && beforeIdx < end {
		end = beforeIdx
	}
	for i := start; i < end; i++ {
		if steps[i] == exact {
			return i
		}
	}
	return -1
}

func algCallHasTool(call aipapi.Call, name string) bool {
	for _, tool := range call.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func algCallShape(call aipapi.Call) string {
	if call.HasItemAuthority() {
		return "items"
	}
	return "messages"
}

func algMaxOut(call aipapi.Call) int {
	if call.Options.MaxOutputTokens == nil {
		return -1
	}
	return *call.Options.MaxOutputTokens
}

func algCallHasPlainText(call aipapi.Call, text string) bool {
	for _, m := range call.Instructions {
		for _, p := range m.Parts {
			if p.Text == text {
				return true
			}
		}
	}
	for _, m := range call.Messages {
		for _, p := range m.Parts {
			if p.Text == text {
				return true
			}
		}
	}
	for _, it := range call.Items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		for _, p := range it.Content {
			if p.Text == text {
				return true
			}
		}
	}
	return false
}

func algNeverBackendMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(algNeverBackendText)}}
}

func algLiveConversationSnapshot(t *testing.T) conversationprojection.Snapshot {
	t.Helper()
	taggedID, err := conversationprojection.MessageIdentityOf(algNeverBackendMessage())
	require.NoError(t, err)
	// never_backend is enough to take the live Reassert/StageFinal branch.
	// Steering is omitted: current VerifyAdaptation fail-closes when this
	// stable_prefix overlay is observed in Messages rather than Instructions.
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: taggedID, Reason: "test"}},
	}
}

type algCVReader struct {
	snap conversationprojection.Snapshot
}

func (r algCVReader) Snapshot(context.Context, string) (conversationprojection.Snapshot, error) {
	return r.snap, nil
}

type algCVObserver struct {
	log *algOrderLog
}

func (o *algCVObserver) OnProjection(stage string, _ conversationprojection.ProjectionSummary) {
	if stage == conversationprojection.StageFinal {
		o.log.add("stage_final")
	}
}
func (*algCVObserver) OnProjectionFailure(string) {}
func (*algCVObserver) OnAnchorFallback(string, conversationprojection.AnchorMissingPolicy) {
}
func (*algCVObserver) OnAnchorFailure(conversationprojection.AnchorMissingPolicy) {}

type algClampPreviewProbe struct {
	log   *algOrderLog
	clamp int64
}

func (p *algClampPreviewProbe) PreviewAttempt(_ context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	p.log.add("clamp_preview:" + strings.TrimSpace(in.BackendID))
	d := authority.Decision{Kind: authority.DecisionAllow, ProviderID: "alg-clamp-preview"}
	if p.clamp > 0 {
		d.Clamps = []authority.Clamp{{Kind: authority.ClampMaxOutputTokens, Value: p.clamp}}
	}
	return d, nil
}

func (*algClampPreviewProbe) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "alg-clamp-preview"}, nil
}

func (*algClampPreviewProbe) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (*algClampPreviewProbe) ReleaseAttempt(context.Context, authority.AttemptRelease) error {
	return nil
}

// algIngressPreflightProbe is the post-clamp observation of openCall: StoreBackendIngress
// then runPreflight(openCall) run after StageFinal/clamp and before AdaptCallForCandidate.
// CountCall sees the still-unadapted call (Requirement 4.7 / Two-Point Projection).
type algIngressPreflightProbe struct {
	log *algOrderLog
}

func (p algIngressPreflightProbe) CountCall(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	p.log.add("ingress_preflight:" + strings.TrimSpace(in.Backend) + ":" + algLogCallMeta(in.Call))
	return accountingapp.CountResult{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

func algOrderedToolsCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityOrderedItems)
}

func algWireOpenOrderProbes(t *testing.T, ex *runtime.Executor, log *algOrderLog, snap conversationprojection.Snapshot) {
	t.Helper()
	ex.ConversationViewReader = algCVReader{snap: snap}
	ex.ConversationViewObserver = &algCVObserver{log: log}
	ex.AttemptCoordinator = &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID:       "alg-clamp-preview",
			Class:    authoritycoord.AttemptPriorityHardSpend,
			Provider: &algClampPreviewProbe{log: log, clamp: algClampTokens},
			Strength: authority.StrengthRequired,
		}},
	}
	// Secure prepare always installs a metering holder, so openAttemptTx's
	// post-clamp StoreBackendIngress + runPreflight(openCall) path is live.
	ex.Preflight = accountingpreflight.NewChecker(algIngressPreflightProbe{log: log}, accountingpreflight.Config{
		Enabled: true,
		Mode:    accountingpreflight.ModeAdvisory,
	})
}

// algAssertPostClampIngressStillMessageAuthority pins Two-Point Projection:
// conversation-view Reassert/StageFinal runs before AdaptCallForCandidate.
// Post-clamp ingress/preflight is the production observation that still sees
// message-authority openCall; PTB is the first traffic sample of item authority.
// If Adapt moved after post-hook ResolveCaps and before Reassert, this sample
// would be shape=items and these assertions would fail.
func algAssertPostClampIngressStillMessageAuthority(t *testing.T, steps []string, stageFinalIdx, clampIdx, ptbIdx int, ingressPrefix string) {
	t.Helper()
	require.GreaterOrEqual(t, stageFinalIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, clampIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, ptbIdx, 0, "steps=%v", steps)
	ingressIdx := algIndexOfBetween(steps, ingressPrefix, clampIdx, ptbIdx)
	require.GreaterOrEqual(t, ingressIdx, 0, "post-clamp ingress/preflight must observe openCall after StageFinal/clamp and before Adapt/PTB; steps=%v", steps)
	assert.Greater(t, ingressIdx, stageFinalIdx, "post-clamp ingress/preflight must run after StageFinal; steps=%v", steps)
	assert.Greater(t, ingressIdx, clampIdx, "post-clamp ingress/preflight must run after clamp preview; steps=%v", steps)
	assert.Less(t, ingressIdx, ptbIdx, "post-clamp ingress/preflight must run before PTB; steps=%v", steps)
	require.Contains(t, steps[ingressIdx], "shape=messages", "post-clamp ingress/preflight must still see message-authority openCall (AdaptCallForCandidate has not run); steps=%v", steps)
	require.Contains(t, steps[ingressIdx], fmt.Sprintf("max_out=%d", algClampTokens), "post-clamp ingress/preflight must see the applied clamp; steps=%v", steps)
	require.Contains(t, steps[ingressIdx], "never_backend=false", "post-clamp ingress/preflight must run after Reassert filtered never_backend; steps=%v", steps)
	require.Contains(t, steps[ptbIdx], "shape=items", "PTB must observe AdaptCallForCandidate item authority; steps=%v", steps)
}

func algOpenOrderCall(selector, id string) *aipapi.Call {
	call := algWeatherCall(selector)
	call.ID = id
	call.Messages = append(call.Messages, algNeverBackendMessage())
	return call
}

func algLogCallMeta(call aipapi.Call) string {
	return fmt.Sprintf("shape=%s:max_out=%d:steering=%v:never_backend=%v",
		algCallShape(call), algMaxOut(call),
		algCallHasPlainText(call, algSteeringText),
		algCallHasPlainText(call, algNeverBackendText))
}

func algStreamingTransport() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
}

func algSecureExecutor(t *testing.T, backends map[string]execbackend.Backend, bus *hooks.Bus, snapOpts extensions.SnapshotOptions) *runtime.Executor {
	t.Helper()
	ex, _ := interleavedSecureExecutor(t, backends)
	ex.InterleavedProcessor = nil
	if bus != nil {
		ex.Bus = bus
	}
	if snapOpts.Workspace == nil {
		snapOpts.Workspace = voidWorkspaceResolver{}
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	ex.MaxAttempts = 4
	return ex
}

type algAttemptTransformProbe struct {
	log *algOrderLog
}

func (algAttemptTransformProbe) ID() string { return "alg-attempt-transform-probe" }
func (algAttemptTransformProbe) Order() int { return 0 }
func (algAttemptTransformProbe) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (p algAttemptTransformProbe) HandleAttempt(_ context.Context, call *aipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	p.log.add("attempt_transform:" + strings.TrimSpace(meta.BackendID))
	if call != nil && !algCallHasTool(*call, algTransformProbeTool) {
		call.Tools = append(call.Tools, aipapi.ToolDef{Name: algTransformProbeTool, Parameters: []byte(`{}`)})
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type algRequestHookProbe struct {
	log *algOrderLog
}

func (algRequestHookProbe) ID() string                        { return "alg-request-hook-probe" }
func (algRequestHookProbe) Order() int                        { return 0 }
func (algRequestHookProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (p algRequestHookProbe) HandleRequestParts(_ context.Context, call *aipapi.Call, meta sdkhooks.PartMeta) error {
	p.log.add("request_hook:" + strings.TrimSpace(meta.BackendID))
	if call != nil && !algCallHasTool(*call, algHookProbeTool) {
		call.Tools = append(call.Tools, aipapi.ToolDef{Name: algHookProbeTool, Parameters: []byte(`{}`)})
	}
	return nil
}

type algPTBProbe struct {
	log *algOrderLog
}

func (p *algPTBProbe) OnObservation(_ context.Context, ev traffic.Observation) error {
	if ev.Leg == traffic.LegPTB {
		var call aipapi.Call
		meta := "shape=unknown:max_out=-1:steering=false:never_backend=false"
		if err := json.Unmarshal(ev.Body, &call); err == nil {
			meta = algLogCallMeta(call)
		}
		p.log.add("ptb:" + strings.TrimSpace(ev.BackendID) + ":" + meta)
	}
	return nil
}

type algAssemblerProbe struct {
	log *algOrderLog
}

func (algAssemblerProbe) ID() string { return "alg-assembler-probe" }
func (algAssemblerProbe) Order() int { return 0 }

func (p algAssemblerProbe) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	p.log.add("assembler")
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

type algToolPolicyProbe struct {
	log *algOrderLog
}

func (algToolPolicyProbe) ID() string                        { return "alg-tool-policy-probe" }
func (algToolPolicyProbe) Order() int                        { return 0 }
func (algToolPolicyProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (p algToolPolicyProbe) Handle(_ context.Context, event aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	p.log.add("policy:" + string(event.Kind) + ":" + event.ToolName)
	return toolpolicy.DecisionAllow, nil
}

type algToolReactorProbe struct {
	log *algOrderLog
}

func (algToolReactorProbe) ID() string { return "alg-tool-reactor-probe" }
func (algToolReactorProbe) Order() int { return 0 }

func (p algToolReactorProbe) HandleToolEvent(_ context.Context, te aipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	p.log.add("reactor:" + string(te.Kind) + ":" + te.ToolName)
	return sdkhooks.ToolPass, te, nil
}

func algWeatherCall(selector string) *aipapi.Call {
	call := pdBaseCall(selector)
	call.Tools = []aipapi.ToolDef{{
		Name:       "get_weather",
		Parameters: []byte(`{"type":"object"}`),
	}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	call.Invocation = aipapi.Invocation{
		Operation:    aipapi.OperationOpenAIChatCompletions,
		DeliveryMode: aipapi.DeliveryModeStreaming,
	}
	return call
}

func algCollectEOF(t *testing.T, stream aipapi.EventStream) []aipapi.Event {
	t.Helper()
	var out []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			return out
		}
		out = append(out, ev)
	}
}

// TestCandidateOpenOrdering_transformHooksPostHookCapsPTBThenOpen pins the
// current two-point candidate-open sequence (Requirement 4.7 / design Two-Point
// Projection and Existing Architecture):
//
//	attempt transforms
//	  -> pre-hook capability/admission (call already has transform mutation)
//	  -> request-part hooks
//	  -> post-hook capability/admission (call has hook mutation)
//	  -> conversation-view Reassert / StageFinal
//	  -> clamp preview
//	  -> backend-ingress freeze + post-clamp preflight (still message authority)
//	  -> AdaptCallForCandidate
//	  -> PTB
//	  -> Backend.Open
func TestCandidateOpenOrdering_transformHooksPostHookCapsPTBThenOpen(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	var opened atomic.Int32
	var openCall atomic.Pointer[aipapi.Call]
	caps := algOrderedToolsCaps()
	backend := execbackend.Backend{
		Caps:                    caps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add(fmt.Sprintf("resolve_caps:%s:transformed=%v:hooked=%v:%s",
				cand.Primary.Backend, algCallHasTool(call, algTransformProbeTool), algCallHasTool(call, algHookProbeTool),
				algLogCallMeta(call)))
			return caps
		},
		Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			log.add("open:" + cand.Primary.Backend + ":" + algLogCallMeta(call))
			opened.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := algSecureExecutor(t, map[string]execbackend.Backend{"openai": backend}, hooks.New(hooks.Config{
		RequestPartHooks: []sdkhooks.RequestPartHook{algRequestHookProbe{log: log}},
	}), extensions.SnapshotOptions{
		TrafficObserver: &algPTBProbe{log: log},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{algAttemptTransformProbe{log: log}},
		}),
	})
	algWireOpenOrderProbes(t, ex, log, algLiveConversationSnapshot(t))

	stream, err := ex.Execute(principalCtx("alg-open-order"), algOpenOrderCall("openai:gpt-4", "alg-open-order"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), opened.Load())
	steps := log.snapshot()
	require.NotEmpty(t, steps, "expected ordering probes")

	transformIdx := algIndexOf(steps, "attempt_transform:")
	hookIdx := algIndexOf(steps, "request_hook:")
	stageFinalIdx := algIndexOf(steps, "stage_final")
	clampIdx := algIndexOf(steps, "clamp_preview:")
	ptbIdx := algIndexOf(steps, "ptb:")
	openIdx := algIndexOf(steps, "open:")
	require.GreaterOrEqual(t, transformIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, hookIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, stageFinalIdx, 0, "live conversation-view Reassert must emit StageFinal; steps=%v", steps)
	require.GreaterOrEqual(t, clampIdx, 0, "clamp preview must run after Reassert; steps=%v", steps)
	require.GreaterOrEqual(t, ptbIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, openIdx, 0, "steps=%v", steps)
	assert.Less(t, transformIdx, hookIdx, "attempt transform must precede request hooks; steps=%v", steps)
	assert.Less(t, hookIdx, ptbIdx, "request hooks must precede PTB; steps=%v", steps)
	assert.Less(t, ptbIdx, openIdx, "PTB must precede Backend.Open; steps=%v", steps)

	unhookedIdx, hookedIdx := -1, -1
	for i, step := range steps {
		if strings.Contains(step, "hooked=false") && unhookedIdx < 0 {
			unhookedIdx = i
		}
		if strings.Contains(step, "hooked=true") && hookedIdx < 0 {
			hookedIdx = i
		}
	}
	require.GreaterOrEqual(t, unhookedIdx, 0, "pre-hook admission must see the call before request hooks; steps=%v", steps)
	require.GreaterOrEqual(t, hookedIdx, 0, "post-hook rederive must see hook mutation; steps=%v", steps)
	assert.Less(t, unhookedIdx, hookIdx, "pre-hook ResolveCaps must run before request hooks; steps=%v", steps)
	assert.Greater(t, hookedIdx, hookIdx, "post-hook ResolveCaps must run after request hooks; steps=%v", steps)
	assert.Less(t, hookedIdx, ptbIdx, "post-hook rederive must finish before PTB; steps=%v", steps)
	assert.Less(t, hookedIdx, openIdx, "post-hook rederive must finish before Backend.Open; steps=%v", steps)
	assert.Less(t, hookedIdx, stageFinalIdx, "StageFinal/Reassert must run after post-hook ResolveCaps; steps=%v", steps)
	assert.Less(t, stageFinalIdx, clampIdx, "clamp preview must run after StageFinal/Reassert; steps=%v", steps)
	assert.Less(t, clampIdx, ptbIdx, "clamp preview must run before PTB; steps=%v", steps)
	require.Contains(t, steps[unhookedIdx], "transformed=true", "evaluateCandidate admission must see attempt-transform mutation")
	require.Contains(t, steps[unhookedIdx], "shape=messages", "pre-adapt ResolveCaps must see legacy message authority")
	require.Contains(t, steps[hookedIdx], "shape=messages", "post-hook ResolveCaps must still be pre-adapt")
	require.Contains(t, steps[hookedIdx], "max_out=-1", "post-hook ResolveCaps must run before clamp preview")
	algAssertPostClampIngressStillMessageAuthority(t, steps, stageFinalIdx, clampIdx, ptbIdx, "ingress_preflight:")
	require.Contains(t, steps[ptbIdx], fmt.Sprintf("max_out=%d", algClampTokens), "PTB must observe the post-Reassert clamp")
	require.Contains(t, steps[openIdx], "shape=items", "Backend.Open must receive the adapted item-authority call")
	require.Contains(t, steps[openIdx], fmt.Sprintf("max_out=%d", algClampTokens), "Backend.Open must observe the post-Reassert clamp")

	got := openCall.Load()
	require.NotNil(t, got)
	assert.True(t, algCallHasTool(*got, algTransformProbeTool))
	assert.True(t, algCallHasTool(*got, algHookProbeTool), "Backend.Open must receive the post-hook backend-effective call")
	assert.True(t, got.HasItemAuthority(), "AdaptCallForCandidate must project before Open")
	assert.False(t, algCallHasPlainText(*got, algNeverBackendText), "Open must not receive never_backend content after Reassert")
	require.NotNil(t, got.Options.MaxOutputTokens)
	assert.Equal(t, algClampTokens, *got.Options.MaxOutputTokens)
}

// TestCandidateOpenOrdering_failoverRecomputesCapsPerCandidate pins Requirement 4.7:
// a replacement candidate before commitment re-runs transform/admission against
// THAT candidate's ResolveCaps and is excluded when it cannot satisfy the
// already-required tools call. A viable failover candidate re-runs request
// hooks, post-hook rederive, conversation-view StageFinal, PTB, and Open.
func TestCandidateOpenOrdering_failoverRecomputesCapsPerCandidate(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	var openedWithTools, openedNoTools, openedViable atomic.Int32
	orderedCaps := algOrderedToolsCaps()
	withTools := execbackend.Backend{
		Caps:                    orderedCaps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add(fmt.Sprintf("resolve_caps:%s:tools=%v:call_tools=%d:%s",
				cand.Primary.Backend, true, len(call.Tools), algLogCallMeta(call)))
			return orderedCaps
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openedWithTools.Add(1)
			log.add("open:withtools")
			return nil, aipapi.RecoverablePreOutputError(fmt.Errorf("primary open failed"))
		},
	}
	noTools := execbackend.Backend{
		Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: algStreamingTransport(),
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add(fmt.Sprintf("resolve_caps:%s:tools=%v:call_tools=%d:%s",
				cand.Primary.Backend, false, len(call.Tools), algLogCallMeta(call)))
			return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openedNoTools.Add(1)
			log.add("open:notools")
			return nil, fmt.Errorf("no-tools backend must not open")
		},
	}
	viable := execbackend.Backend{
		Caps:                    orderedCaps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add(fmt.Sprintf("resolve_caps:%s:tools=%v:call_tools=%d:hooked=%v:%s",
				cand.Primary.Backend, true, len(call.Tools), algCallHasTool(call, algHookProbeTool), algLogCallMeta(call)))
			return orderedCaps
		},
		Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openedViable.Add(1)
			log.add("open:viable:" + algLogCallMeta(call))
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := algSecureExecutor(t, map[string]execbackend.Backend{
		"withtools": withTools,
		"notools":   noTools,
		"viable":    viable,
	}, hooks.New(hooks.Config{
		RequestPartHooks: []sdkhooks.RequestPartHook{algRequestHookProbe{log: log}},
	}), extensions.SnapshotOptions{
		TrafficObserver: &algPTBProbe{log: log},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			AttemptTransforms: []request.AttemptTransform{algAttemptTransformProbe{log: log}},
		}),
	})
	algWireOpenOrderProbes(t, ex, log, algLiveConversationSnapshot(t))

	stream, err := ex.Execute(principalCtx("alg-failover-caps"), algOpenOrderCall("withtools:m|notools:m|viable:m", "alg-failover-caps"))
	require.NoError(t, err, "viable failover candidate must open after the no-tools candidate is excluded")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)
	assert.Equal(t, int32(1), openedWithTools.Load(), "tools-capable primary must be attempted")
	assert.Equal(t, int32(0), openedNoTools.Load(), "capability recompute must exclude the no-tools candidate before Open")
	assert.Equal(t, int32(1), openedViable.Load(), "viable failover candidate must open")

	steps := log.snapshot()
	assert.GreaterOrEqual(t, algIndexOf(steps, "attempt_transform:withtools"), 0, "steps=%v", steps)
	assert.GreaterOrEqual(t, algIndexOf(steps, "attempt_transform:notools"), 0, "failover must re-run attempt transforms for the replacement candidate; steps=%v", steps)
	assert.GreaterOrEqual(t, algIndexOf(steps, "resolve_caps:notools:"), 0, "failover must re-run capability admission for the replacement candidate; steps=%v", steps)
	assert.Equal(t, -1, algIndexOf(steps, "request_hook:notools"), "excluded no-tools candidate must not reach request hooks/Open; steps=%v", steps)

	viableHookIdx := algIndexOf(steps, "request_hook:viable")
	viableHookedCapsIdx := -1
	for i, step := range steps {
		if strings.HasPrefix(step, "resolve_caps:viable:") && strings.Contains(step, "hooked=true") {
			viableHookedCapsIdx = i
			break
		}
	}
	viableClampIdx := algIndexOf(steps, "clamp_preview:viable")
	viablePTBIdx := algIndexOf(steps, "ptb:viable:")
	viableOpenIdx := algIndexOf(steps, "open:viable:")
	require.GreaterOrEqual(t, viableHookIdx, 0, "viable failover must re-run request hooks; steps=%v", steps)
	require.GreaterOrEqual(t, viableHookedCapsIdx, 0, "viable failover must re-run post-hook rederive; steps=%v", steps)
	require.GreaterOrEqual(t, viableClampIdx, 0, "viable failover must re-run clamp preview; steps=%v", steps)
	require.GreaterOrEqual(t, viablePTBIdx, 0, "viable failover must re-run PTB; steps=%v", steps)
	require.GreaterOrEqual(t, viableOpenIdx, 0, "viable failover must Open; steps=%v", steps)
	viableStageFinalIdx := algIndexOfExactBetween(steps, "stage_final", viableHookedCapsIdx, viableClampIdx)
	require.GreaterOrEqual(t, viableStageFinalIdx, 0, "viable replacement candidate must re-run StageFinal after ITS post-hook ResolveCaps and before ITS clamp; steps=%v", steps)
	assert.Less(t, viableHookIdx, viableHookedCapsIdx, "viable post-hook rederive must follow request hooks; steps=%v", steps)
	assert.Less(t, viableHookedCapsIdx, viableStageFinalIdx, "viable StageFinal must follow that candidate's post-hook ResolveCaps; steps=%v", steps)
	assert.Less(t, viableStageFinalIdx, viableClampIdx, "viable clamp must follow that candidate's StageFinal; steps=%v", steps)
	assert.Less(t, viableClampIdx, viablePTBIdx, "viable PTB must follow clamp; steps=%v", steps)
	assert.Less(t, viablePTBIdx, viableOpenIdx, "viable Open must follow PTB; steps=%v", steps)
	require.Contains(t, steps[viableHookedCapsIdx], "shape=messages")
	algAssertPostClampIngressStillMessageAuthority(t, steps, viableStageFinalIdx, viableClampIdx, viablePTBIdx, "ingress_preflight:viable:")
	require.Contains(t, steps[viableOpenIdx], "shape=items")
	stageFinalCount := 0
	for _, step := range steps {
		if step == "stage_final" {
			stageFinalCount++
		}
	}
	require.GreaterOrEqual(t, stageFinalCount, 2, "each opened candidate must re-run conversation-view StageFinal; steps=%v", steps)
}

// TestOrdinaryStreaming_textDeltaReleasedBeforeBackendFinish pins Requirement 5.1:
// with no completion gate installed, Recv returns ordinary text as it arrives and
// does not wait for backend ResponseFinished (no whole-response buffer).
func TestOrdinaryStreaming_textDeltaReleasedBeforeBackendFinish(t *testing.T) {
	t.Parallel()

	backendStream := newPushManagedStream()
	t.Cleanup(func() { backendStream.ClosePush() })
	ex := algSecureExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: algStreamingTransport(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}, nil, extensions.SnapshotOptions{})

	stream, err := ex.Execute(principalCtx("alg-stream"), pdBaseCall("openai:gpt-4"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	backendStream.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
	backendStream.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	backendStream.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello-before-finish"})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var gotText bool
	for !gotText {
		ev, recvErr := stream.Recv(ctx)
		require.NoError(t, recvErr, "ordinary text must be observable before backend finish")
		if ev.Kind == aipapi.EventTextDelta {
			assert.Equal(t, "hello-before-finish", ev.Delta)
			gotText = true
		}
	}

	backendStream.Push(aipapi.Event{Kind: aipapi.EventResponseFinished})
	backendStream.ClosePush()
	for {
		_, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			require.ErrorIs(t, recvErr, io.EOF)
			return
		}
	}
}

// TestToolResponseOrdering_assemblerThenPolicyThenReactor pins Requirement 5.4
// and design Response Interception Placement: completed-call assembler/finalizers
// run before ordinary tool policy, which runs before tool reactors. Later
// proxy-owned control capture must sit before this policy/reactor pair.
func TestToolResponseOrdering_assemblerThenPolicyThenReactor(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"location":"NYC"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "get_weather"},
		{Kind: aipapi.EventResponseFinished},
	})
	ex := algSecureExecutor(t, map[string]execbackend.Backend{
		"openai": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: algStreamingTransport(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}, hooks.New(hooks.Config{
		ToolReactors: []sdkhooks.ToolReactor{algToolReactorProbe{log: log}},
	}), extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies:                 []toolpolicy.Policy{algToolPolicyProbe{log: log}},
			ToolCallFinalizers:               []toolcall.Finalizer{algAssemblerProbe{log: log}},
			ToolCallFinalizationMaxArgsBytes: 64 * 1024,
		}),
	})

	stream, err := ex.Execute(principalCtx("alg-tool-order"), algWeatherCall("openai:gpt-4"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	steps := log.snapshot()
	assemblerIdx := algIndexOf(steps, "assembler")
	policyIdx := algIndexOf(steps, "policy:")
	reactorIdx := algIndexOf(steps, "reactor:")
	require.GreaterOrEqual(t, assemblerIdx, 0, "assembler/finalizer must run; steps=%v", steps)
	require.GreaterOrEqual(t, policyIdx, 0, "tool policy must run; steps=%v", steps)
	require.GreaterOrEqual(t, reactorIdx, 0, "tool reactor must run; steps=%v", steps)
	assert.Less(t, assemblerIdx, policyIdx, "assembler must precede tool policy; steps=%v", steps)
	assert.Less(t, policyIdx, reactorIdx, "tool policy must precede tool reactors; steps=%v", steps)
}

// TestCommitmentChokepoint_noFailoverAfterClientVisibleText pins Requirement 8.7:
// after the first client-visible text delta, transparent failover/retry is denied.
func TestCommitmentChokepoint_noFailoverAfterClientVisibleText(t *testing.T) {
	t.Parallel()

	var secondary atomic.Int32
	ex := algSecureExecutor(t, map[string]execbackend.Backend{
		"one": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: algStreamingTransport(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &deltaThenErrStream{}, nil
			},
		},
		"two": {
			Caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: algStreamingTransport(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				secondary.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}, nil, extensions.SnapshotOptions{})

	stream, err := ex.Execute(principalCtx("alg-commit"), pdBaseCall("one:m|two:m"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })

	var sawText bool
	var terminalErr error
	for {
		ev, recvErr := stream.Recv(context.Background())
		if recvErr != nil {
			terminalErr = recvErr
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
		}
	}
	require.True(t, sawText, "client-visible text must commit before the stream error")
	require.Error(t, terminalErr)
	assert.False(t, aipapi.IsRecoverablePreOutput(terminalErr), "post-output failure must not remain recoverable for retry")
	assert.Equal(t, int32(0), secondary.Load(), "secondary backend must not open after first client-visible output")
}
