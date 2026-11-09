package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// siwcPreviewOmissionWarningLiteral is the private exact marker the connector
// emits on the canonical stream and the host adapter consumes. It is duplicated
// on both sides of the module boundary and pinned by the staged acceptance test.
const siwcPreviewOmissionWarningLiteral = "siwc_preview_omission_v1"

// warningEvents returns the canonical warning events in stream order.
func warningEvents(frames []backendplugin.ServerFrame) []*backendplugin.CanonicalEvent {
	out := make([]*backendplugin.CanonicalEvent, 0)
	for _, frame := range frames {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil &&
			frame.Event.Kind == backendplugin.EventWarning {
			out = append(out, frame.Event)
		}
	}
	return out
}

// eventKindOrder returns the canonical event kinds in stream order.
func eventKindOrder(frames []backendplugin.ServerFrame) []backendplugin.EventKind {
	out := make([]backendplugin.EventKind, 0)
	for _, frame := range frames {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil {
			out = append(out, frame.Event.Kind)
		}
	}
	return out
}

// TestExecute_OmissionEmitsOneFixedWarningAfterResponseStarted proves the
// connector half of the F3 paired fix: a served request with at least one
// preview omission carries exactly one fixed, content-free warning event, and
// it follows response_started so the canonical lifecycle stays valid.
func TestExecute_OmissionEmitsOneFixedWarningAfterResponseStarted(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, hostedDeclarationCall()))
	if err != nil {
		t.Fatalf("interoperable Execute: %v", err)
	}
	if terminal := terminalFrameOf(t, frames); terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal status = %q, want success", terminal.Status)
	}

	warnings := warningEvents(frames)
	if len(warnings) != 1 {
		t.Fatalf("canonical warning events = %d, want exactly one fixed omission marker", len(warnings))
	}
	if warnings[0].Warning == nil || *warnings[0].Warning != siwcPreviewOmissionWarningLiteral {
		t.Fatalf("warning = %#v, want the exact private marker %q", warnings[0].Warning, siwcPreviewOmissionWarningLiteral)
	}
	if strings.Contains(*warnings[0].Warning, "web_search") || strings.Contains(*warnings[0].Warning, "find the weather") {
		t.Fatalf("the marker must be content-free, got %q", *warnings[0].Warning)
	}

	kinds := eventKindOrder(frames)
	startIndex, warningIndex := -1, -1
	for i, kind := range kinds {
		switch kind {
		case backendplugin.EventResponseStarted:
			startIndex = i
		case backendplugin.EventWarning:
			warningIndex = i
		}
	}
	if startIndex < 0 {
		t.Fatal("the stream emitted no response_started event")
	}
	if warningIndex != startIndex+1 {
		t.Fatalf("warning index = %d, want immediately after response_started at %d; kinds=%v",
			warningIndex, startIndex, kinds)
	}
}

// TestExecute_OmissionWarningDoesNotBlockCancellation proves the injected
// warning preserves the existing cancellation contract: the provider request is
// still aborted, exactly one attempt exists, and no failure is manufactured.
func TestExecute_OmissionWarningDoesNotBlockCancellation(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, cancellationScript)
	provider.stallUntilClose = true
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newExecuteHarness(ctx, hostedDeclarationInvocation(t, hostedDeclarationCall()))
	done := make(chan error, 1)
	go func() { done <- inst.Execute(h) }()

	awaitAssistantText(t, h, "held open")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after the downstream context was cancelled")
	}
	select {
	case <-provider.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request context was not cancelled: the connection was not released")
	}
	if requests := provider.captured(); len(requests) != 1 {
		t.Fatalf("cancellation must not start another provider attempt: got %d requests", len(requests))
	}
	if warnings := warningEvents(h.snapshot()); len(warnings) != 1 {
		t.Fatalf("cancellation stream warning events = %d, want the one omission marker", len(warnings))
	}
}

// TestExecute_MaterializedContinuationEmitsNoWarning proves the marker is not a
// generic "an Omission record exists" signal: dropping the proxy-owned
// previous_response_id from a materialized continuation is normal bookkeeping,
// not a degradation, so it must not warn the operator.
func TestExecute_MaterializedContinuationEmitsNoWarning(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	inv := invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{})
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatalf("CallFromInvocation: %v", err)
	}
	call.PreviousResponseID = "resp_parent"
	aipcont.MarkMaterialized(&call)

	stream, err := inst.openManagedStream(context.Background(), inv, call)
	if err != nil {
		t.Fatalf("openManagedStream: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !collected.FinishReceived {
		t.Fatal("the materialized continuation must complete")
	}
	if len(collected.Warnings) != 0 {
		t.Fatalf("materialized continuation warnings = %v, want none", collected.Warnings)
	}
}

// TestExecute_NoOmissionEmitsNoWarning proves that an ordinary served request
// does not gain the marker.
func TestExecute_NoOmissionEmitsNoWarning(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, err := runExecute(context.Background(), inst, invocation(aipapi.DeliveryModeNonStreaming, backendplugin.GenerationOptions{}))
	if err != nil {
		t.Fatalf("ordinary Execute: %v", err)
	}
	if got := len(warningEvents(frames)); got != 0 {
		t.Fatalf("ordinary request warning events = %d, want 0", got)
	}
}

// TestExecute_StrictOmissionEmitsNoWarning proves that a strict refusal never
// manufactures a served-stream warning.
func TestExecute_StrictOmissionEmitsNoWarning(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
		"policy_mode":        string(responses.PolicyModeStrict),
	})

	frames, err := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, hostedDeclarationCall()))
	if !errors.Is(err, responses.ErrRequestNotAdmissible) {
		t.Fatalf("strict Execute error = %v, want the preview-policy refusal", err)
	}
	if got := len(warningEvents(frames)); got != 0 {
		t.Fatalf("strict refusal warning events = %d, want 0", got)
	}
	if events, _, _, _ := countFrames(frames); events != 0 {
		t.Fatalf("strict refusal canonical events = %d, want 0", events)
	}
}

// TestExecute_PreResponseStartedFailureEmitsNoWarning proves that a request
// that is not actually served (provider fails before response_started) emits no
// omission marker.
func TestExecute_PreResponseStartedFailureEmitsNoWarning(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	provider.status = http.StatusInternalServerError
	provider.errorBody = `{"error":{"message":"upstream unavailable"}}`
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	frames, _ := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, hostedDeclarationCall()))
	if got := len(warningEvents(frames)); got != 0 {
		t.Fatalf("pre-response-start failure warning events = %d, want 0", got)
	}
}
