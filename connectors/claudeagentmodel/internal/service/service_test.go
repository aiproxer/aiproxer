package service

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestService_DescribePosture(t *testing.T) {
	t.Parallel()
	desc, err := New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if desc.PluginID != PluginID || desc.ProtocolMajor != 1 || desc.ProtocolMinor != backendplugin.ProtocolMinorReasoningEffortValues {
		t.Fatalf("descriptor=%+v", desc)
	}
	if len(desc.Features) != 5 || desc.Features[0].Name != backendplugin.FeatureOrderedItems ||
		desc.Features[1].Name != backendplugin.FeatureProxyOwnedSessionID ||
		desc.Features[2].Name != backendplugin.FeatureCancellationHandshake ||
		desc.Features[3].Name != backendplugin.FeatureReasoningEffortValues ||
		desc.Features[4].Name != backendplugin.FeatureExactOpenResponsesFields {
		t.Fatalf("features=%+v", desc.Features)
	}
	factory := desc.Factories[0]
	if factory.Kind != FactoryKind || factory.CredentialMode != backendplugin.CredentialModeOAuthUser ||
		factory.AccessScope != backendplugin.AccessScopeLocalOnly || !factory.Experimental ||
		!factory.StaticCapabilities.Streaming || !factory.StaticCapabilities.Tools ||
		!factory.StaticCapabilities.OrderedItems || factory.StaticCapabilities.ItemReferences ||
		factory.StaticCapabilities.ParallelToolCalls || !factory.SupportsDynamicInventory {
		t.Fatalf("factory=%+v", factory)
	}
}

func TestService_ConformanceWithFakeSidecar(t *testing.T) {
	deps := testDeps(t)
	svc := &Service{deps: &deps}
	cfg := testConfig(t)
	configYAML := []byte("executable: " + cfg.Executable + "\nadmin_policy: honor\ndefault_workspace: " + cfg.DefaultWorkspace + "\n")
	// The generic execute probe sends a tool-less request. The tool envelope is
	// proved by the connector's own stream suite instead.
	report := conformance.RunWith(context.Background(), svc, conformance.Options{
		ConfigYAML:  configYAML,
		SampleModel: "anthropic/sonnet",
		SkipExecute: true,
	})
	if !report.Ok() {
		t.Fatalf("conformance failures=%v", report.Failures())
	}
}

func TestService_ManifestAndReleasePosture(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "manifest", "template.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		PluginID string `json:"plugin_id"`
		Exports  []struct {
			Kind           string `json:"kind"`
			Experimental   bool   `json:"experimental"`
			CredentialMode string `json:"credential_mode"`
			AccessScope    string `json:"access_scope"`
			ProcessSharing string `json:"process_sharing"`
			ExecutionClass string `json:"execution_class"`
		} `json:"exports"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.PluginID != PluginID || len(manifest.Exports) != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	export := manifest.Exports[0]
	if export.Kind != FactoryKind || !export.Experimental || export.CredentialMode != "oauth_user" ||
		export.AccessScope != "local_only" || export.ProcessSharing != "per_instance" ||
		export.ExecutionClass != "agent_runtime" {
		t.Fatalf("export=%+v", export)
	}
	if desc, err := New().Describe(context.Background()); err != nil {
		t.Fatal(err)
	} else if desc.Version != ConnectorVersion {
		t.Fatalf("descriptor version=%q want %q", desc.Version, ConnectorVersion)
	}
	if !strings.Contains(string(body), `"version": "`+ConnectorVersion+`"`) {
		t.Fatalf("manifest version does not match %q", ConnectorVersion)
	}
	release, err := os.ReadFile(filepath.Join(root, "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"version: " + ConnectorVersion,
		"factory_kind: " + FactoryKind,
		"plugin_id: " + PluginID,
		"manifest_template: manifest/template.backendplugin.json",
		"private_companions:",
		"sidecar",
	} {
		if !strings.Contains(string(release), want) {
			t.Fatalf("release.yaml missing %q", want)
		}
	}
}

func TestService_DisabledExampleSpawnsNothing(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	example, err := os.ReadFile(filepath.Join(root, "..", "..", "config", "examples", "claude-agent-model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"enabled: false",
		"kind: " + FactoryKind,
		"admin_policy: refuse",
		"executable: /absolute/path/to/claude",
	} {
		if !strings.Contains(string(example), required) {
			t.Fatalf("disabled example missing %q", required)
		}
	}
}

func TestIsolation_NoRootInternalImport(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), `"github.com/aiproxer/aiproxer/internal/`) {
			t.Errorf("%s imports root internal", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIsolation_RootModuleDoesNotDependOnConnector(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "claudeagentmodel") {
		t.Fatal("root module must not depend on the optional connector")
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

type negotiatedStream struct {
	ctx            context.Context
	frames         []backendplugin.ClientFrame
	index          int
	sent           []backendplugin.ServerFrame
	endAfterFrames bool
}

func (s *negotiatedStream) Context() context.Context { return s.ctx }

func (s *negotiatedStream) Recv() (backendplugin.ClientFrame, error) {
	if s.index >= len(s.frames) {
		if s.endAfterFrames {
			return backendplugin.ClientFrame{}, io.EOF
		}
		<-s.ctx.Done()
		return backendplugin.ClientFrame{}, s.ctx.Err()
	}
	frame := s.frames[s.index]
	s.index++
	return frame, nil
}

func (s *negotiatedStream) Send(frame backendplugin.ServerFrame) error {
	s.sent = append(s.sent, frame)
	return nil
}

func (s *negotiatedStream) Negotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		EnabledFeatures: []string{
			backendplugin.FeatureOrderedItems,
			backendplugin.FeatureProxyOwnedSessionID,
			backendplugin.FeatureCancellationHandshake,
			backendplugin.FeatureReasoningEffortValues,
		},
	}
}

func TestService_NegotiatedCancellationDoesNotHang(t *testing.T) {
	eng := testEngine(t, "slow")
	inv := textInvocation("cancel-1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &negotiatedStream{ctx: ctx, frames: []backendplugin.ClientFrame{
		{Kind: backendplugin.ClientFrameStart, InstanceID: "i", Invocation: inv},
		{Kind: backendplugin.ClientFrameCancel, CancelReason: backendplugin.CancelReasonClient},
	}}
	done := make(chan error, 1)
	go func() { done <- (&instance{eng: eng}).Execute(stream) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("negotiated cancellation hung")
	}
	if len(stream.sent) == 0 || stream.sent[0].Kind != backendplugin.ServerFrameAccepted {
		t.Fatalf("frames=%v", stream.sent)
	}
}

func textInvocation(id string) *backendplugin.Invocation {
	text := "hi"
	return &backendplugin.Invocation{
		RequestID: id,
		AttemptID: "a", ALegID: "a", BLegID: "b",
		CanonicalModelID: "anthropic/sonnet",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
}

func executeOnce(t *testing.T, eng *engine, inv *backendplugin.Invocation) (*negotiatedStream, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream := &negotiatedStream{
		ctx:            ctx,
		endAfterFrames: true,
		frames:         []backendplugin.ClientFrame{{Kind: backendplugin.ClientFrameStart, InstanceID: "i", Invocation: inv}},
	}
	err := (&instance{eng: eng}).Execute(stream)
	return stream, err
}

func terminalFrame(t *testing.T, frames []backendplugin.ServerFrame) *backendplugin.Terminal {
	t.Helper()
	var terminal *backendplugin.Terminal
	for _, frame := range frames {
		if frame.Kind == backendplugin.ServerFrameTerminal {
			terminal = frame.Terminal
		}
	}
	if terminal == nil {
		t.Fatalf("no terminal frame in %+v", frames)
	}
	return terminal
}

func TestService_PrematureExitBeforeOutputIsTerminalFailure(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "exit_early")
	stream, err := executeOnce(t, eng, textInvocation("exit-early"))
	if err == nil {
		t.Fatal("a clean child exit before a terminal event was treated as success")
	}
	terminal := terminalFrame(t, stream.sent)
	if terminal.Status != backendplugin.TerminalFailure || terminal.Error == nil {
		t.Fatalf("terminal=%+v", terminal)
	}
	if terminal.Error.Code != backendplugin.ErrorCodeProtocolViolation {
		t.Fatalf("code=%v message=%q", terminal.Error.Code, terminal.Error.Message)
	}
}

func TestService_PrematureExitAfterPartialOutputIsTerminalFailure(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "exit_partial")
	stream, err := executeOnce(t, eng, textInvocation("exit-partial"))
	if err == nil {
		t.Fatal("a clean child exit after partial output was treated as success")
	}
	terminal := terminalFrame(t, stream.sent)
	if terminal.Status != backendplugin.TerminalFailure || terminal.Error == nil || !terminal.Error.OutputCommitted {
		t.Fatalf("terminal=%+v", terminal)
	}
	sawPartial := false
	for _, frame := range stream.sent {
		if frame.Event != nil && frame.Event.Delta != nil && *frame.Event.Delta == "partial" {
			sawPartial = true
		}
	}
	if !sawPartial {
		t.Fatalf("partial output was not forwarded: %+v", stream.sent)
	}
}

func TestService_GenerationControlsAtInvocationBoundary(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "echo_start")
	effort := "high"
	maxTokens := uint32(64)
	inv := textInvocation("controls")
	inv.Options.ReasoningEffort = &effort
	inv.Options.MaxOutputTokens = &maxTokens
	stream, err := executeOnce(t, eng, inv)
	if err != nil {
		t.Fatal(err)
	}
	if terminal := terminalFrame(t, stream.sent); terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal=%+v", terminal)
	}
	var warning, delta string
	for _, frame := range stream.sent {
		if frame.Event == nil {
			continue
		}
		switch frame.Event.Kind {
		case backendplugin.EventWarning:
			if frame.Event.Warning != nil {
				warning = *frame.Event.Warning
			}
		case backendplugin.EventTextDelta:
			if frame.Event.Delta != nil {
				delta = *frame.Event.Delta
			}
		}
	}
	if !strings.Contains(warning, "max_output_tokens") {
		t.Fatalf("unsupported control was not disclosed: %q", warning)
	}
	if delta != "effort=high;model=sonnet" {
		t.Fatalf("validated effort was not forwarded: %q", delta)
	}
}

func TestService_StrictGenerationControlsRejectBeforeTurn(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "echo_start")
	eng.cfg.StrictSemantics = true
	maxTokens := uint32(64)
	inv := textInvocation("strict-controls")
	inv.Options.MaxOutputTokens = &maxTokens
	stream, err := executeOnce(t, eng, inv)
	if err == nil {
		t.Fatal("strict mode admitted an unsupported generation control")
	}
	terminal := terminalFrame(t, stream.sent)
	if terminal.Status != backendplugin.TerminalFailure || terminal.Error == nil ||
		terminal.Error.Code != backendplugin.ErrorCodeFailedPrecondition {
		t.Fatalf("terminal=%+v", terminal)
	}
	for _, frame := range stream.sent {
		if frame.Event != nil && frame.Event.Kind == backendplugin.EventTextDelta {
			t.Fatalf("strict reject produced model output: %+v", frame)
		}
	}
}
