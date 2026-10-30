// Failover replacement isolation of the private control-call state for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Concurrency and
// Lifecycle / Capture State; requirements 4.7, 8.2, 8.7, 10.3, 12.5).
//
// This file is the public-stream half of the lifecycle contract: it drives a real
// executor, real candidate failover, and the real client event stream. The
// replaced attempt opens a control call it never finishes and is then lost to a
// recoverable pre-output transport failure; the replacement attempt must derive
// an independent capture, so the same opaque call ID is claimable again on the new
// owner and the pinned provider runs exactly once, for the replacement attempt
// only. Nothing about the control call may reach the client, in either attempt.
//
// Generic runtime only: the provider is the same anonymous `proxy_control` model
// control tool any feature generation would contribute through
// feature.PlaneControlToolProvider.
package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// controlLifecycleProvider is the generation-admitted generic control provider.
// It records every handoff with its frozen attempt provenance, so a case can prove
// which attempt the control result belongs to.
type controlLifecycleProvider struct {
	outcome controltool.Outcome

	mu    sync.Mutex
	calls []controltool.CompletedCall
	metas []controltool.Meta
}

func (p *controlLifecycleProvider) ID() string             { return algControlProviderID }
func (p *controlLifecycleProvider) Spec() controltool.Spec { return algControlTestSpec() }

func (p *controlLifecycleProvider) Handle(_ context.Context, call controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	p.calls = append(p.calls, controltool.CompletedCall{
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ArgsJSON:   append([]byte(nil), call.ArgsJSON...),
	})
	p.metas = append(p.metas, meta)
	p.mu.Unlock()
	return p.outcome, nil
}

func (p *controlLifecycleProvider) observed() ([]controltool.CompletedCall, []controltool.Meta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls := make([]controltool.CompletedCall, len(p.calls))
	copy(calls, p.calls)
	metas := make([]controltool.Meta, len(p.metas))
	copy(metas, p.metas)
	return calls, metas
}

// controlLifecyclePlanes composes the provider the way a feature generation does.
func controlLifecyclePlanes(t *testing.T, provider controltool.Provider) aipfeature.FrozenPlaneSet {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, algControlProviderID, provider),
		"generation composition must accept the generic control spec")
	return cs.Freeze()
}

// controlLifecycleFailoverStream emits its events and then fails with fail, or
// ends normally when fail is nil. The failure is what loses the attempt.
type controlLifecycleFailoverStream struct {
	events []aipapi.Event
	fail   error
	idx    int
}

func (s *controlLifecycleFailoverStream) Recv(context.Context) (aipapi.Event, error) {
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}

func (s *controlLifecycleFailoverStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *controlLifecycleFailoverStream) Close() error { return nil }

var _ aipapi.ManagedEventStream = (*controlLifecycleFailoverStream)(nil)

// TestControlCallLifecycleFailover_replacementDerivesIndependentCapture pins
// requirement 4.7 and the replacement-owner rule on the real public stream: a
// replacement attempt recomputes its own activation, inherits no call ID and no
// argument buffer from the attempt it replaced, and is the only attempt whose
// private result survives.
func TestControlCallLifecycleFailover_replacementDerivesIndependentCapture(t *testing.T) {
	t.Parallel()

	const replacementBackendID = "openai-control-replacement"
	const callID = "control-shared-id"

	provider := &controlLifecycleProvider{outcome: controltool.Outcome{
		Kind:       controltool.OutcomeComplete,
		ResultText: "the replacement answer",
		ReasonCode: "control_complete",
	}}
	planes := controlLifecyclePlanes(t, provider)
	log := &algOrderLog{}

	// Both candidates are tools-capable, so both are independently eligible for
	// the control contract. The first one opens a control call it never finishes
	// and then fails before any output is committed, which is the recoverable
	// pre-output transition that loses the attempt.
	loserStream := &controlLifecycleFailoverStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: callID, ToolName: algControlToolName},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: callID, Delta: `{"note":"loser`},
		},
		fail: aipapi.RecoverablePreOutputError(errors.New("connection reset before output")),
	}
	// The replacement attempt claims the very same opaque call ID. An inherited
	// capture would treat it as a duplicate claim and never hand it off.
	replacementStream := &controlLifecycleFailoverStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: callID, ToolName: algControlToolName},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: callID, Delta: `{"note":"replacement"}`},
			{Kind: aipapi.EventToolCallFinished, ToolCallID: callID, ToolName: algControlToolName},
			{Kind: aipapi.EventResponseFinished},
		},
	}

	ex := algSeamExecutor(t, map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				return loserStream, nil
			}),
		replacementBackendID: algSeamBackend(log, algOrderedToolsCaps(), replacementBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				return replacementStream, nil
			}),
	}, planes, log)
	require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the generation must admit the generic control provider")

	selector := algControlBackendID + ":" + algControlBackendModel + "|" + replacementBackendID + ":" + algControlBackendModel
	stream, err := ex.Execute(principalCtx("control-lifecycle-failover"), algSeamCall(selector, "control-lifecycle-failover"))
	require.NoError(t, err, "a recoverable pre-output failure must still reach a replacement candidate")
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	// The replacement really happened, and both attempts activated the contract
	// from their own candidate.
	steps := log.snapshot()
	require.GreaterOrEqual(t, algIndexOf(steps, "open:"+algControlBackendID), 0, "the first candidate must be opened; steps=%v", steps)
	require.GreaterOrEqual(t, algIndexOf(steps, "open:"+replacementBackendID), 0, "the replacement candidate must be opened; steps=%v", steps)
	for _, step := range steps {
		if strings.HasPrefix(step, "open:") {
			assert.Contains(t, step, "control_tools=1",
				"every candidate must project the control contract from its own capabilities; step=%q", step)
		}
	}

	// Exactly one handoff, for the replacement attempt, with its own bounded
	// arguments and its own frozen provenance.
	calls, metas := provider.observed()
	require.Len(t, calls, 1,
		"only the replacement attempt's own valid completion may be handed to the pinned provider; calls=%+v", calls)
	assert.Equal(t, callID, calls[0].ToolCallID)
	assert.Equal(t, algControlToolName, calls[0].ToolName)
	assert.Equal(t, `{"note":"replacement"}`, string(calls[0].ArgsJSON),
		"a replacement attempt must never inherit the replaced attempt's argument bytes")
	require.Len(t, metas, 1)
	assert.Equal(t, replacementBackendID+":"+algControlBackendModel, metas[0].CandidateKey,
		"the handoff must carry the replacement attempt's own candidate")

	// Neither attempt's control traffic reached the client, and the replaced
	// attempt's half-open call was never completed into client tool execution.
	for _, ev := range released {
		assert.NotEqual(t, aipapi.EventToolCallStarted, ev.Kind, "a claimed control start must never be released; released=%+v", released)
	}
	assert.Zero(t, controlLifecycleCountContaining(controlLifecycleEventLabels(released), algControlToolName),
		"no proxy-owned control call may reach the client; released=%+v", released)
	assert.Zero(t, controlLifecycleCountContaining(controlLifecycleEventLabels(released), callID),
		"no control call ID may reach the client; released=%+v", released)
}

// controlLifecycleCountContaining counts labels carrying want, so a failing
// assertion can name exactly which observed label leaked.
func controlLifecycleCountContaining(values []string, want string) int {
	count := 0
	for _, v := range values {
		if strings.Contains(v, want) {
			count++
		}
	}
	return count
}

func controlLifecycleEventLabels(released []aipapi.Event) []string {
	out := make([]string, 0, len(released))
	for _, ev := range released {
		label := string(ev.Kind)
		if ev.ToolCallID != "" {
			label += ":" + ev.ToolCallID
		}
		if ev.Item != nil && ev.Item.ToolCall != nil {
			label += ":" + ev.Item.ToolCall.CallID
		}
		out = append(out, label)
	}
	return out
}
