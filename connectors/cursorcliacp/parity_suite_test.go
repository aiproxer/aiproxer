package cursorcliacp_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/cursorcliacp/internal/product"
	"github.com/aiproxer/aiproxer/connectors/cursorcliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func cursorInventory() modelinventory.Provider {
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: []modelinventory.Model{{
			CanonicalID: "cursor/composer-2", NativeID: "composer-2", DisplayName: "composer-2",
		}},
	}
}

func TestParity_KindLocalOnlyLoginPosture(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fac := d.Factories[0]
	if fac.Kind != "cursorcliacp" || fac.AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("%+v", fac)
	}
	if fac.CredentialMode != backendplugin.CredentialModeNone {
		t.Fatalf("cursor login posture credential=%s", fac.CredentialMode)
	}
	eng := product.NewWithStarter(product.Config{
		ConnectorConfig: acp.ConnectorConfig{DefaultWorkspace: t.TempDir(), Model: "composer-2"},
		Inventory:       cursorInventory(),
	}, &acp.ScriptedStarter{})
	if _, ok := eng.Caps[aipapi.CapabilityStreaming]; !ok {
		t.Fatalf("caps=%v", eng.Caps)
	}
	if _, ok := eng.Caps[aipapi.CapabilityReasoning]; !ok {
		t.Fatalf("caps=%v", eng.Caps)
	}
}

func TestParity_ConfigEnvCommandInventory(t *testing.T) {
	t.Parallel()
	raw := []byte(`
executable: agent
model: composer-2
default_workspace: /tmp/ws
auto_accept: true
trust_workspace: true
cursor_api_endpoint: https://example.invalid
extra_args: ["--verbose"]
idle_timeout_seconds: 1.5
stale_kill_delay_seconds: 0.2
`)
	cfg, err := service.ParseConfigYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoAccept || !cfg.TrustWorkspace || cfg.CursorAPIEndpoint == "" || len(cfg.ExtraArgs) != 1 {
		t.Fatalf("%+v", cfg)
	}
	if cfg.IdleTimeoutS <= 0 || cfg.StaleKillDelayS <= 0 {
		t.Fatalf("timers %+v", cfg)
	}
}

func TestParity_ExeCacheInstanceIsolation(t *testing.T) {
	t.Parallel()
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	exe1 := filepath.Join(dir1, "agent.exe")
	exe2 := filepath.Join(dir2, "agent.exe")
	if err := os.WriteFile(exe1, []byte("1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe2, []byte("2"), 0o755); err != nil {
		t.Fatal(err)
	}
	c1 := &acp.ExecutableCache{}
	c2 := &acp.ExecutableCache{}
	e1, err := product.New(product.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: exe1, Model: "composer-2", DefaultWorkspace: dir1},
		ExeCache:        c1,
		Inventory:       cursorInventory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := product.New(product.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: exe2, Model: "composer-2", DefaultWorkspace: dir2},
		ExeCache:        c2,
		Inventory:       cursorInventory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.ExecutableCache() == e2.ExecutableCache() {
		t.Fatal("caches must be distinct instances")
	}
	if e1.ResolvedExecutable() != exe1 || e2.ResolvedExecutable() != exe2 {
		t.Fatalf("exe paths %q %q", e1.ResolvedExecutable(), e2.ResolvedExecutable())
	}
	c1.Reset()
	if _, ok := c2.CheckExecutable(exe2); !ok {
		t.Fatal("reset of cache1 must not clear cache2")
	}
}

func TestParity_ConformanceAndScriptedStreamCancel(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	agent := acp.NewScriptedStdioAgent()
	svc := &service.Service{
		Starter:   &acp.ScriptedStarter{},
		Inventory: cursorInventory(),
	}
	fakeBin := filepath.Join(ws, "agent.exe")
	if err := os.WriteFile(fakeBin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte("executable: " + strconvQuote(fakeBin) + "\nmodel: composer-2\ndefault_workspace: " + strconvQuote(ws) + "\n")
	rep := conformance.RunWith(context.Background(), svc, conformance.Options{
		ConfigYAML:              yaml,
		SampleModel:             "composer-2",
		DisableUsageRequirement: true,
		VisionInputOnly:         true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}

	svc.Starter = &acp.ScriptedStarter{Agent: agent}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "c1", ConfigYAML: yaml,
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
	models, err := inst.ListModels(context.Background(), 4)
	if err != nil || len(models.Models) == 0 {
		t.Fatalf("inventory %+v err=%v", models, err)
	}
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "composer-2",
		SafeMetadata:     map[string]string{"session.id": "sess-1"},
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "c1", Invocation: &inv,
	}}}
	if err := inst.Execute(ms); err != nil {
		t.Fatal(err)
	}
	var sawText, sawReasoning bool
	for _, fr := range ms.outbox {
		if fr.Kind == backendplugin.ServerFrameEvent && fr.Event != nil {
			if fr.Event.Kind == backendplugin.EventTextDelta && fr.Event.Delta != nil && strings.Contains(*fr.Event.Delta, "ok-from-scripted-acp") {
				sawText = true
			}
			if fr.Event.Kind == backendplugin.EventReasoningDelta {
				sawReasoning = true
			}
		}
	}
	if !sawText || !sawReasoning {
		t.Fatalf("text=%v reasoning=%v out=%+v", sawText, sawReasoning, ms.outbox)
	}
	if !agent.PromptSeen.Load() {
		t.Fatal("scripted agent did not see session/prompt")
	}
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
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
