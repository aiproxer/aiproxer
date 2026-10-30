package geminicliacp_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/product"
	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestParity_KindAuthConfigSecurity(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fac := d.Factories[0]
	if fac.Kind != "geminicliacp" || fac.AccessScope != backendplugin.AccessScopeLocalOnly || fac.CredentialMode != backendplugin.CredentialModeNone {
		t.Fatalf("%+v", fac)
	}
	cfg, err := service.ParseConfigYAML([]byte("model: gemini-2.5-flash\nauto_accept: true\ndefault_workspace: /tmp/g\nextra_args: [\"--debug\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoAccept || cfg.Model != "gemini-2.5-flash" || len(cfg.ExtraArgs) != 1 {
		t.Fatalf("%+v", cfg)
	}
}

func TestParity_ExeCacheInstanceIsolation(t *testing.T) {
	t.Parallel()
	dir1, dir2 := t.TempDir(), t.TempDir()
	exe1, exe2 := filepath.Join(dir1, "gemini.cmd"), filepath.Join(dir2, "gemini.cmd")
	for _, p := range []string{exe1, exe2} {
		if err := os.WriteFile(p, []byte("@echo off\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c1, c2 := &acp.ExecutableCache{}, &acp.ExecutableCache{}
	e1, err := product.New(product.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: exe1, Model: "gemini-2.5-flash", DefaultWorkspace: dir1},
		ExeCache:        c1,
	})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := product.New(product.Config{
		ConnectorConfig: acp.ConnectorConfig{Executable: exe2, Model: "gemini-2.5-flash", DefaultWorkspace: dir2},
		ExeCache:        c2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.ExecutableCache() == e2.ExecutableCache() || e1.ResolvedExecutable() == e2.ResolvedExecutable() {
		t.Fatal("instance isolation failed")
	}
	c1.Reset()
	if _, ok := c2.CheckExecutable(exe2); !ok {
		t.Fatal("cache2 polluted")
	}
}

func TestParity_ConformanceAndScriptedStream(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	fakeBin := filepath.Join(ws, "gemini.cmd")
	if err := os.WriteFile(fakeBin, []byte("@echo off\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent := acp.NewScriptedStdioAgent()
	svc := &service.Service{Starter: &acp.ScriptedStarter{
		SessionModels: []acp.ModelInfo{
			{ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
			{ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
		},
		SessionCurrentModel: "gemini-2.5-flash",
	}}
	yaml := []byte("executable: \"" + strings.ReplaceAll(fakeBin, `\`, `\\`) + "\"\nmodel: gemini-2.5-flash\ndefault_workspace: \"" + strings.ReplaceAll(ws, `\`, `\\`) + "\"\n")
	rep := conformance.RunWith(context.Background(), svc, conformance.Options{
		ConfigYAML: yaml, SampleModel: "gemini-2.5-flash",
		DisableUsageRequirement: true, VisionInputOnly: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "g1", ConfigYAML: yaml,
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
	if err != nil {
		t.Fatalf("inventory err=%v", err)
	}
	// Enumeration is authoritative: exactly the ACP-advertised models, no static union.
	if len(models.Models) != 2 {
		t.Fatalf("inventory len=%d, want 2 enumerated models: %+v", len(models.Models), models.Models)
	}
	seen := map[string]bool{}
	for _, m := range models.Models {
		seen[m.CanonicalModelID] = true
	}
	if !seen["google/gemini-2.5-pro"] || !seen["google/gemini-2.5-flash"] {
		t.Fatalf("inventory missing enumerated models: %+v", models.Models)
	}
	if models.InventorySource != "geminicliacp" {
		t.Fatalf("InventorySource = %q", models.InventorySource)
	}
	if models.FetchedUnixMS <= 0 {
		t.Fatalf("FetchedUnixMS = %d, want positive", models.FetchedUnixMS)
	}
	svc.Starter = &acp.ScriptedStarter{Agent: agent}
	inst2, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "g2", ConfigYAML: yaml,
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
		CanonicalModelID: "gemini-2.5-flash",
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	ms := &memStream{ctx: context.Background(), inbox: []backendplugin.ClientFrame{{
		Kind: backendplugin.ClientFrameStart, InstanceID: "g2", Invocation: &inv,
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
		t.Fatalf("sawText=%v prompt=%v", sawText, agent.PromptSeen.Load())
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
