// Runtime slice for tasks 3.1/3.2 of agent-loop-explicit-completion-protocol
// (spec: .kiro/specs/agent-loop-explicit-completion-protocol, requirements 3.1,
// 3.2, 3.3, 4.1-4.7, 7.6, 10.3-10.6, 11.3, 12.1, 12.2, 12.4, 12.6).
//
// Task 3.1 froze these as a behavioral RED checkpoint behind the temporary
// controltool_red build tag. Task 3.2 implements the generic bounded
// control-projection stage plus the attempt-local activation owner and removed
// the tag, so every acceptance assertion below now runs in the normal build.
//
// Generic runtime only. This file names no concrete feature: the fake
// provider is an anonymous `proxy_control` model control tool contributed
// through feature.PlaneControlToolProvider, exactly like any feature
// generation would contribute it.
package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// The generic control contract used by every case below. Name, schema,
// instruction text, and args budget are plain-spaced, byte-stable, and
// provider-owned; the runtime must never rewrite them.
const (
	algControlProviderID   = "generic-control-test"
	algControlToolName     = "proxy_control"
	algControlToolDesc     = "Call this only when the assigned proxy-local control action is complete."
	algControlInstruction  = "Call proxy_control only when the assigned proxy-local control action is complete."
	algControlSchema       = `{"type":"object","properties":{"note":{"type":"string","description":"Bounded control result."}},"required":["note"],"additionalProperties":false}`
	algWeatherToolName     = "get_weather"
	algWeatherToolSchema   = `{"type":"object"}`
	algControlBackendID    = "openai"
	algControlBackendModel = "gpt-4"
)

// algFakeControlProvider is the generation-admitted generic control provider.
// Composition validates ID+Spec, so the request-time counters are reset after
// composition and then prove that the candidate-open path reads the frozen
// contract without ever running the control handler.
type algFakeControlProvider struct {
	id   string
	spec controltool.Spec

	mu      sync.Mutex
	handled int
}

func (p *algFakeControlProvider) ID() string { return p.id }

func (p *algFakeControlProvider) Spec() controltool.Spec {
	spec := p.spec
	// Hand out an independent schema copy so a runtime that adopts the spec
	// cannot reach the fixture by slice aliasing.
	spec.Tool.Parameters = json.RawMessage(algControlSchema)
	return spec
}

// Handle is out of scope for this slice: no candidate-open behavior may invoke
// it. A call here is recorded and answered with a bounded invalid outcome so a
// future stage that reaches it fails its own counter assertion loudly.
func (p *algFakeControlProvider) Handle(_ context.Context, _ controltool.CompletedCall, _ controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handled++
	return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "control_handler_reached"}, nil
}

// resetRequestCounters rebases the request-time counter after composition
// validation so a later assertion observes only request-time calls.
func (p *algFakeControlProvider) resetRequestCounters() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handled = 0
}

func (p *algFakeControlProvider) handledCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handled
}

func algControlTestSpec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        algControlToolName,
			Description: algControlToolDesc,
			Parameters:  json.RawMessage(algControlSchema),
		},
		Instruction: controltool.Instruction{
			Role: aipapi.RoleSystem,
			Text: algControlInstruction,
		},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

// algControlTestProvider composes the generic provider the way a feature
// generation does (validate identity plus frozen spec) and then rebases the
// method counters so only request-time calls are observed.
func algControlTestProvider(t *testing.T) *algFakeControlProvider {
	t.Helper()
	provider := &algFakeControlProvider{id: algControlProviderID, spec: algControlTestSpec()}
	require.NoError(t, controltool.ValidateProvider(provider), "generation composition must accept the generic control spec")
	provider.resetRequestCounters()
	return provider
}

// algControlPlanes builds the request-time feature plane set. A nil provider
// contributes nothing, which is the ordinary zero-work no-op generation.
func algControlPlanes(t *testing.T, provider controltool.Provider) aipfeature.FrozenPlaneSet {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	if provider != nil {
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, algControlProviderID, provider))
	}
	return cs.Freeze()
}

func algCountToolDef(call aipapi.Call, name string) int {
	count := 0
	for _, tool := range call.Tools {
		if tool.Name == name {
			count++
		}
	}
	return count
}

func algCountPlainText(call aipapi.Call, text string) int {
	count := 0
	for _, m := range call.Instructions {
		for _, p := range m.Parts {
			if p.Text == text {
				count++
			}
		}
	}
	for _, m := range call.Messages {
		for _, p := range m.Parts {
			if p.Text == text {
				count++
			}
		}
	}
	for _, it := range call.Items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		for _, p := range it.Content {
			if p.Text == text {
				count++
			}
		}
	}
	return count
}

func algToolNames(tools []aipapi.ToolDef) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func algControlChoiceOf(call aipapi.Call) string {
	return fmt.Sprintf("choice=%s/%s/allowed=%d",
		orDefault(string(call.ToolChoice.Mode)),
		orDefault(call.ToolChoice.Name),
		len(call.ToolChoice.AllowedTools))
}

func orDefault(value string) string {
	if value == "" {
		return "omitted"
	}
	return value
}

// algControlMeta is the bounded, content-free observation every probe records.
// It never carries instruction text, only presence counts.
func algControlMeta(call aipapi.Call) string {
	return fmt.Sprintf("shape=%s:control_tools=%d:control_text=%d:tools=%d:%s",
		algCallShape(call),
		algCountToolDef(call, algControlToolName),
		algCountPlainText(call, algControlInstruction),
		len(call.Tools),
		algControlChoiceOf(call))
}

func algLastIndexOf(steps []string, prefix string) int {
	for i := range slices.Backward(steps) {
		if strings.HasPrefix(steps[i], prefix) {
			return i
		}
	}
	return -1
}

func algFirstIndexAfter(steps []string, prefix string, after int) int {
	for i := after + 1; i < len(steps); i++ {
		if strings.HasPrefix(steps[i], prefix) {
			return i
		}
	}
	return -1
}

// algControlCallSample is one owned observation of a canonical call.
type algControlCallSample struct {
	backend string
	call    aipapi.Call
}

// algControlCountProbe is the authoritative token-accounting preflight. It
// records every call it is asked to price so the post-hook CountCall can be
// inspected as a real call, not as a formatting string (Requirement 11.3 and
// the design Accounting/Context section: the control tool plus instruction are
// real provider input for sizing and accounting).
type algControlCountProbe struct {
	log *algOrderLog

	mu      sync.Mutex
	samples []algControlCallSample
}

func (p *algControlCountProbe) CountCall(_ context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	cloned := aipapi.CloneCall(in.Call)
	p.mu.Lock()
	p.samples = append(p.samples, algControlCallSample{backend: strings.TrimSpace(in.Backend), call: cloned})
	p.mu.Unlock()
	p.log.add("count_call:" + strings.TrimSpace(in.Backend) + ":" + algControlMeta(cloned))
	return accountingapp.CountResult{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

func (p *algControlCountProbe) ordered() []algControlCallSample {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]algControlCallSample, len(p.samples))
	copy(out, p.samples)
	return out
}

// algControlTrafficSample is one owned decoded traffic body.
type algControlTrafficSample struct {
	backend string
	body    []byte
	call    aipapi.Call
	decoded bool
}

// algControlTrafficProbe captures the A-leg (CTP) and B-leg (PTB) canonical
// request bodies so the tests can prove the control contract never reaches
// client truth while it does reach the backend.
type algControlTrafficProbe struct {
	log *algOrderLog

	mu      sync.Mutex
	samples map[sdktraffic.Leg][]algControlTrafficSample
}

func newAlgControlTrafficProbe(log *algOrderLog) *algControlTrafficProbe {
	return &algControlTrafficProbe{log: log, samples: map[sdktraffic.Leg][]algControlTrafficSample{}}
}

func (p *algControlTrafficProbe) OnObservation(_ context.Context, ev sdktraffic.Observation) error {
	if ev.Leg != sdktraffic.LegCTP && ev.Leg != sdktraffic.LegPTB {
		return nil
	}
	sample := algControlTrafficSample{backend: strings.TrimSpace(ev.BackendID), body: append([]byte(nil), ev.Body...)}
	var call aipapi.Call
	if err := json.Unmarshal(ev.Body, &call); err == nil {
		sample.call = call
		sample.decoded = true
	}
	p.mu.Lock()
	p.samples[ev.Leg] = append(p.samples[ev.Leg], sample)
	p.mu.Unlock()
	meta := "shape=unknown:control_tools=?:control_text=?"
	if sample.decoded {
		meta = algControlMeta(call)
	}
	p.log.add(string(ev.Leg) + ":" + strings.TrimSpace(ev.BackendID) + ":" + meta)
	return nil
}

func (p *algControlTrafficProbe) first(leg sdktraffic.Leg) (algControlTrafficSample, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	got := p.samples[leg]
	if len(got) == 0 {
		return algControlTrafficSample{}, false
	}
	return got[0], true
}

func (p *algControlTrafficProbe) count(leg sdktraffic.Leg) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.samples[leg])
}

type algControlHarness struct {
	log      *algOrderLog
	provider *algFakeControlProvider
	traffic  *algControlTrafficProbe
	counter  *algControlCountProbe
	ex       *runtime.Executor
	opened   *atomic.Int32
	openCall *atomic.Pointer[aipapi.Call]
}

// algControlEligibleHarness wires the shared secure executor with the live
// conversation-view reader/observer and clamp preview, adds a request-part
// hook that mutates the candidate before the projection point, and records
// capability resolution, accounting, and both request traffic legs.
func algControlEligibleHarness(t *testing.T, caps aipapi.BackendCaps, snap conversationprojection.Snapshot) algControlHarness {
	t.Helper()
	log := &algOrderLog{}
	provider := algControlTestProvider(t)
	trafficProbe := newAlgControlTrafficProbe(log)
	counter := &algControlCountProbe{log: log}
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}

	backend := execbackend.Backend{
		Caps:                    caps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add("resolve_caps:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(call))
			return caps
		},
		Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			opened.Add(1)
			log.add("open:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(call))
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := algSecureExecutor(t, map[string]execbackend.Backend{algControlBackendID: backend},
		hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{algRequestHookProbe{log: log}}}),
		extensions.SnapshotOptions{
			TrafficObserver: trafficProbe,
			FeaturePlanes:   algControlPlanes(t, provider),
		})
	require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the request snapshot must expose the admitted generic control provider")
	algWireOpenOrderProbes(t, ex, log, snap)
	// Replace the shared ingress preflight so every accounting observation is
	// an owned, orderable call sample instead of a formatted log line.
	ex.Preflight = accountingpreflight.NewChecker(counter, accountingpreflight.Config{
		Enabled: true,
		Mode:    accountingpreflight.ModeAdvisory,
	})

	return algControlHarness{
		log:      log,
		provider: provider,
		traffic:  trafficProbe,
		counter:  counter,
		ex:       ex,
		opened:   opened,
		openCall: openCall,
	}
}

func algRunControlRequest(t *testing.T, h algControlHarness, ctxID string, call *aipapi.Call) {
	t.Helper()
	stream, err := h.ex.Execute(principalCtx(ctxID), call)
	require.NoError(t, err, "an eligible or ineligible control candidate must stay usable")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)
}

func algControlMessageCall(selector, id string) *aipapi.Call {
	call := algWeatherCall(selector)
	call.ID = id
	call.Messages = append(call.Messages, algNeverBackendMessage())
	return call
}

func algControlSystemMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(algControlInstruction)}}
}

func algControlUserItem(id, text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		ID:      id,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}

// algControlItemCall is the item-authority twin: the control instruction must
// be materialized as a leading system item instead of an instruction message.
func algControlItemCall(selector, id string) *aipapi.Call {
	call := algWeatherCall(selector)
	call.ID = id
	call.Messages = nil
	call.Items = []aipapi.Item{
		algControlUserItem("msg-user-1", "report the weather"),
		algControlUserItem("msg-user-2", algNeverBackendText),
	}
	return call
}

func algControlItemSnapshot(t *testing.T) conversationprojection.Snapshot {
	t.Helper()
	taggedID, err := conversationprojection.ItemIdentityOf(algControlUserItem("msg-user-2", algNeverBackendText))
	require.NoError(t, err)
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: taggedID, Reason: "test"}},
	}
}

// TestControlToolProjection_postHookPreflightAndOpenSeeOneApprovedMessageAuthorityProjection
// is the task 3.1 acceptance case for a message-authority candidate on a
// tools-capable ordered-items backend with the live conversation view.
//
// It pins design Two-Point Projection point 1 (post-hook projection) and point
// 2 (final reassertion after conversation-view StageFinal):
//   - pre-hook probes (ResolveCaps, CountCall) naturally see no control
//     content, because the projection point is after ordinary request mutation;
//   - the authoritative post-hook capability rederive and post-hook accounting
//     preflight see the projected call, not merely Backend.Open;
//   - the live never_backend StageFinal reconstruction still leaves exactly one
//     byte-identical control instruction and one control tool in the adapted
//     backend-effective call;
//   - A-leg client truth (CTP) never gains the tool or the instruction, while
//     B-leg traffic (PTB) contains both;
//   - the client's tool order and tool choice are preserved and no serialized
//     control provenance is introduced;
//   - the control handler is never invoked on the request path.
func TestControlToolProjection_postHookPreflightAndOpenSeeOneApprovedMessageAuthorityProjection(t *testing.T) {
	t.Parallel()

	h := algControlEligibleHarness(t, algOrderedToolsCaps(), algLiveConversationSnapshot(t))
	call := algControlMessageCall(algControlBackendID+":"+algControlBackendModel, "control-message-authority")
	clientChoice := call.ToolChoice
	clientToolNames := algToolNames(call.Tools)

	algRunControlRequest(t, h, "control-msg-authority", call)

	require.Equal(t, int32(1), h.opened.Load(), "exactly one candidate must reach Backend.Open")
	steps := h.log.snapshot()
	require.NotEmpty(t, steps, "expected candidate-open ordering probes")

	hookIdx := algIndexOf(steps, "request_hook:")
	stageFinalIdx := algIndexOf(steps, "stage_final")
	ptbIdx := algIndexOf(steps, string(sdktraffic.LegPTB)+":")
	openIdx := algIndexOf(steps, "open:")
	require.GreaterOrEqual(t, hookIdx, 0, "the request-part hook must run; steps=%v", steps)
	require.GreaterOrEqual(t, stageFinalIdx, 0, "live conversation-view Reassert must emit StageFinal; steps=%v", steps)
	require.GreaterOrEqual(t, ptbIdx, 0, "B-leg request traffic must be captured; steps=%v", steps)
	require.GreaterOrEqual(t, openIdx, 0, "steps=%v", steps)
	assert.Less(t, hookIdx, ptbIdx, "request hooks must precede B-leg traffic; steps=%v", steps)
	assert.Less(t, ptbIdx, openIdx, "B-leg traffic must precede Backend.Open; steps=%v", steps)

	// Point 1 predecessor: pre-hook probes cannot see the projection.
	preHookResolve := algIndexOf(steps, "resolve_caps:")
	require.GreaterOrEqual(t, preHookResolve, 0, "capability admission must run; steps=%v", steps)
	assert.Less(t, preHookResolve, hookIdx, "pre-hook capability admission must run before request hooks; steps=%v", steps)
	assert.Contains(t, steps[preHookResolve], "control_tools=0", "pre-hook capability admission runs before the projection point; steps=%v", steps)
	assert.Contains(t, steps[preHookResolve], "control_text=0", "pre-hook capability admission runs before the projection point; steps=%v", steps)

	counted := h.counter.ordered()
	require.NotEmpty(t, counted, "accounting preflight must run; steps=%v", steps)
	assert.Zero(t, algCountToolDef(counted[0].call, algControlToolName), "pre-hook accounting sees the call before the projection point")
	assert.Zero(t, algCountPlainText(counted[0].call, algControlInstruction), "pre-hook accounting sees the call before the projection point")
	preHookCount := algIndexOf(steps, "count_call:")
	require.GreaterOrEqual(t, preHookCount, 0, "steps=%v", steps)
	assert.Less(t, preHookCount, hookIdx, "pre-hook accounting must run before request hooks; steps=%v", steps)

	// Point 1: the authoritative post-hook rederive observes the projection.
	postHookResolve := algLastIndexOf(steps, "resolve_caps:")
	require.Greater(t, postHookResolve, hookIdx, "the post-hook capability rederive must run after request hooks; steps=%v", steps)
	assert.Less(t, postHookResolve, stageFinalIdx, "the post-hook capability rederive must run before conversation-view StageFinal; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_tools=1",
		"the post-hook capability rederive must see the projected control tool; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_text=1",
		"the post-hook capability rederive must see the projected control instruction; steps=%v", steps)

	postHookCount := algFirstIndexAfter(steps, "count_call:", hookIdx)
	require.GreaterOrEqual(t, postHookCount, 0, "post-hook accounting preflight must run; steps=%v", steps)
	assert.Less(t, postHookCount, stageFinalIdx, "post-hook accounting preflight must run before conversation-view StageFinal; steps=%v", steps)
	assert.Contains(t, steps[postHookCount], "control_tools=1",
		"the post-hook accounting preflight must see the projected control tool, not only Backend.Open; steps=%v", steps)
	assert.Contains(t, steps[postHookCount], "control_text=1",
		"the post-hook accounting preflight must see the projected control instruction; steps=%v", steps)
	if assert.Greater(t, len(counted), 1, "post-hook accounting must run a second preflight; steps=%v", steps) {
		postHookCounted := counted[len(counted)-1]
		assert.Equal(t, 1, algCountToolDef(postHookCounted.call, algControlToolName),
			"the post-hook accounting call must carry exactly one control tool")
		assert.Equal(t, 1, algCountPlainText(postHookCounted.call, algControlInstruction),
			"the post-hook accounting call must carry exactly one control instruction")
	}

	// Point 2: the final canonical reconstruction keeps the approved bytes.
	got := h.openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	require.NoError(t, got.Validate(), "the backend-effective call must stay canonically valid")
	require.True(t, got.HasItemAuthority(), "ordered-items caps must still adapt the call to item authority before Open")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName), "exactly one control tool may reach the backend")
	assert.Equal(t, 1, algCountPlainText(*got, algControlInstruction), "exactly one control instruction may reach the backend")
	assert.Zero(t, algCountPlainText(*got, algNeverBackendText), "the final conversation-view reassertion must still drop never_backend content")

	require.NotEmpty(t, got.Items, "the adapted call must carry items")
	head := got.Items[0]
	assert.Equal(t, aipapi.ItemKindMessage, head.Kind, "the control instruction must be the leading materialized item")
	assert.Equal(t, aipapi.RoleSystem, head.Role, "the control instruction must keep its approved role")
	if assert.Len(t, head.Content, 1, "the control instruction item must carry exactly one content part") {
		assert.Equal(t, algControlInstruction, head.Content[0].Text, "the control instruction bytes must survive the final reassertion exactly")
	}

	// Bind the effective call to the SDK-approved projection, not to a copy of
	// the fixture, so task 3.2 cannot satisfy the runtime with different bytes.
	_, approved, err := controltool.Project(aipapi.CloneCall(*call), algControlTestSpec(), algOrderedToolsCaps())
	require.NoError(t, err, "the message-authority fixture must be a valid control spec")
	require.True(t, approved.Active(), "the message-authority fixture must be eligible for the approved control spec")

	require.NotEmpty(t, got.Tools, "the backend-effective catalog must not be empty")
	tail := got.Tools[len(got.Tools)-1]
	assert.Equal(t, approved.Tool(), tail, "Backend.Open must receive the byte-identical approved control tool definition")
	assert.Equal(t, algControlToolName, tail.Name, "the control tool must be the final append")
	assert.Equal(t, algControlToolDesc, tail.Description, "the control tool description must be byte-identical to the approved spec")
	assert.Equal(t, algControlSchema, string(tail.Parameters), "the control tool schema bytes must be byte-identical to the approved spec")
	ordinary := got.Tools[:len(got.Tools)-1]
	wantNames := append([]string(nil), clientToolNames...)
	wantNames = append(wantNames, algHookProbeTool)
	assert.Equal(t, wantNames, algToolNames(ordinary), "the client tool order must be preserved ahead of the appended control tool")
	assert.Equal(t, algWeatherToolSchema, string(ordinary[0].Parameters), "the client tool schema bytes must be unchanged")
	assert.Equal(t, clientChoice, got.ToolChoice, "the client tool choice must never be rewritten")
	assert.Equal(t, approved.ToolChoice(), got.ToolChoice, "the runtime must never rewrite the approved client tool choice")
	assert.Equal(t, controltool.DefaultMaxArgsBytes, approved.MaxArgsBytes(),
		"the approved control args budget is the 64 KiB tool-call envelope")

	assert.Len(t, got.Extensions, len(call.Extensions), "control activation must not serialize provenance into canonical call extensions")
	assert.Len(t, got.SemanticExtensions, len(call.SemanticExtensions), "control activation must not serialize provenance into semantic extensions")

	// A-leg client truth is untouched; B-leg traffic carries the projection.
	ctp, ok := h.traffic.first(sdktraffic.LegCTP)
	require.True(t, ok, "A-leg input must be captured")
	require.True(t, ctp.decoded, "A-leg input body must decode as a canonical call")
	assert.Zero(t, algCountToolDef(ctp.call, algControlToolName), "A-leg client truth must never gain the control tool")
	assert.Zero(t, algCountPlainText(ctp.call, algControlInstruction), "A-leg client truth must never gain the control instruction")

	ptb, ok := h.traffic.first(sdktraffic.LegPTB)
	require.True(t, ok, "B-leg request traffic must be captured")
	require.True(t, ptb.decoded, "B-leg request body must decode as a canonical call")
	assert.Equal(t, 1, algCountToolDef(ptb.call, algControlToolName), "B-leg traffic must contain the control tool")
	assert.Equal(t, 1, algCountPlainText(ptb.call, algControlInstruction), "B-leg traffic must contain the control instruction")
	assert.Equal(t, len(got.Tools), len(ptb.call.Tools), "B-leg traffic and Backend.Open must agree on the backend-effective catalog")
	assert.Len(t, ptb.call.Extensions, len(call.Extensions), "B-leg traffic must not carry serialized control provenance")
	assert.Equal(t, 0, h.provider.handledCount(), "the control handler must not run during candidate open")
}

// TestControlToolProjection_itemAuthorityProjectsLeadingSystemItemAndSurvivesStageFinal
// is the item-authority twin of the acceptance case. The control instruction
// must be materialized as a leading system/developer message item before the
// mutable history, must not be duplicated, and must still be exactly one copy
// after the live conversation-view StageFinal reconstruction.
func TestControlToolProjection_itemAuthorityProjectsLeadingSystemItemAndSurvivesStageFinal(t *testing.T) {
	t.Parallel()

	h := algControlEligibleHarness(t, algOrderedToolsCaps(), algControlItemSnapshot(t))
	call := algControlItemCall(algControlBackendID+":"+algControlBackendModel, "control-item-authority")
	clientChoice := call.ToolChoice
	clientItems := append([]aipapi.Item(nil), call.Items...)

	algRunControlRequest(t, h, "control-item-authority", call)

	require.Equal(t, int32(1), h.opened.Load(), "exactly one candidate must reach Backend.Open")
	steps := h.log.snapshot()
	hookIdx := algIndexOf(steps, "request_hook:")
	stageFinalIdx := algIndexOf(steps, "stage_final")
	require.GreaterOrEqual(t, hookIdx, 0, "the request-part hook must run; steps=%v", steps)
	require.GreaterOrEqual(t, stageFinalIdx, 0, "live conversation-view Reassert must emit StageFinal; steps=%v", steps)
	assert.Greater(t, hookIdx, 0, "the request-part hook must precede the projection point; steps=%v", steps)

	postHookResolve := algLastIndexOf(steps, "resolve_caps:")
	require.Greater(t, postHookResolve, hookIdx, "the post-hook capability rederive must run after request hooks; steps=%v", steps)
	assert.Less(t, postHookResolve, stageFinalIdx, "the post-hook capability rederive must run before conversation-view StageFinal; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_tools=1", "the post-hook capability rederive must see the projected control tool; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_text=1", "the post-hook capability rederive must see the projected control instruction; steps=%v", steps)

	postHookCount := algFirstIndexAfter(steps, "count_call:", hookIdx)
	require.GreaterOrEqual(t, postHookCount, 0, "post-hook accounting preflight must run; steps=%v", steps)
	assert.Contains(t, steps[postHookCount], "control_tools=1", "the post-hook accounting preflight must see the projected control tool; steps=%v", steps)
	assert.Contains(t, steps[postHookCount], "control_text=1", "the post-hook accounting preflight must see the projected control instruction; steps=%v", steps)

	got := h.openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	require.NoError(t, got.Validate(), "the backend-effective call must stay canonically valid")
	require.True(t, got.HasItemAuthority(), "item authority must survive to Open on an ordered-items backend")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName), "exactly one control tool may reach the backend")
	assert.Equal(t, 1, algCountPlainText(*got, algControlInstruction), "exactly one control instruction may reach the backend")
	assert.Zero(t, algCountPlainText(*got, algNeverBackendText), "the final conversation-view reassertion must still drop never_backend content")

	require.NotEmpty(t, got.Items, "the backend-effective call must carry items")
	head := got.Items[0]
	assert.Equal(t, aipapi.ItemKindMessage, head.Kind, "the control instruction must be a leading message item")
	assert.Equal(t, aipapi.RoleSystem, head.Role, "the control instruction must keep its approved role")
	if assert.Len(t, head.Content, 1, "the control instruction item must carry exactly one content part") {
		assert.Equal(t, algControlInstruction, head.Content[0].Text, "the control instruction bytes must survive the final reassertion exactly")
	}

	// The client's own history stays behind the control item and is untouched.
	var survived []aipapi.Item
	for _, it := range got.Items[1:] {
		if algCountPlainText(aipapi.Call{Items: []aipapi.Item{it}}, algNeverBackendText) > 0 {
			continue
		}
		survived = append(survived, it)
	}
	if assert.NotEmpty(t, survived, "client history must remain behind the control item") {
		assert.Equal(t, clientItems[0].Content[0].Text, survived[0].Content[0].Text,
			"client item content must remain intact behind the control instruction")
	}

	_, approved, err := controltool.Project(aipapi.CloneCall(*call), algControlTestSpec(), algOrderedToolsCaps())
	require.NoError(t, err, "the item-authority fixture must be a valid control spec")
	require.True(t, approved.Active(), "the item-authority fixture must be eligible for the approved control spec")

	require.NotEmpty(t, got.Tools, "the backend-effective catalog must not be empty")
	tail := got.Tools[len(got.Tools)-1]
	assert.Equal(t, approved.Tool(), tail, "Backend.Open must receive the byte-identical approved control tool definition")
	assert.Equal(t, algControlToolName, tail.Name, "the control tool must be the final append")
	assert.Equal(t, algControlSchema, string(tail.Parameters), "the control tool schema bytes must be byte-identical to the approved spec")
	assert.Equal(t, []string{algWeatherToolName, algHookProbeTool}, algToolNames(got.Tools[:len(got.Tools)-1]),
		"the client tool order must be preserved ahead of the appended control tool")
	assert.Equal(t, clientChoice, got.ToolChoice, "the client tool choice must never be rewritten")
	assert.Len(t, got.Extensions, len(call.Extensions), "control activation must not serialize provenance into canonical call extensions")
	assert.Equal(t, 0, h.provider.handledCount(), "the control handler must not run during candidate open")

	ctp, ok := h.traffic.first(sdktraffic.LegCTP)
	require.True(t, ok, "A-leg input must be captured")
	require.True(t, ctp.decoded, "A-leg input body must decode as a canonical call")
	assert.Zero(t, algCountToolDef(ctp.call, algControlToolName), "A-leg client truth must never gain the control tool")
	assert.Zero(t, algCountPlainText(ctp.call, algControlInstruction), "A-leg client truth must never gain the control instruction")

	ptb, ok := h.traffic.first(sdktraffic.LegPTB)
	require.True(t, ok, "B-leg request traffic must be captured")
	require.True(t, ptb.decoded, "B-leg request body must decode as a canonical call")
	assert.Equal(t, 1, algCountToolDef(ptb.call, algControlToolName), "B-leg traffic must contain the control tool")
	assert.Equal(t, 1, algCountPlainText(ptb.call, algControlInstruction), "B-leg traffic must contain the control instruction")
}

// algControlIneligibleCase is one row of the design V1 Eligibility Matrix as
// observed end to end through the runtime.
type algControlIneligibleCase struct {
	name        string
	backendCaps aipapi.BackendCaps
	build       func() *aipapi.Call
	wantReason  string
	// wantControlTool/ wantControlText are how many control-named tool
	// definitions and control instruction copies the client itself declared.
	// The runtime must add nothing on top of them.
	wantControlTool int
	wantControlText int
	wantToolNames   []string
}

func algControlIneligibleMatrix() []algControlIneligibleCase {
	toolsCaps := algOrderedToolsCaps()
	noToolsCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	weather := func(selector string) *aipapi.Call {
		call := algWeatherCall(selector)
		call.ID = "control-ineligible"
		return call
	}
	bare := func(selector string) *aipapi.Call {
		call := pdBaseCall(selector)
		call.ID = "control-ineligible"
		return call
	}
	return []algControlIneligibleCase{
		{
			name:            "backend_without_tools_and_no_client_tools",
			backendCaps:     noToolsCaps,
			build:           func() *aipapi.Call { return bare(algControlBackendID + ":" + algControlBackendModel) },
			wantReason:      controltool.ReasonBackendToolsUnsupported,
			wantToolNames:   []string{},
			wantControlTool: 0,
			wantControlText: 0,
		},
		{
			name:        "client_tool_choice_none_without_client_tools",
			backendCaps: toolsCaps,
			build: func() *aipapi.Call {
				call := bare(algControlBackendID + ":" + algControlBackendModel)
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
				return call
			},
			wantReason:      controltool.ReasonToolChoiceNone,
			wantToolNames:   []string{},
			wantControlTool: 0,
			wantControlText: 0,
		},
		{
			name: "client_tool_choice_any_with_backend_tools",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonToolChoiceConstrained,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 0,
		},
		{
			name: "client_tool_choice_required_named_with_backend_tools",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: algWeatherToolName}
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonToolChoiceRequired,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 0,
		},
		{
			name: "client_allowed_tools_subset_with_backend_tools",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.ToolChoice = aipapi.ToolChoice{
					Mode:         aipapi.ToolChoiceAuto,
					AllowedTools: []string{algWeatherToolName},
				}
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonAllowedToolsConstrained,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 0,
		},
		{
			name: "client_declared_same_named_control_tool",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.Tools = append(call.Tools, aipapi.ToolDef{
					Name:        algControlToolName,
					Description: "Client-owned control tool.",
					Parameters:  json.RawMessage(`{"type":"object"}`),
				})
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonToolNameCollision,
			wantToolNames:   []string{algWeatherToolName, algControlToolName},
			wantControlTool: 1,
			wantControlText: 0,
		},
		{
			name: "client_owned_control_instruction_in_instructions",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.Instructions = []aipapi.Message{algControlSystemMessage()}
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonInstructionCollision,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 1,
		},
		{
			name: "client_owned_control_instruction_in_messages",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.Messages = append(call.Messages, algControlSystemMessage())
				return call
			},
			backendCaps:     toolsCaps,
			wantReason:      controltool.ReasonInstructionCollision,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 1,
		},
		{
			// Finding 4 (PR #724 review): a JSON-object output contract stays
			// inactive even when the backend declares the necessary tool AND
			// structured-output capabilities. The proxy synthesizes the
			// published text from a tool argument, so publishing it would
			// break the client's expected payload; the protocol abstains
			// instead.
			name: "json_object_response_format_with_backend_tools",
			build: func() *aipapi.Call {
				call := weather(algControlBackendID + ":" + algControlBackendModel)
				call.Options.ResponseMIMEType = "application/json"
				return call
			},
			backendCaps: aipapi.NewBackendCaps(
				aipapi.CapabilityStreaming,
				aipapi.CapabilityTools,
				aipapi.CapabilityOrderedItems,
				aipapi.CapabilityStructuredOutputs,
			),
			wantReason:      controltool.ReasonOutputFormatUnsupported,
			wantToolNames:   []string{algWeatherToolName},
			wantControlTool: 0,
			wantControlText: 0,
		},
	}
}

// TestControlToolProjection_ineligibleMatrixStaysUsableAndGainsNoControlContract
// pins the whole design V1 Eligibility Matrix end to end (Requirements 4.1-4.7):
// an ineligible candidate must remain openable, must not be rejected, must not
// gain any hidden tool or instruction, and must preserve the client's tool
// catalog, order, and tool choice exactly. The SDK contract is used as the
// expected reason oracle so the runtime and the pure projection cannot drift.
func TestControlToolProjection_ineligibleMatrixStaysUsableAndGainsNoControlContract(t *testing.T) {
	t.Parallel()

	for _, tc := range algControlIneligibleMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			call := tc.build()
			// Oracle: the pure projection must stay inactive, must report the
			// bounded reason, and must return the candidate call unchanged.
			projected, proj, err := controltool.Project(aipapi.CloneCall(*call), algControlTestSpec(), tc.backendCaps)
			require.NoError(t, err, "an ineligible matrix row is not a spec error")
			require.False(t, proj.Active(), "matrix row must be ineligible; reason=%q", proj.Reason())
			assert.Equal(t, tc.wantReason, proj.Reason(), "bounded inactive reason must match the approved matrix")
			assert.Equal(t, call.Tools, projected.Tools, "an inactive projection must not touch the client tool catalog")
			assert.Equal(t, call.ToolChoice, projected.ToolChoice, "an inactive projection must not rewrite the client tool choice")

			log := &algOrderLog{}
			provider := algControlTestProvider(t)
			trafficProbe := newAlgControlTrafficProbe(log)
			opened := &atomic.Int32{}
			openCall := &atomic.Pointer[aipapi.Call]{}

			ex := algSecureExecutor(t, map[string]execbackend.Backend{algControlBackendID: {
				Caps:          tc.backendCaps,
				TransportCaps: algStreamingTransport(),
				ResolveCaps: func(_ context.Context, got aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
					log.add("resolve_caps:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(got))
					return tc.backendCaps
				},
				Open: func(_ context.Context, got aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					cloned := aipapi.CloneCall(got)
					openCall.Store(&cloned)
					opened.Add(1)
					return aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventResponseFinished},
					}), nil
				},
			}}, nil, extensions.SnapshotOptions{
				TrafficObserver: trafficProbe,
				FeaturePlanes:   algControlPlanes(t, provider),
			})
			require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the generation still admits the generic control provider")

			stream, err := ex.Execute(principalCtx("control-ineligible"), call)
			require.NoError(t, err, "an ineligible control candidate must stay usable")
			t.Cleanup(func() { _ = stream.Close() })
			_ = algCollectEOF(t, stream)

			require.Equal(t, int32(1), opened.Load(), "the ineligible candidate must still open exactly once")
			got := openCall.Load()
			require.NotNil(t, got, "Backend.Open must receive the unchanged candidate call")
			require.NoError(t, got.Validate(), "the ineligible backend-effective call must stay canonically valid")
			assert.Equal(t, tc.wantControlTool, algCountToolDef(*got, algControlToolName),
				"an ineligible candidate must gain no control tool")
			assert.Equal(t, tc.wantControlText, algCountPlainText(*got, algControlInstruction),
				"an ineligible candidate must gain no control instruction")
			assert.Equal(t, tc.wantToolNames, algToolNames(got.Tools), "the client tool catalog and order must be preserved exactly")
			assert.Equal(t, call.ToolChoice, got.ToolChoice, "the client tool choice must never be rewritten")
			assert.Len(t, got.Extensions, len(call.Extensions), "an ineligible candidate must gain no serialized control provenance")
			assert.Equal(t, 0, provider.handledCount(), "the control handler must not run for an ineligible candidate")

			// Every probe before Backend.Open must observe exactly the
			// client-declared control surface, never an injected one.
			unchanged := fmt.Sprintf("control_tools=%d:control_text=%d", tc.wantControlTool, tc.wantControlText)
			steps := log.snapshot()
			require.NotEmpty(t, steps, "expected capability and traffic probes")
			for i, step := range steps {
				if strings.HasPrefix(step, "open:") {
					continue
				}
				assert.Contains(t, step, unchanged, "no probe before Open may see an injected control contract; step[%d]=%q", i, step)
			}
			firstResolve := algIndexOf(steps, "resolve_caps:")
			require.GreaterOrEqual(t, firstResolve, 0, "capability admission must run; steps=%v", steps)
			require.Contains(t, steps[firstResolve], unchanged, "capability admission must see the client surface unchanged; steps=%v", steps)

			ptb, ok := trafficProbe.first(sdktraffic.LegPTB)
			require.True(t, ok, "B-leg request traffic must be captured")
			require.True(t, ptb.decoded, "B-leg request body must decode as a canonical call")
			assert.Equal(t, tc.wantControlTool, algCountToolDef(ptb.call, algControlToolName),
				"B-leg traffic must not carry an injected control tool")
			assert.Equal(t, tc.wantControlText, algCountPlainText(ptb.call, algControlInstruction),
				"B-leg traffic must not carry an injected control instruction")
			assert.Equal(t, 1, trafficProbe.count(sdktraffic.LegPTB), "exactly one B-leg request must be captured")
		})
	}
}

// TestControlToolProjection_absentControlProviderIsZeroWorkNoOp pins the
// removal contract (Requirements 4.5, 10.5, 12.3): a generation that admits no
// control provider keeps the exact candidate-open ordering and byte behavior it
// has today, with no control work anywhere in the request.
func TestControlToolProjection_absentControlProviderIsZeroWorkNoOp(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	trafficProbe := newAlgControlTrafficProbe(log)
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}
	caps := algOrderedToolsCaps()

	backend := execbackend.Backend{
		Caps:                    caps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add("resolve_caps:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(call))
			return caps
		},
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			opened.Add(1)
			log.add("open:" + algControlBackendID + ":" + algControlMeta(call))
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := algSecureExecutor(t, map[string]execbackend.Backend{algControlBackendID: backend},
		hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{algRequestHookProbe{log: log}}}),
		extensions.SnapshotOptions{
			TrafficObserver: trafficProbe,
			FeaturePlanes:   algControlPlanes(t, nil),
		})
	require.Nil(t, ex.RuntimeSnapshot.ControlToolProvider(), "a generation without a control provider must read nil")
	algWireOpenOrderProbes(t, ex, log, algLiveConversationSnapshot(t))

	call := algControlMessageCall(algControlBackendID+":"+algControlBackendModel, "control-absent-provider")
	clientChoice := call.ToolChoice
	stream, err := ex.Execute(principalCtx("control-absent"), call)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), opened.Load(), "exactly one candidate must reach Backend.Open")
	got := openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	require.NoError(t, got.Validate(), "the backend-effective call must stay canonically valid")
	assert.Zero(t, algCountToolDef(*got, algControlToolName), "an absent control provider must append no control tool")
	assert.Zero(t, algCountPlainText(*got, algControlInstruction), "an absent control provider must append no control instruction")
	assert.Equal(t, []string{algWeatherToolName, algHookProbeTool}, algToolNames(got.Tools),
		"the ordinary tool catalog must be untouched")
	assert.Equal(t, clientChoice, got.ToolChoice, "the client tool choice must never be rewritten")
	assert.Zero(t, algCountPlainText(*got, algNeverBackendText), "the final conversation-view reassertion must still drop never_backend content")

	steps := log.snapshot()
	hookIdx := algIndexOf(steps, "request_hook:")
	stageFinalIdx := algIndexOf(steps, "stage_final")
	ptbIdx := algIndexOf(steps, string(sdktraffic.LegPTB)+":")
	openIdx := algIndexOf(steps, "open:")
	require.GreaterOrEqual(t, hookIdx, 0, "the request-part hook must run; steps=%v", steps)
	require.GreaterOrEqual(t, stageFinalIdx, 0, "live conversation-view Reassert must emit StageFinal; steps=%v", steps)
	require.GreaterOrEqual(t, ptbIdx, 0, "steps=%v", steps)
	require.GreaterOrEqual(t, openIdx, 0, "steps=%v", steps)
	assert.Less(t, hookIdx, stageFinalIdx, "request hooks must precede conversation-view StageFinal; steps=%v", steps)
	assert.Less(t, stageFinalIdx, ptbIdx, "conversation-view StageFinal must precede B-leg traffic; steps=%v", steps)
	assert.Less(t, ptbIdx, openIdx, "B-leg traffic must precede Backend.Open; steps=%v", steps)
	for i, step := range steps {
		assert.NotContains(t, step, "control_tools=1", "no probe may observe a control tool without a provider; step[%d]=%q", i, step)
		assert.NotContains(t, step, "control_text=1", "no probe may observe a control instruction without a provider; step[%d]=%q", i, step)
	}
}

// TestControlToolProjection_clientOwnedSameNamedToolIsPreservedNotAdopted pins
// the V1 Eligibility Matrix collision row together with Requirements 3.2, 3.3,
// 4.4, and 12.1: a client that already owns the provider's control tool name
// makes the candidate inactive instead of activating a proxy-owned tool. The
// client-owned tool is preserved exactly as declared — same definition bytes,
// same catalog position, no second same-named append — the proxy appends no
// control instruction on its account, the client tool choice is never
// rewritten, and no serialized control provenance is introduced on either leg.
// Trusted private activation is out of scope here and belongs to task 3.2.
func TestControlToolProjection_clientOwnedSameNamedToolIsPreservedNotAdopted(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	provider := algControlTestProvider(t)
	trafficProbe := newAlgControlTrafficProbe(log)
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}
	caps := algOrderedToolsCaps()

	ex := algSecureExecutor(t, map[string]execbackend.Backend{algControlBackendID: {
		Caps:          caps,
		TransportCaps: algStreamingTransport(),
		ResolveCaps: func(_ context.Context, got aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add("resolve_caps:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(got))
			return caps
		},
		Open: func(_ context.Context, got aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(got)
			openCall.Store(&cloned)
			opened.Add(1)
			log.add("open:" + algControlBackendID + ":" + algControlMeta(got))
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}}, nil, extensions.SnapshotOptions{
		TrafficObserver: trafficProbe,
		FeaturePlanes:   algControlPlanes(t, provider),
	})
	require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the generation still admits the generic control provider")

	clientControlTool := aipapi.ToolDef{
		Name:        algControlToolName,
		Description: "Client-owned control tool.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}
	call := algWeatherCall(algControlBackendID + ":" + algControlBackendModel)
	call.ID = "control-client-owned-name"
	call.Tools = append(call.Tools, clientControlTool)
	clientChoice := call.ToolChoice

	stream, err := ex.Execute(principalCtx("control-client-owned"), call)
	require.NoError(t, err, "a client-owned same-named tool must leave the candidate usable")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), opened.Load(), "the client-owned collision candidate must still open")
	got := openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the client-owned candidate")
	require.NoError(t, got.Validate(), "the backend-effective call must stay canonically valid")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName), "the client-owned tool must appear exactly once, never duplicated")
	assert.Equal(t, []string{algWeatherToolName, algControlToolName}, algToolNames(got.Tools),
		"the client-owned tool must keep its position in the client catalog")
	require.NotEmpty(t, got.Tools)
	adopted := got.Tools[len(got.Tools)-1]
	assert.Equal(t, clientControlTool.Name, adopted.Name)
	assert.Equal(t, clientControlTool.Description, adopted.Description,
		"a client-owned same-named tool must keep the client definition, not be replaced by the provider spec")
	assert.Equal(t, string(clientControlTool.Parameters), string(adopted.Parameters),
		"a client-owned same-named tool must keep the client schema bytes")
	assert.Zero(t, algCountPlainText(*got, algControlInstruction),
		"a client-declared tool name must never cause the control instruction to be appended")
	assert.Equal(t, clientChoice, got.ToolChoice, "the client tool choice must never be rewritten")
	assert.Len(t, got.Extensions, 0, "no serialized control provenance may appear on the backend-effective call")
	assert.Len(t, got.SemanticExtensions, 0, "no serialized control provenance may appear as a semantic extension")
	assert.Equal(t, 0, provider.handledCount(), "the control handler must not run for a client-owned collision")

	ctp, ok := trafficProbe.first(sdktraffic.LegCTP)
	require.True(t, ok, "A-leg input must be captured")
	require.True(t, ctp.decoded, "A-leg input body must decode as a canonical call")
	assert.Zero(t, algCountPlainText(ctp.call, algControlInstruction), "A-leg client truth must never gain the control instruction")

	ptb, ok := trafficProbe.first(sdktraffic.LegPTB)
	require.True(t, ok, "B-leg request traffic must be captured")
	require.True(t, ptb.decoded, "B-leg request body must decode as a canonical call")
	assert.Zero(t, algCountPlainText(ptb.call, algControlInstruction), "B-leg traffic must not carry an injected control instruction")
	assert.Len(t, ptb.call.Extensions, 0, "B-leg traffic must not carry serialized control provenance")
}

// TestControlToolProjection_hooksRunBeforeProjectionAndReassertionIsExact
// guards the two remaining task 3.2 seams that the acceptance cases above
// depend on: the request-part hook mutation must be visible to the projection
// point, and the final conversation-view reassertion must not duplicate or
// reorder the already-approved projection. It is expressed as a regression
// guard on the current ordering so 3.2 cannot quietly move the projection
// before ordinary request mutation.
//
// Because the projection point sits after ordinary request mutation, the
// authoritative post-hook rederive observes the hook mutation and the approved
// projection in one call: two ordinary tools plus the appended control tool.
// A preliminary post-hook eligibility lookup may legitimately still see the
// unprojected call, so only the authoritative rederive is asserted here.
func TestControlToolProjection_hooksRunBeforeProjectionAndReassertionIsExact(t *testing.T) {
	t.Parallel()

	h := algControlEligibleHarness(t, algOrderedToolsCaps(), algLiveConversationSnapshot(t))
	call := algControlMessageCall(algControlBackendID+":"+algControlBackendModel, "control-projection-seams")
	algRunControlRequest(t, h, "control-projection-seams", call)

	require.Equal(t, int32(1), h.opened.Load(), "exactly one candidate must reach Backend.Open")
	steps := h.log.snapshot()
	hookIdx := algIndexOf(steps, "request_hook:")
	stageFinalIdx := algIndexOf(steps, "stage_final")
	clampIdx := algIndexOf(steps, "clamp_preview:")
	ptbIdx := algIndexOf(steps, string(sdktraffic.LegPTB)+":")
	openIdx := algIndexOf(steps, "open:")
	postHookResolve := algLastIndexOf(steps, "resolve_caps:")
	require.GreaterOrEqual(t, hookIdx, 0, "the request-part hook must run; steps=%v", steps)
	require.GreaterOrEqual(t, stageFinalIdx, 0, "live conversation-view Reassert must emit StageFinal; steps=%v", steps)
	require.GreaterOrEqual(t, clampIdx, 0, "clamp preview must run; steps=%v", steps)
	require.GreaterOrEqual(t, postHookResolve, 0, "steps=%v", steps)

	assert.Less(t, hookIdx, postHookResolve, "ordinary request mutation must complete before the projection point; steps=%v", steps)
	assert.Less(t, postHookResolve, stageFinalIdx, "the projection point must precede conversation-view StageFinal; steps=%v", steps)
	assert.Less(t, stageFinalIdx, clampIdx, "conversation-view StageFinal must precede the clamp preview; steps=%v", steps)
	assert.Less(t, clampIdx, ptbIdx, "the clamp preview must precede B-leg traffic; steps=%v", steps)
	assert.Less(t, ptbIdx, openIdx, "B-leg traffic must precede Backend.Open; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "tools=3",
		"the authoritative post-hook rederive must observe ordinary request-hook mutation together with the approved control append; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_tools=1",
		"the authoritative post-hook rederive must see the projected control tool; steps=%v", steps)
	assert.Contains(t, steps[postHookResolve], "control_text=1",
		"the authoritative post-hook rederive must see the projected control instruction; steps=%v", steps)

	got := h.openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	require.NotNil(t, got.Options.MaxOutputTokens)
	assert.Equal(t, algClampTokens, *got.Options.MaxOutputTokens, "the final reassertion must not drop the applied clamp")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName), "the approved control tool must appear exactly once after reassertion")
	assert.Equal(t, 1, algCountPlainText(*got, algControlInstruction), "the approved control instruction must appear exactly once after reassertion")
	assert.Equal(t, 0, h.provider.handledCount(), "the control handler must not run during candidate open")
}
