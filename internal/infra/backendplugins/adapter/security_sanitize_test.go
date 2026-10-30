package adapter_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestAdversarial_UnknownPluginEventKindRejected(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeUnknownEventKind}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "unk", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "unk"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected protocol rejection for unknown plugin event kind")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("want ClassifiedError, got %T %v", err, err)
	}
	if ce.Code != "protocol" {
		t.Fatalf("code=%q", ce.Code)
	}
}

func TestAdversarial_PluginTerminalErrorRedactsSecrets(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeSecretTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "sec", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "sec"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected terminal classified error")
	}
	msg := err.Error()
	for _, needle := range []string{
		"sk-or-v1-openrouter-leak", "sk-ant-api03", "eyJhbGciOiJIUzI1NiJ9", "api_key=",
	} {
		if strings.Contains(msg, needle) {
			t.Fatalf("classified error leaked %q via %q", needle, msg)
		}
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Fatalf("expected [redacted] marker in %q", msg)
	}
}
