package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// pdCaptureObserver records policy decision records for assertions.
type pdCaptureObserver struct {
	mu      sync.Mutex
	records []policydecision.Record
}

func (c *pdCaptureObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
	return nil
}

func (c *pdCaptureObserver) snapshot() []policydecision.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]policydecision.Record, len(c.records))
	copy(out, c.records)
	return out
}

// findRecord returns the first record matching stage, or nil.
func (c *pdCaptureObserver) findRecord(stage string) (policydecision.Record, bool) {
	for _, r := range c.snapshot() {
		if r.Stage == stage {
			return r, true
		}
	}
	return policydecision.Record{}, false
}

// pdHungPreReq blocks until ctx is done and returns ctx.Err(), used to assert timeout
// enforcement at the runtime integration level.
type pdHungPreReq struct {
	id   string
	mode sdkhooks.FailureMode
}

func (h pdHungPreReq) ID() string                        { return h.id }
func (h pdHungPreReq) Order() int                        { return 0 }
func (h pdHungPreReq) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h pdHungPreReq) Handle(ctx context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	<-ctx.Done()
	return prerequest.Decision{}, ctx.Err()
}

// policySecureExecutor builds a secure-session executor with a customizable snapshot and
// interleaved shaping disabled, modeled on nonInterferenceSecureExecutor.
func policySecureExecutor(t *testing.T, backends map[string]execbackend.Backend, snapOpts extensions.SnapshotOptions) (*runtime.Executor, *b2bua.MemoryStore) {
	t.Helper()
	ex, st := interleavedSecureExecutor(t, backends)
	ex.InterleavedProcessor = nil
	if snapOpts.Workspace == nil {
		snapOpts.Workspace = voidWorkspaceResolver{}
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	return ex, st
}

func pdBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

// pdFinishingStream returns a minimal well-formed finishing stream (started + finish).
func pdFinishingStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	})
}

// --- pre-backend stage handlers ---

type pdDenyPreReq struct{ message string }

func (pdDenyPreReq) ID() string                        { return "pd-deny-prereq" }
func (pdDenyPreReq) Order() int                        { return 0 }
func (pdDenyPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (d pdDenyPreReq) Handle(_ context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Decision{Deny: true, DenyMessage: d.message}, nil
}

type pdNoopPreReq struct{}

func (pdNoopPreReq) ID() string                        { return "pd-noop-prereq" }
func (pdNoopPreReq) Order() int                        { return 0 }
func (pdNoopPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdNoopPreReq) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Decision{}, nil
}

type pdMutateRtx struct{}

func (pdMutateRtx) ID() string                        { return "pd-mutate-rtx" }
func (pdMutateRtx) Order() int                        { return 0 }
func (pdMutateRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdMutateRtx) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.Messages[0].Parts[0].Text = "shaped:" + call.Messages[0].Parts[0].Text
	return nil
}

type pdNoopRtx struct{}

func (pdNoopRtx) ID() string                        { return "pd-noop-rtx" }
func (pdNoopRtx) Order() int                        { return 0 }
func (pdNoopRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdNoopRtx) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type pdFailRtx struct{}

func (pdFailRtx) ID() string                        { return "pd-fail-rtx" }
func (pdFailRtx) Order() int                        { return 0 }
func (pdFailRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdFailRtx) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return errors.New("pd transform boom")
}

type pdAppendToolRtx struct{}

func (pdAppendToolRtx) ID() string                        { return "pd-append-tool" }
func (pdAppendToolRtx) Order() int                        { return 0 }
func (pdAppendToolRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdAppendToolRtx) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.Tools = append(call.Tools, aipapi.ToolDef{Name: "policy-added", Parameters: []byte(`{}`)})
	return nil
}

// --- stream stage handlers ---

type pdDenyToolPolicy struct{ name string }

func (d pdDenyToolPolicy) ID() string                        { return "pd-deny-tool" }
func (d pdDenyToolPolicy) Order() int                        { return 0 }
func (d pdDenyToolPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (d pdDenyToolPolicy) Handle(_ context.Context, ev aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	if ev.ToolName == d.name {
		return toolpolicy.DecisionDeny, nil
	}
	return toolpolicy.DecisionAllow, nil
}

type pdRejectGate struct{}

func (pdRejectGate) ID() string                        { return "pd-reject-gate" }
func (pdRejectGate) Order() int                        { return 0 }
func (pdRejectGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (pdRejectGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.RejectOutcome(errors.New("completion rejected by gate")), nil
}

type pdPassGate struct{}

func (pdPassGate) ID() string                        { return "pd-pass-gate" }
func (pdPassGate) Order() int                        { return 0 }
func (pdPassGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (pdPassGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func recordingBackend(id string, opens *atomic.Int32, stream aipapi.ManagedEventStream) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return stream, nil
		},
	}
}

// --- submit hook handlers ---

type pdRejectSubmitHook struct{ reason string }

func (pdRejectSubmitHook) ID() string                        { return "pd-reject-submit" }
func (pdRejectSubmitHook) Order() int                        { return 0 }
func (pdRejectSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h pdRejectSubmitHook) Handle(_ context.Context, _ *aipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{Reject: true, Reason: h.reason}, nil
}

type pdAnnotateSubmitHook struct{}

func (pdAnnotateSubmitHook) ID() string                        { return "pd-annotate-submit" }
func (pdAnnotateSubmitHook) Order() int                        { return 0 }
func (pdAnnotateSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (pdAnnotateSubmitHook) Handle(_ context.Context, _ *aipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations["team"] = "platform"
	return sdkhooks.SubmitDecision{}, nil
}

type pdFailSubmitHook struct{ mode sdkhooks.FailureMode }

func (pdFailSubmitHook) ID() string                          { return "pd-fail-submit" }
func (pdFailSubmitHook) Order() int                          { return 0 }
func (h pdFailSubmitHook) FailureMode() sdkhooks.FailureMode { return h.mode }
func (pdFailSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, errors.New("submit boom")
}

// pdSubmitExecutor builds a secure executor whose Bus carries the given submit
// hooks. The snapshot is built from an empty bus (for the emitter + any stage
// chains), then ex.Bus is replaced with the submit-hook bus; RunSubmit uses
// ex.Bus while the evidence emitter comes from the snapshot.
func pdSubmitExecutor(t *testing.T, backends map[string]execbackend.Backend, snapOpts extensions.SnapshotOptions, submitHooks []sdkhooks.SubmitHook) *runtime.Executor {
	t.Helper()
	ex, _ := policySecureExecutor(t, backends, snapOpts)
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: submitHooks})
	return ex
}
