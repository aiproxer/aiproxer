package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

// ============ Task 4.2 tests ============

func TestStreamToolPolicyDenialNoRetry(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "blocked"},
		{Kind: aipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies:  []toolpolicy.Policy{pdDenyToolPolicy{name: "blocked"}},
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	call.Tools = []aipapi.ToolDef{{Name: "blocked", Parameters: []byte(`{}`)}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	stream, err := ex.Execute(principalCtx("user-pd-tool"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	if sawErr == nil {
		t.Fatal("expected stream error from tool policy denial")
	}
	if !strings.Contains(sawErr.Error(), "denied") {
		t.Fatalf("stream error should mention denied, got %v", sawErr)
	}
	if opens.Load() != 1 {
		t.Fatalf("no retry/failover expected: backend opens want 1 got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDToolEventReaction)
	if !ok {
		t.Fatalf("expected tool-event-reaction record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("outcome: got %s want deny", rec.Outcome)
	}
	if rec.BackendAttempted != true {
		t.Fatalf("BackendAttempted: got %v want true (stream stage)", rec.BackendAttempted)
	}
	if rec.ReasonCode != extensions.ReasonToolPolicyDenied {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonToolPolicyDenied)
	}
	if rec.Effect != policydecision.EffectNone {
		t.Fatalf("effect: got %s want none", rec.Effect)
	}
	if rec.Provider.ID != "pd-deny-tool" {
		t.Fatalf("provider id: got %q want %q (per-policy projection)", rec.Provider.ID, "pd-deny-tool")
	}
	for _, got := range obs.snapshot() {
		if got.Stage == feature.StageIDAttemptLifecycle {
			t.Fatalf("tool-policy denial should not also emit attempt-lifecycle evidence: %+v", got)
		}
	}
}

func TestStreamCompletionRejectNoFailover(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates:   []completion.Gate{pdRejectGate{}},
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-creject"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	if sawErr == nil {
		t.Fatal("expected stream error from completion gate reject")
	}
	if opens.Load() != 1 {
		t.Fatalf("no retry/failover expected: backend opens want 1 got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDCompletionGating)
	if !ok {
		t.Fatalf("expected completion-gating record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("outcome: got %s want deny", rec.Outcome)
	}
	if rec.ReasonCode != extensions.ReasonCompletionReject {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionReject)
	}
	if rec.OutputCommitted != false {
		t.Fatalf("OutputCommitted: got %v want false (completion is buffered at gate time; client has not received output)", rec.OutputCommitted)
	}
	if rec.BackendAttempted != true {
		t.Fatalf("BackendAttempted: got %v want true", rec.BackendAttempted)
	}
	if rec.Provider.ID != "pd-reject-gate" {
		t.Fatalf("provider id: got %q want %q (per-gate projection)", rec.Provider.ID, "pd-reject-gate")
	}
}

func TestStreamCompletionPassEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompletionGates:   []completion.Gate{pdPassGate{}},
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-cpass"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
	if col.Text.String() != "x" {
		t.Fatalf("aggregated text: got %q want x", col.Text.String())
	}
	rec, ok := obs.findRecord(feature.StageIDCompletionGating)
	if !ok {
		t.Fatalf("expected completion-gating record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeAllow {
		t.Fatalf("outcome: got %s want allow", rec.Outcome)
	}
	if rec.ReasonCode != extensions.ReasonCompletionPass {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonCompletionPass)
	}
	if rec.OutputCommitted != false {
		t.Fatalf("OutputCommitted: got %v want false (completion is buffered at gate time; client has not received output)", rec.OutputCommitted)
	}
	if rec.BackendAttempted != true {
		t.Fatalf("BackendAttempted: got %v want true", rec.BackendAttempted)
	}
	if rec.Provider.ID != "pd-pass-gate" {
		t.Fatalf("provider id: got %q want %q (per-gate projection)", rec.Provider.ID, "pd-pass-gate")
	}
}

func TestStreamPolicyNoninterferenceNoObserver(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	// No PolicyObserver configured; default no-op observer.
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			ToolCallPolicies:  []toolpolicy.Policy{pdDenyToolPolicy{name: "nonexistent"}},
			CompletionGates:   []completion.Gate{pdPassGate{}},
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-streamni"), call)
	if err != nil {
		t.Fatalf("execute (no observer): %v", err)
	}
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect (no observer): %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens (no observer): want 1 got %d", opens.Load())
	}
	if col.Text.String() != "ok" {
		t.Fatalf("aggregated text (no observer): got %q want ok", col.Text.String())
	}
	if !col.FinishReceived {
		t.Fatal("expected finish (no observer)")
	}
}

// ============ Task 3.6 / 4.2 attempt lifecycle evidence ============

// TestStreamAttemptFailureEmitsEvidence asserts a post-output backend failure
// (surfaced failure) emits an attempt-lifecycle error/none record with the
// backend id as provider and BackendAttempted=true, without triggering retry.
func TestStreamAttemptFailureEmitsEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &deltaThenErrStream{n: 0}, nil
			},
		},
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-attempt-fail"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	if sawErr == nil {
		t.Fatal("expected surfaced failure from post-output backend error")
	}
	if opens.Load() != 1 {
		t.Fatalf("no retry/failover expected: backend opens want 1 got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDAttemptLifecycle)
	if !ok {
		t.Fatalf("expected attempt-lifecycle record, got %d records: %+v", len(obs.snapshot()), obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeError || rec.Effect != policydecision.EffectNone {
		t.Fatalf("attempt failure: want error/none, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonAttemptFailure {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonAttemptFailure)
	}
	if rec.Provider.ID != "openai" {
		t.Fatalf("provider id: got %q want openai (backend as attempt provider)", rec.Provider.ID)
	}
	if !rec.BackendAttempted {
		t.Fatalf("BackendAttempted: got false want true (stream attempt stage)")
	}
	if err := extensions.ValidateDecisionRecord(rec); err != nil {
		t.Fatalf("attempt record not legal: %v", err)
	}
}

// TestStreamAttemptFailureNoninterferenceNoObserver asserts no attempt evidence
// is emitted when no PolicyObserver is configured, while the failure still
// surfaces to the client.
func TestStreamAttemptFailureNoninterferenceNoObserver(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return &deltaThenErrStream{n: 0}, nil
			},
		},
	}
	obs := &pdCaptureObserver{}
	// No PolicyObserver configured; default no-op observer.
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	var buf bytes.Buffer
	ex.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-attempt-ni"), call)
	if err != nil {
		t.Fatalf("execute (no observer): %v", err)
	}
	var sawErr error
	for {
		_, rerr := stream.Recv(context.Background())
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			sawErr = rerr
			break
		}
	}
	_ = stream.Close()
	if sawErr == nil {
		t.Fatal("expected surfaced failure even without observer")
	}
	if len(obs.snapshot()) != 0 {
		t.Fatalf("no attempt evidence expected without observer, got %+v", obs.snapshot())
	}
	if strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("no policy decision log expected without observer, got: %q", buf.String())
	}
}
