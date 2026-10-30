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

// TestPluginTerminalProviderTransientMapsAtTheAdapter proves the adapter maps a
// classified terminal into the host-visible pre-output classification instead
// of collapsing it to a generic internal failure. This fixture configures the
// fake instance in-process; the staged SIWC host test separately covers the
// executable-plugin boundary.
func TestPluginTerminalProviderTransientMapsAtTheAdapter(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{
		Mode: testkit.ModeValid,
		TerminalError: &backendplugin.PluginError{
			Code:      backendplugin.ErrorCodeProviderTransient,
			Message:   "provider temporarily unavailable",
			Retryable: true,
		},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "terminal-classification", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "terminal-classification"})
	t.Cleanup(func() { _ = br.Cleanup() })
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected the terminal failure at the host")
	}
	var classified *adapter.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("host error=%T %v, want *adapter.ClassifiedError", err, err)
	}
	if classified.Code != string(backendplugin.ErrorCodeProviderTransient) || !classified.Retryable || classified.OutputCommitted {
		t.Fatalf("host classification=%+v, want retryable provider_transient without output commit", classified)
	}
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatalf("host error %v must remain recoverable before output", err)
	}
}
