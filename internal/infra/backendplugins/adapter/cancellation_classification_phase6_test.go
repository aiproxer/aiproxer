package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestPhase6_LegacyCancel_ClassifiedAsCanceledNotTransportDeath proves that intentional
// legacy transport death caused by Cancel() is classified as ExecuteFailureCanceled
// (non-retryable, no generation invalidation) rather than retryable transport death.
func TestPhase6_LegacyCancel_ClassifiedAsCanceledNotTransportDeath(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeSlowOutput, SlowWait: 1 * time.Second}
	neg := backendplugin.Negotiation{
		Compatible: true,
		// No cancellation handshake feature -> legacy connector fallback
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:    "legacy-cancel",
		FactoryKind:   "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated bool
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "legacy-cancel",
		Negotiation:          neg,
		InvalidateGeneration: func() { invalidated = true },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	// Cancel stream before reading to trigger intentional legacy transport cancel
	res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "user stopped"})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("res.Mode = %v, want %v", res.Mode, aipapi.CancelModeTransport)
	}

	// Recv should receive the error from the canceled stream
	var recvErr error
	for {
		_, err := stream.Recv(context.Background())
		if err != nil {
			recvErr = err
			break
		}
	}
	if recvErr == nil || errors.Is(recvErr, io.EOF) {
		t.Fatalf("expected error on canceled stream, got: %v", recvErr)
	}

	// Must NOT be classified as recoverable pre-output!
	if aipapi.IsRecoverablePreOutput(recvErr) {
		t.Fatalf("intentional legacy cancellation was classified as recoverable pre-output: %v", recvErr)
	}

	// Must NOT invalidate connector generation for intentional cancel
	if invalidated {
		t.Fatal("intentional legacy cancellation must NOT invalidate connector generation")
	}

	if ce, ok := errors.AsType[*adapter.ClassifiedError](recvErr); ok {
		if ce.Retryable {
			t.Fatalf("classified error has Retryable=true on canceled stream: %+v", ce)
		}
		if ce.Code != "canceled" {
			t.Fatalf("classified error code = %q, want 'canceled'", ce.Code)
		}
	}
}

// TestPhase6_ExecuteFailureCanceled_ToClassifiedError_NotRetryable verifies that
// ExecuteFailureCanceled always produces Retryable=false regardless of OutputCommitted.
func TestPhase6_ExecuteFailureCanceled_ToClassifiedError_NotRetryable(t *testing.T) {
	t.Parallel()

	efUncommitted := &adapter.ExecuteFailureError{
		Kind:            adapter.ExecuteFailureCanceled,
		Err:             context.Canceled,
		OutputCommitted: false,
	}
	ce1 := efUncommitted.ToClassifiedError()
	if ce1.Retryable {
		t.Fatalf("uncommitted canceled error must have Retryable=false, got %+v", ce1)
	}
	if ce1.Code != "canceled" {
		t.Fatalf("uncommitted canceled error code = %q, want 'canceled'", ce1.Code)
	}
	if ce1.Unwrap() != nil {
		t.Fatalf("uncommitted canceled error Unwrap() must be nil, got: %v", ce1.Unwrap())
	}
	if aipapi.IsRecoverablePreOutput(ce1) {
		t.Fatalf("IsRecoverablePreOutput must be false for canceled error")
	}

	efCommitted := &adapter.ExecuteFailureError{
		Kind:            adapter.ExecuteFailureCanceled,
		Err:             context.Canceled,
		OutputCommitted: true,
	}
	ce2 := efCommitted.ToClassifiedError()
	if ce2.Retryable {
		t.Fatalf("committed canceled error must have Retryable=false, got %+v", ce2)
	}
}

// TestPhase6_ForcedAbort_ClassifiedAsCanceledNotTransportDeath verifies that
// when graceful cancellation times out and is forced, it is classified as cancellation
// rather than retryable transport death.
func TestPhase6_ForcedAbort_ClassifiedAsCanceledNotTransportDeath(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeSlowOutput, SlowWait: 1 * time.Second}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
		EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID:    "force-cancel",
		FactoryKind:   "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated bool
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "force-cancel",
		Negotiation:          neg,
		CancelTimeout:        10 * time.Millisecond,
		InvalidateGeneration: func() { invalidated = true },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("res.Mode = %v, want %v", res.Mode, aipapi.CancelModeTransport)
	}

	_, recvErr := stream.Recv(context.Background())
	if recvErr == nil || errors.Is(recvErr, io.EOF) {
		t.Fatalf("expected error on forced canceled stream, got: %v", recvErr)
	}
	if aipapi.IsRecoverablePreOutput(recvErr) {
		t.Fatalf("forced cancel must NOT be recoverable pre-output: %v", recvErr)
	}
	if invalidated {
		t.Fatal("forced cancel must NOT invalidate generation")
	}
}

// TestPhase6_ClassifyExecuteError_GRPCCanceled verifies grpc status codes.
func TestPhase6_ClassifyExecuteError_GRPCCanceled(t *testing.T) {
	t.Parallel()
	grpcCanceled := status.Error(codes.Canceled, "context canceled")
	fe := adapter.ClassifyExecuteError(grpcCanceled, false)
	if fe.Kind != adapter.ExecuteFailureCanceled {
		t.Fatalf("fe.Kind = %v, want ExecuteFailureCanceled", fe.Kind)
	}
	ce := fe.ToClassifiedError()
	if ce.Retryable {
		t.Fatalf("ce.Retryable must be false, got true")
	}
}
