package security_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestAdversarial_PluginStderrNeverReachesClientSurface(t *testing.T) {
	t.Parallel()
	const poison = "STDERR_SECRET_sk-or-v1-LEAKME\x1b[31m\x00CONTROL"
	launcher := &processhost.TestLauncher{PID: 9101, StderrPayload: poison}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "stderr-adv",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "stderr-adv"},
		Model:      processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.LastProcess == nil || launcher.LastProcess.Stderr() == nil {
		t.Fatal("expected launched process stderr from processhost seam")
	}

	fake := &testkit.FakeService{Mode: testkit.ModeSecretTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "stderr-adv", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	// The process host owns the process-pipe drain (the launched plugin's
	// stderr is drained to discard for the whole process lifetime), so the
	// adapter seam is exercised with an equivalent stream: a plugin stderr
	// stream handed to the adapter must still stay off every client surface.
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "stderr-adv",
		Stderr:     io.NopCloser(strings.NewReader(poison)),
	})
	call := aipapi.Call{
		ID:         "stderr-req",
		Session:    aipapi.SessionRef{ALegID: "aleg"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}
	stream, err := br.Backend.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var gotErr error
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			gotErr = err
			break
		}
		blob := string(ev.Kind) + ev.Delta + ev.ErrorMessage + ev.WarningMessage + ev.AssistantRef
		if strings.Contains(blob, "LEAKME") || strings.Contains(blob, "STDERR_SECRET") || strings.Contains(blob, "\x1b") {
			t.Fatalf("event leaked stderr material: %+v", ev)
		}
	}
	if gotErr == nil || errors.Is(gotErr, io.EOF) {
		t.Fatal("expected terminal classified error")
	}
	msg := gotErr.Error()
	for _, bad := range []string{"LEAKME", "STDERR_SECRET", "sk-or-v1-LEAKME", "\x1b"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("client error leaked stderr %q via %q", bad, msg)
		}
	}
}
