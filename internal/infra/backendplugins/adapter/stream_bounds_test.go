package adapter_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
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
