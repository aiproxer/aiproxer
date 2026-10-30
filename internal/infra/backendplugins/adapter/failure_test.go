package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestPreOutput_ClassifiedRetryable(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeProcessExit}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "pre", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated bool
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "pre",
		InvalidateGeneration: func() { invalidated = true },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected classified pre-output error")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("%T %v", err, err)
	}
	if ce.OutputCommitted || !ce.Retryable {
		t.Fatalf("%+v", ce)
	}
	if !invalidated {
		t.Fatal("crash must invalidate generation")
	}
}

type fakeProviderCancelInstance struct {
	backendplugin.ConfiguredInstance
}

func (f *fakeProviderCancelInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
	}, nil
}

func (f *fakeProviderCancelInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (f *fakeProviderCancelInstance) Close(context.Context) error {
	return nil
}

func (f *fakeProviderCancelInstance) Execute(stream backendplugin.ExecuteStream) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if frame.Kind == backendplugin.ClientFrameStart {
			_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
		}
		if frame.Kind == backendplugin.ClientFrameCancel {
			_ = stream.Send(backendplugin.ServerFrame{
				Kind:     backendplugin.ServerFrameCancelOutcome,
				Sequence: 1,
				CancelOutcome: &backendplugin.CancelOutcome{
					Acknowledged: true,
					Mode:         backendplugin.CancelModeProvider,
					Reason:       frame.CancelReason,
				},
			})
			_ = stream.Send(backendplugin.ServerFrame{
				Kind:     backendplugin.ServerFrameTerminal,
				Sequence: 2,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
			})
			return nil
		}
	}
}

func TestCancel_ProviderVsTransport(t *testing.T) {
	t.Parallel()

	t.Run("unacknowledged returns none", func(t *testing.T) {
		t.Parallel()
		fake := &testkit.FakeService{Mode: testkit.ModeBlockedCancel}
		neg := backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		}
		inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
			InstanceID:    "cancel-transport",
			FactoryKind:   "fake",
			Negotiation:   neg,
			RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		profile, _ := inst.Resolve(context.Background(), nil)
		br := adapter.Build(inst, profile, adapter.Options{InstanceID: "cancel-transport", Negotiation: neg})
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := br.Backend.Open(ctx, testCall(), testCand())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close(); cancel() })
		res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeNone {
			t.Fatalf("res.Mode = %v, want %v", res.Mode, aipapi.CancelModeNone)
		}
	})

	t.Run("reported provider outcome returns provider", func(t *testing.T) {
		t.Parallel()
		inst := &fakeProviderCancelInstance{}
		neg := backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		}
		br := adapter.Build(inst, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "cancel-provider", Negotiation: neg})
		stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		res := stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if res.Mode != aipapi.CancelModeProvider {
			t.Fatalf("res.Mode = %v, want %v", res.Mode, aipapi.CancelModeProvider)
		}
	})
}

func TestPostOutput_DiagnosticAndUsageDoNotCommit(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "diag", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "diag"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ms, ok := stream.(interface{ OutputCommitted() bool })
	if !ok {
		t.Fatalf("stream %T missing OutputCommitted", stream)
	}
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventUsageDelta && !ms.OutputCommitted() && aipapi.OutputCommitted(ev) {
			t.Fatal("usage must not commit")
		}
	}
}
