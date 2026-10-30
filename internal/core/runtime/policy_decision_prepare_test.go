package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// ============ Task 4.1 tests ============

func TestPreparePreRequestDenialNoBackendAttempt(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{pdDenyPreReq{message: "blocked"}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	_, err := ex.Execute(principalCtx("user-pd-deny"), call)
	if err == nil {
		t.Fatal("expected pre-request denial error")
	}
	if _, ok := errors.AsType[*prerequest.RejectError](err); !ok {
		t.Fatalf("expected prerequest.RejectError, got %T %v", err, err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend must not open on pre-request denial, got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDPreRequest)
	if !ok {
		t.Fatalf("expected pre-request record, got %d records: %+v", len(obs.snapshot()), obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("outcome: got %s want %s", rec.Outcome, policydecision.OutcomeDeny)
	}
	if rec.BackendAttempted != false {
		t.Fatalf("BackendAttempted: got %v want false (no-backend-attempt)", rec.BackendAttempted)
	}
	if rec.Provider.ID != "pd-deny-prereq" {
		t.Fatalf("provider id: got %q want %q (per-handler projection, not aggregate runtime)", rec.Provider.ID, "pd-deny-prereq")
	}
	if rec.ReasonCode != extensions.ReasonPreRequestDenied {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonPreRequestDenied)
	}
	if rec.ClientMessage != "blocked" {
		t.Fatalf("client message: got %q want %q", rec.ClientMessage, "blocked")
	}
	if rec.Effect != policydecision.EffectNone {
		t.Fatalf("effect: got %s want none", rec.Effect)
	}
}

func TestPrepareRequestTransformMutationEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	var seenText string
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				if len(call.Messages) > 0 {
					seenText = textOf(call.Messages[0])
				}
				return backendStream, nil
			},
		},
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdMutateRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-mut"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
	if seenText != "shaped:hi" {
		t.Fatalf("backend saw %q want shaped:hi (mutation must drive backend attempt)", seenText)
	}
	rec, ok := obs.findRecord(feature.StageIDRequestWide)
	if !ok {
		t.Fatalf("expected request-wide record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeAllow {
		t.Fatalf("outcome: got %s want allow", rec.Outcome)
	}
	if rec.Effect != policydecision.EffectMutate {
		t.Fatalf("effect: got %s want mutate", rec.Effect)
	}
	if rec.Provider.ID != "pd-mutate-rtx" {
		t.Fatalf("provider id: got %q want %q (per-transform projection)", rec.Provider.ID, "pd-mutate-rtx")
	}
	if rec.ReasonCode != extensions.ReasonRequestTransformMutated {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonRequestTransformMutated)
	}
	if rec.BackendAttempted != false {
		t.Fatalf("BackendAttempted: got %v want false (pre-backend stage)", rec.BackendAttempted)
	}
}

func TestPrepareRequestTransformPassthroughEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-pass"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	rec, ok := obs.findRecord(feature.StageIDRequestWide)
	if !ok {
		t.Fatalf("expected request-wide record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeAllow {
		t.Fatalf("outcome: got %s want allow", rec.Outcome)
	}
	if rec.Effect != policydecision.EffectNone {
		t.Fatalf("effect: got %s want none", rec.Effect)
	}
	if rec.ReasonCode != extensions.ReasonRequestTransformPass {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonRequestTransformPass)
	}
	if rec.Provider.ID != "pd-noop-rtx" {
		t.Fatalf("provider id: got %q want %q (per-transform projection)", rec.Provider.ID, "pd-noop-rtx")
	}
}

func TestPrepareRequestTransformFailureEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdFailRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	_, err := ex.Execute(principalCtx("user-pd-fail"), call)
	if err == nil {
		t.Fatal("expected transform failure error")
	}
	if opens.Load() != 0 {
		t.Fatalf("backend must not open on transform failure, got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDRequestWide)
	if !ok {
		t.Fatalf("expected request-wide record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeError {
		t.Fatalf("outcome: got %s want error", rec.Outcome)
	}
	if rec.ReasonCode != extensions.ReasonRequestTransformFailure {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonRequestTransformFailure)
	}
	if rec.BackendAttempted != false {
		t.Fatalf("BackendAttempted: got %v want false", rec.BackendAttempted)
	}
	if rec.Provider.ID != "pd-fail-rtx" {
		t.Fatalf("provider id: got %q want %q (per-transform failure projection)", rec.Provider.ID, "pd-fail-rtx")
	}
}

func TestPrepareEffectiveCapabilityAfterPolicyShaping(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	var seenTools int
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				seenTools = len(call.Tools)
				return backendStream, nil
			},
		},
	}
	obs := &pdCaptureObserver{}
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdAppendToolRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	call.Tools = []aipapi.ToolDef{{Name: "orig", Parameters: []byte(`{}`)}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	stream, err := ex.Execute(principalCtx("user-pd-cap"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
	if seenTools != 2 {
		t.Fatalf("capability negotiation / backend attempt must observe mutated call: saw %d tools want 2", seenTools)
	}
}

func TestPreparePolicyNoninterferenceNoObserver(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	// No PolicyObserver configured; default snapshot (no-op observer).
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms:  []request.Transform{pdMutateRtx{}},
			PreRequestHandlers: []prerequest.Handler{pdNoopPreReq{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-ni"), call)
	if err != nil {
		t.Fatalf("execute (no observer): %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect (no observer): %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens (no observer): want 1 got %d", opens.Load())
	}
}

// TestPreparePolicyNoninterferenceNoObserverEmitsNoPolicyLog proves the no-op
// observer default does not attach an active evidence emitter, so no policy
// decision log line is produced (requirements 7.6, 10.5).
func TestPreparePolicyNoninterferenceNoObserverEmitsNoPolicyLog(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	// No PolicyObserver configured; default snapshot (no-op observer).
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms:  []request.Transform{pdMutateRtx{}},
			PreRequestHandlers: []prerequest.Handler{pdNoopPreReq{}},
		}),
	})
	var buf bytes.Buffer
	ex.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-nolog"), call)
	if err != nil {
		t.Fatalf("execute (no observer): %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect (no observer): %v", err)
	}
	if strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("no policy decision log expected with default no-op observer, got: %q", buf.String())
	}
}

// TestPrepareNoObserverTimeoutStillEnforces proves a non-default timeout budget is
// enforced on a hung pre-request handler even when no PolicyObserver is configured
// (no-op observer / nil emitter). The default no-timeout/no-observer path stays cheap
// and silent; a configured budget must still bound the provider (requirements 6.3,
// 7.6, 10.5).
func TestPrepareNoObserverTimeoutStillEnforces(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	// No PolicyObserver configured: default no-op observer, nil emitter.
	ex, _ := policySecureExecutor(t, backends, extensions.SnapshotOptions{
		TimeoutBudgetSource: extensions.StaticTimeoutBudgetSource{Budget: 40 * time.Millisecond},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{pdHungPreReq{id: "pd-hung-prereq", mode: sdkhooks.FailClosed}},
			RequestTransforms:  []request.Transform{pdNoopRtx{}},
		}),
	})
	call := pdBaseCall("openai:gpt-4")
	start := time.Now()
	_, err := ex.Execute(principalCtx("user-pd-noobserver-timeout"), call)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout failure from hung pre-request handler")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("want policy failure, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout must bound hung provider even with no observer, elapsed %v", elapsed)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend must not open on pre-request timeout, got %d", opens.Load())
	}
}

// ============ Task 3.5 / 4.1 submit hook evidence ============

// TestPrepareSubmitRejectEvidence asserts a rejecting submit hook emits a
// deny/none submit record with the hook's provider id and no backend attempt.
func TestPrepareSubmitRejectEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex := pdSubmitExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	}, []sdkhooks.SubmitHook{pdRejectSubmitHook{reason: "blocked"}})
	call := pdBaseCall("openai:gpt-4")
	_, err := ex.Execute(principalCtx("user-pd-submit-reject"), call)
	if err == nil {
		t.Fatal("expected submit reject error")
	}
	if !sdkhooks.IsSubmitReject(err) {
		t.Fatalf("expected submit reject, got %v", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend must not open on submit reject, got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDSubmit)
	if !ok {
		t.Fatalf("expected submit record, got %d records: %+v", len(obs.snapshot()), obs.snapshot())
	}
	if rec.Outcome != policydecision.OutcomeDeny {
		t.Fatalf("outcome: got %s want deny", rec.Outcome)
	}
	if rec.Effect != policydecision.EffectNone {
		t.Fatalf("effect: got %s want none", rec.Effect)
	}
	if rec.BackendAttempted {
		t.Fatalf("BackendAttempted: got true want false (no-backend-attempt submit denial)")
	}
	if rec.Provider.ID != "pd-reject-submit" {
		t.Fatalf("provider id: got %q want pd-reject-submit (per-hook projection)", rec.Provider.ID)
	}
	if rec.ReasonCode != extensions.ReasonSubmitRejected {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonSubmitRejected)
	}
}

// TestPrepareSubmitAnnotateEvidence asserts an annotating submit hook emits an
// allow/annotate submit record and the request proceeds to the backend.
func TestPrepareSubmitAnnotateEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex := pdSubmitExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	}, []sdkhooks.SubmitHook{pdAnnotateSubmitHook{}})
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-submit-annotate"), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDSubmit)
	if !ok {
		t.Fatalf("expected submit record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeAllow || rec.Effect != policydecision.EffectAnnotate {
		t.Fatalf("annotate: want allow/annotate, got %s/%s", rec.Outcome, rec.Effect)
	}
	if rec.Provider.ID != "pd-annotate-submit" {
		t.Fatalf("provider id: got %q want pd-annotate-submit", rec.Provider.ID)
	}
	if rec.ReasonCode != extensions.ReasonSubmitAnnotated {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonSubmitAnnotated)
	}
	if rec.Annotations["team"] != "platform" {
		t.Fatalf("submit annotations not projected: %+v", rec.Annotations)
	}
	if rec.BackendAttempted {
		t.Fatalf("BackendAttempted: got true want false (pre-backend stage)")
	}
}

// TestPrepareSubmitFailClosedEvidence asserts a fail-closed submit hook failure
// emits an error/none submit record and the request is rejected.
func TestPrepareSubmitFailClosedEvidence(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	ex := pdSubmitExecutor(t, backends, extensions.SnapshotOptions{
		PolicyObserver: obs,
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	}, []sdkhooks.SubmitHook{pdFailSubmitHook{mode: sdkhooks.FailClosed}})
	call := pdBaseCall("openai:gpt-4")
	_, err := ex.Execute(principalCtx("user-pd-submit-fail"), call)
	if err == nil {
		t.Fatal("expected submit fail-closed error")
	}
	if opens.Load() != 0 {
		t.Fatalf("backend must not open on submit fail-closed, got %d", opens.Load())
	}
	rec, ok := obs.findRecord(feature.StageIDSubmit)
	if !ok {
		t.Fatalf("expected submit failure record, got %d records", len(obs.snapshot()))
	}
	if rec.Outcome != policydecision.OutcomeError {
		t.Fatalf("outcome: got %s want error", rec.Outcome)
	}
	if rec.ReasonCode != extensions.ReasonSubmitFailure {
		t.Fatalf("reason code: got %q want %q", rec.ReasonCode, extensions.ReasonSubmitFailure)
	}
	if rec.Provider.ID != "pd-fail-submit" {
		t.Fatalf("provider id: got %q want pd-fail-submit", rec.Provider.ID)
	}
}

// TestPrepareSubmitNoninterferenceNoObserver asserts no submit evidence is
// emitted and no policy log is produced when no PolicyObserver is configured
// (default no-op observer), while submit behavior is preserved.
func TestPrepareSubmitNoninterferenceNoObserver(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backendStream := pdFinishingStream()
	backends := map[string]execbackend.Backend{
		"openai": recordingBackend("openai", &opens, backendStream),
	}
	obs := &pdCaptureObserver{}
	// No PolicyObserver configured; obs is only used to assert no records.
	ex := pdSubmitExecutor(t, backends, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{pdNoopRtx{}},
		}),
	}, []sdkhooks.SubmitHook{pdAnnotateSubmitHook{}})
	var buf bytes.Buffer
	ex.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	call := pdBaseCall("openai:gpt-4")
	stream, err := ex.Execute(principalCtx("user-pd-submit-ni"), call)
	if err != nil {
		t.Fatalf("execute (no observer): %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect (no observer): %v", err)
	}
	if len(obs.snapshot()) != 0 {
		t.Fatalf("no submit evidence expected without observer, got %+v", obs.snapshot())
	}
	if strings.Contains(buf.String(), "policy decision") {
		t.Fatalf("no policy decision log expected without observer, got: %q", buf.String())
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens (no observer): want 1 got %d", opens.Load())
	}
}
