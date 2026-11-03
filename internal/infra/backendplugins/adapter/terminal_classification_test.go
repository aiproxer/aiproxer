package adapter_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
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

// TestPluginTerminalPermissionDeniedMapsToClientReject proves a non-retryable
// connector permission decision reaches the client as the existing 403 reject
// branch with a bounded connector-owned message, not as a generic internal
// error. The plugin's own diagnostic text must never cross the client boundary,
// and the attempt must not become retry/failover eligible.
func TestPluginTerminalPermissionDeniedMapsToClientReject(t *testing.T) {
	t.Parallel()
	const canary = "third_party_extra_usage canary-9a2b"
	fake := &testkit.FakeService{
		Mode: testkit.ModeValid,
		TerminalError: &backendplugin.PluginError{
			Code:      backendplugin.ErrorCodePermissionDenied,
			Message:   canary,
			Retryable: false,
		},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "terminal-permission", FactoryKind: "fake",
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
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "terminal-permission"})
	t.Cleanup(func() { _ = br.Cleanup() })
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected the permission terminal at the host")
	}
	var classified *adapter.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("host error=%T %v, want *adapter.ClassifiedError", err, err)
	}
	if classified.Code != string(backendplugin.ErrorCodePermissionDenied) || classified.Retryable || classified.OutputCommitted {
		t.Fatalf("host classification=%+v, want non-retryable permission_denied without output commit", classified)
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatal("permission_denied must never be retry/failover eligible")
	}
	if !prerequest.IsRejected(err) {
		t.Fatal("permission_denied must expose the existing 403 reject branch")
	}
	out := execerr.ClassifyExecute(err)
	if out.Kind != execerr.KindClientReject || out.Status != http.StatusForbidden {
		t.Fatalf("outcome kind=%v status=%d, want KindClientReject 403", out.Kind, out.Status)
	}
	if out.Message != "backend permission denied" {
		t.Fatalf("wire message=%q want the bounded constant", out.Message)
	}
	if strings.Contains(out.Message, "canary-9a2b") {
		t.Fatalf("plugin text reached the wire: %q", out.Message)
	}
	// The terminal was already published: a second receive repeats the same
	// classified failure instead of inventing another terminal.
	if _, err = stream.Recv(context.Background()); err == nil {
		t.Fatal("a terminal failure must not be followed by a success event")
	}
	if got := fake.ExecuteCount.Load(); got != 1 {
		t.Fatalf("Execute calls=%d want exactly 1 (no retry)", got)
	}
}
