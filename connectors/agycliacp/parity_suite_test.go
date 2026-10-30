package agycliacp_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/agycliacp/internal/product"
	"github.com/aiproxer/aiproxer/connectors/agycliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func agyInventory() modelinventory.Provider {
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: []modelinventory.Model{{
			CanonicalID: "google/gemini-3.5-flash-high",
			NativeID:    "google/gemini-3.5-flash-high",
			DisplayName: "Gemini 3.5 Flash (High)",
		}},
	}
}

func TestParity_KindAuthConfigEnv(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fac := d.Factories[0]
	if fac.Kind != "agycliacp" || fac.AccessScope != backendplugin.AccessScopeLocalOnly || fac.CredentialMode != backendplugin.CredentialModeNone {
		t.Fatalf("%+v", fac)
	}
	cfg, err := service.ParseConfigYAML([]byte(`
model: google/gemini-3.5-flash-high
wrapper_executable: go-agy-acp-wrapper
agy_binary: agy
skip_permissions: true
timeout_seconds: 30
default_workspace: /tmp/agy
extra_args: ["--verbose"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AGYBinary != "agy" || cfg.WrapperExecutable == "" || cfg.TimeoutSeconds != 30 || cfg.SkipPermissions == nil || !*cfg.SkipPermissions {
		t.Fatalf("%+v", cfg)
	}
}

func TestParity_ExeCacheInstanceIsolation(t *testing.T) {
	t.Parallel()
	dir1, dir2 := t.TempDir(), t.TempDir()
	w1, w2 := filepath.Join(dir1, "go-agy-acp-wrapper.exe"), filepath.Join(dir2, "go-agy-acp-wrapper.exe")
	for _, p := range []string{w1, w2} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c1, c2 := &acp.ExecutableCache{}, &acp.ExecutableCache{}
	e1, err := product.New(product.Config{
		ConnectorConfig:   acp.ConnectorConfig{Model: "google/gemini-3.5-flash-high", DefaultWorkspace: dir1},
		WrapperExecutable: w1, ExeCache: c1, Inventory: agyInventory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := product.New(product.Config{
		ConnectorConfig:   acp.ConnectorConfig{Model: "google/gemini-3.5-flash-high", DefaultWorkspace: dir2},
		WrapperExecutable: w2, ExeCache: c2, Inventory: agyInventory(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.ExecutableCache() == e2.ExecutableCache() || e1.ResolvedExecutable() != w1 || e2.ResolvedExecutable() != w2 {
		t.Fatal("instance isolation failed")
	}
}

func TestParity_ConformanceAndScriptedStream(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	agent := acp.NewScriptedStdioAgent()
	svc := &service.Service{
		Starter:   &acp.ScriptedStarter{},
		Inventory: agyInventory(),
	}
	fakeWrap := filepath.Join(ws, "go-agy-acp-wrapper.exe")
	if err := os.WriteFile(fakeWrap, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := []byte("wrapper_executable: \"" + strings.ReplaceAll(fakeWrap, `\`, `\\`) + "\"\nmodel: google/gemini-3.5-flash-high\ndefault_workspace: \"" + strings.ReplaceAll(ws, `\`, `\\`) + "\"\n")
	rep := conformance.RunWith(context.Background(), svc, conformance.Options{
		ConfigYAML: yaml, SampleModel: "google/gemini-3.5-flash-high",
		DisableUsageRequirement: true, VisionInputOnly: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "a1", ConfigYAML: yaml,
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
	svc.Starter = &acp.ScriptedStarter{Agent: agent}
	inst2, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "a2", ConfigYAML: yaml,
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
	t.Cleanup(func() { _ = inst2.Close(context.Background()) })
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "google/gemini-3.5-flash-high",
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "a2", Invocation: &inv,
	}}}
	if err := inst2.Execute(ms); err != nil {
		t.Fatal(err)
	}
	var sawText bool
	for _, fr := range ms.outbox {
		if fr.Kind == backendplugin.ServerFrameEvent && fr.Event != nil && fr.Event.Kind == backendplugin.EventTextDelta {
			sawText = true
		}
	}
	if !sawText || !agent.PromptSeen.Load() {
		t.Fatalf("sawText=%v prompt=%v out=%+v", sawText, agent.PromptSeen.Load(), ms.outbox)
	}
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
