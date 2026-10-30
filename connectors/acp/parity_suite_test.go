package acp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/acp/internal/service"
	"github.com/aiproxer/aiproxer/connectors/acp/internal/testemu"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestParity_ConfigSecurityInventoryRoutes(t *testing.T) {
	t.Parallel()
	svc := service.New()
	d, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Factories[0].CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("credential=%s", d.Factories[0].CredentialMode)
	}
	if d.Factories[0].AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("scope=%s", d.Factories[0].AccessScope)
	}
	_, err = service.ParseConfigYAML([]byte(`{}`))
	if err == nil {
		t.Fatal("empty config must fail")
	}
	cfg, err := service.ParseConfigYAML([]byte("base_url: http://127.0.0.1:9\nhttp_timeout: 2s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:9" || cfg.HTTPTimeout != "2s" {
		t.Fatalf("%+v", cfg)
	}
}

func TestParity_ConformanceAdvertised_WithTestEmu(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(testemu.NewHandler())
	t.Cleanup(srv.Close)
	svc := service.New()
	yaml := []byte("base_url: " + srv.URL + "\n")
	rep := conformance.RunWith(context.Background(), svc, conformance.Options{
		ConfigYAML:              yaml,
		SampleModel:             "acp/agent",
		DisableUsageRequirement: true,
		VisionInputOnly:         true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_StreamCancelLifecycle_TestEmu(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(testemu.NewHandler())
	t.Cleanup(srv.Close)
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "stream-1",
		ConfigYAML:  []byte("base_url: " + srv.URL + "\n"),
		Negotiation: backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })

	models, err := inst.ListModels(context.Background(), 8)
	if err != nil || len(models.Models) == 0 || models.Models[0].NativeModelID != "agent" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	prof, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prof.RoutePrefixes) != 1 || prof.RoutePrefixes[0] != "acp" {
		t.Fatalf("routes=%v", prof.RoutePrefixes)
	}
	if !prof.TransportCapabilities.Cancellation {
		t.Fatal("cancellation required")
	}

	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "acp/agent",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "stream-1", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		t.Fatal(err)
	}
	var sawText, sawReasoning, sawTerminal bool
	for _, fr := range ms.outbox {
		switch fr.Kind {
		case backendplugin.ServerFrameTerminal:
			sawTerminal = true
		case backendplugin.ServerFrameEvent:
			if fr.Event == nil {
				continue
			}
			switch fr.Event.Kind {
			case backendplugin.EventTextDelta:
				if fr.Event.Delta != nil && strings.Contains(*fr.Event.Delta, "ok") {
					sawText = true
				}
			case backendplugin.EventReasoningDelta:
				sawReasoning = true
			}
		}
	}
	if !sawText || !sawReasoning || !sawTerminal {
		t.Fatalf("text=%v reasoning=%v terminal=%v outbox=%+v", sawText, sawReasoning, sawTerminal, ms.outbox)
	}
}

func TestParity_GoldenProfileMatchesFixture(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "testdata", "parity_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		PluginID       string   `json:"plugin_id"`
		FactoryKind    string   `json:"factory_kind"`
		CredentialMode string   `json:"credential_mode"`
		AccessScope    string   `json:"access_scope"`
		RoutePrefixes  []string `json:"route_prefixes"`
		Streaming      bool     `json:"streaming"`
		Reasoning      bool     `json:"reasoning"`
		Cancellation   bool     `json:"cancellation"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	svc := service.New()
	d, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != want.PluginID || d.Factories[0].Kind != want.FactoryKind {
		t.Fatalf("desc mismatch %+v vs %+v", d, want)
	}
	if string(d.Factories[0].CredentialMode) != want.CredentialMode {
		t.Fatalf("credential %s", d.Factories[0].CredentialMode)
	}
	if string(d.Factories[0].AccessScope) != want.AccessScope {
		t.Fatalf("scope %s", d.Factories[0].AccessScope)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }
func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}
