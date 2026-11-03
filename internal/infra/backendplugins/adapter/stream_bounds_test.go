package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestBackpressure_SlowConsumerBoundedPending(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "bp", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "bp", MaxPendingEvents: 1,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind == "" {
		t.Fatal("empty event")
	}
	// Drain remainder; with pending=1 the producer must have blocked rather than
	// buffering unbounded events in host memory.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout draining")
		default:
		}
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTerminal_DuplicateRejected(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeDuplicateTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "dup", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "dup"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected duplicate-terminal / protocol failure")
	}
}

func TestStream_FrameBoundOversizeRejected(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeOversize}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "osz", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "osz", MaxStreamFrame: 64,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected oversize/frame bound failure")
	}
}

func TestStream_CancelTimeoutHardStops(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeBlockedCancel, SlowWait: time.Hour}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "cto", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "cto", CancelTimeout: 20 * time.Millisecond,
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = stream.Recv(context.Background())
	})
	res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Err == nil && res.Mode == "" {
		t.Fatal("expected cancel result")
	}
	_ = stream.Close()
	wg.Wait()
}

func TestClassifiedError_UnwrapRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	err := &adapter.ClassifiedError{Code: "transport", Message: "boom", Retryable: true}
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatal("retryable pre-output must unwrap to ErrRecoverablePreOutput")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("core seam must recognize classified pre-output")
	}
	post := &adapter.ClassifiedError{Code: "provider", Message: "x", Retryable: false, OutputCommitted: true}
	if aipapi.IsRecoverablePreOutput(post) {
		t.Fatal("post-output must not be recoverable")
	}
}

func TestClassifiedError_CapabilityProjection(t *testing.T) {
	t.Parallel()
	// The shared SDK repair is the verified sole current producer of the
	// pre-output non-retryable failed_precondition shape, restricted to
	// ErrCapabilityReject; exactly that shape bridges to *aipapi.RejectError.
	cases := []struct {
		name          string
		err           *adapter.ClassifiedError
		wantReject    bool
		wantRetryable bool
	}{
		{name: "capability", err: &adapter.ClassifiedError{Code: "failed_precondition", Message: "capability rejected token=sekret"}, wantReject: true},
		{name: "retryable failed_precondition", err: &adapter.ClassifiedError{Code: "failed_precondition", Message: "x", Retryable: true}, wantRetryable: true},
		{name: "committed failed_precondition", err: &adapter.ClassifiedError{Code: "failed_precondition", Message: "x", OutputCommitted: true}},
		{name: "internal", err: &adapter.ClassifiedError{Code: "internal", Message: "upstream execution failed"}},
		{name: "unavailable", err: &adapter.ClassifiedError{Code: "unavailable", Message: "down"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := aipapi.IsReject(tc.err); got != tc.wantReject {
				t.Fatalf("IsReject=%v want %v", got, tc.wantReject)
			}
			if got := aipapi.IsRecoverablePreOutput(tc.err); got != tc.wantRetryable {
				t.Fatalf("IsRecoverablePreOutput=%v want %v", got, tc.wantRetryable)
			}
			if !tc.wantReject {
				return
			}
			var rej *aipapi.RejectError
			if !errors.As(tc.err, &rej) {
				t.Fatal("must expose *aipapi.RejectError")
			}
			if rej.Reason != "capability rejected" || rej.Error() != "capability rejected" {
				t.Fatalf("reason=%q must be the bounded constant, never plugin text", rej.Reason)
			}
			if !errors.Is(tc.err, aipapi.ErrCapabilityReject) {
				t.Fatal("must match ErrCapabilityReject")
			}
		})
	}
}

func TestClassifiedError_PermissionDeniedProjection(t *testing.T) {
	t.Parallel()
	const wireReason = "backend permission denied"
	const canary = "third_party_extra_usage canary-4f8c1d"
	preOutput := &adapter.ClassifiedError{
		Code:    string(backendplugin.ErrorCodePermissionDenied),
		Message: canary,
	}
	if aipapi.IsRecoverablePreOutput(preOutput) {
		t.Fatal("permission_denied must never become retry/failover eligible")
	}
	if aipapi.IsReject(preOutput) {
		t.Fatal("permission_denied must not be mislabeled as a capability reject")
	}
	if !prerequest.IsRejected(preOutput) {
		t.Fatal("permission_denied must expose the existing 403 reject branch")
	}
	var rej *prerequest.RejectError
	if !errors.As(preOutput, &rej) {
		t.Fatal("must expose *prerequest.RejectError")
	}
	if rej.Message != wireReason {
		t.Fatalf("reject reason=%q want the bounded constant %q", rej.Message, wireReason)
	}
	out := execerr.ClassifyExecute(preOutput)
	if out.Kind != execerr.KindClientReject {
		t.Fatalf("kind=%v want KindClientReject", out.Kind)
	}
	if out.Status != http.StatusForbidden {
		t.Fatalf("status=%d want %d", out.Status, http.StatusForbidden)
	}
	if out.Message != wireReason {
		t.Fatalf("wire message=%q want %q", out.Message, wireReason)
	}
	if strings.Contains(out.Message, "canary-4f8c1d") {
		t.Fatalf("plugin text reached the wire: %q", out.Message)
	}
	// The neighbouring failed_precondition bridge must stay a capability reject.
	capability := &adapter.ClassifiedError{Code: string(backendplugin.ErrorCodeFailedPrecondition), Message: canary}
	if !aipapi.IsReject(capability) || prerequest.IsRejected(capability) {
		t.Fatal("failed_precondition must keep the capability-reject bridge")
	}
}

// permissionDeniedTerminal mirrors a connector's typed terminal: ForwardExecute
// publishes it as a failure terminal and returns the same error from Execute, so
// the Execute boundary must preserve the connector's class.
type permissionDeniedTerminal struct {
	message   string
	retryable bool
}

func (e *permissionDeniedTerminal) Error() string { return e.message }

func (e *permissionDeniedTerminal) TerminalPluginError() *backendplugin.PluginError {
	return &backendplugin.PluginError{
		Code:      backendplugin.ErrorCodePermissionDenied,
		Message:   e.message,
		Retryable: e.retryable,
	}
}

// TestClassifyExecuteError_PermissionDeniedTerminalKeepsClass proves the
// Execute-boundary classification keeps a connector's typed permission denial
// instead of re-deriving retryable transport death. The same failure reaches
// Recv through the failure terminal too, so both paths must agree.
func TestClassifyExecuteError_PermissionDeniedTerminalKeepsClass(t *testing.T) {
	t.Parallel()
	const canary = "third_party_extra_usage canary-7b1e"
	failure := adapter.ClassifyExecuteError(&permissionDeniedTerminal{message: canary}, false)
	if failure == nil {
		t.Fatal("expected a classified execute failure")
	}
	classified := failure.ToClassifiedError()
	if classified.Code != string(backendplugin.ErrorCodePermissionDenied) || classified.Retryable || classified.OutputCommitted {
		t.Fatalf("classification=%+v want non-retryable permission_denied", classified)
	}
	if aipapi.IsRecoverablePreOutput(classified) {
		t.Fatal("Execute-boundary permission_denied must not become retry/failover eligible")
	}
	out := execerr.ClassifyExecute(classified)
	if out.Kind != execerr.KindClientReject || out.Status != http.StatusForbidden || out.Message != "backend permission denied" {
		t.Fatalf("outcome=%+v want KindClientReject 403 with the bounded message", out)
	}
	if strings.Contains(out.Message, "canary-7b1e") {
		t.Fatalf("plugin text reached the wire: %q", out.Message)
	}
	committed := adapter.ClassifyExecuteError(&permissionDeniedTerminal{message: canary}, true).ToClassifiedError()
	if committed.Retryable || !committed.OutputCommitted || prerequest.IsRejected(committed) {
		t.Fatalf("committed classification=%+v must stay post-output and unchanged", committed)
	}
	// A retryable permission terminal keeps the pre-existing Execute-boundary
	// classification: it must not be narrowed into the non-retryable 403 path.
	retryable := adapter.ClassifyExecuteError(&permissionDeniedTerminal{message: canary, retryable: true}, false).ToClassifiedError()
	if retryable.Code != "transport" || !retryable.Retryable || prerequest.IsRejected(retryable) {
		t.Fatalf("retryable classification=%+v must keep the current retryable shape", retryable)
	}
}

// TestClassifiedError_PermissionDeniedKeepsCurrentWhenRetryableOrCommitted pins
// the two shapes the 403 bridge must not touch: a retryable pre-output failure
// keeps its recoverable class, and a post-output failure keeps the internal
// error shape.
func TestClassifiedError_PermissionDeniedKeepsCurrentWhenRetryableOrCommitted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       *adapter.ClassifiedError
		retryable bool
	}{
		{
			name:      "retryable pre-output",
			err:       &adapter.ClassifiedError{Code: string(backendplugin.ErrorCodePermissionDenied), Message: "lease canary", Retryable: true},
			retryable: true,
		},
		{
			name: "post-output",
			err:  &adapter.ClassifiedError{Code: string(backendplugin.ErrorCodePermissionDenied), Message: "lease canary", OutputCommitted: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if prerequest.IsRejected(tc.err) {
				t.Fatal("only the pre-output non-retryable shape may bridge to the 403 reject branch")
			}
			if got := aipapi.IsRecoverablePreOutput(tc.err); got != tc.retryable {
				t.Fatalf("IsRecoverablePreOutput=%v want %v", got, tc.retryable)
			}
			out := execerr.ClassifyExecute(tc.err)
			if out.Kind != execerr.KindInternalError || out.Status != http.StatusInternalServerError || out.Message != execerr.InternalWireMessage {
				t.Fatalf("outcome=%+v, want the unchanged internal error shape", out)
			}
		})
	}
}

func TestStream_ExactReasoningEventPreservedAtMinor3(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeExactReasoning}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems, backendplugin.FeatureExactOpenResponsesFields},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "exact3", FactoryKind: "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "exact3", Negotiation: neg})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventReasoningPart || ev.Reasoning == nil {
		t.Fatalf("event=%#v", ev)
	}
	if !ev.Reasoning.SummaryPresent || !ev.Reasoning.ContentPresent || !ev.Reasoning.EncryptedContentPresent {
		t.Fatalf("exact reasoning presence lost: %#v", ev.Reasoning)
	}
	if string(ev.Reasoning.EncryptedContent) != "null" {
		t.Fatalf("encrypted_content=%s", ev.Reasoning.EncryptedContent)
	}
}

func TestStream_ExactReasoningEventRejectedAtOldMinor(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeExactReasoning}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "exact-old", FactoryKind: "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "exact-old", Negotiation: neg})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected fail-closed rejection of exact reasoning event on old minor")
	}
}
