package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/bridge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func testDeps(t *testing.T) engineDeps {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "bridge", "testdata", "fake-sidecar.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return engineDeps{
		host: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home},
		probe: func(context.Context, Config, sidecarLocation) (runtimeIdentity, error) {
			return runtimeIdentity{NodeVersion: "22.0.0", CLIVersion: "2.1.311", SDKVersion: pinnedSDKVersion}, nil
		},
		sidecar: sidecarLocation{NodePath: node, Script: script, SDKVersion: pinnedSDKVersion},
		now:     time.Now,
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Executable:         "/opt/claude/bin/claude",
		CredentialSource:   "host_login",
		AdminPolicy:        "honor",
		DefaultWorkspace:   t.TempDir(),
		MaxRuntimes:        DefaultMaxRuntimes,
		TurnWaitSeconds:    DefaultTurnWaitSeconds,
		PendingTTLSeconds:  DefaultPendingTTLSeconds,
		IdleTimeoutSeconds: DefaultIdleTimeoutSeconds,
	}
}

func testEngine(t *testing.T, mode string) *engine {
	t.Helper()
	eng, err := newEngine(context.Background(), testConfig(t), nil, testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		eng.env = append(eng.env, "FAKE_MODE="+mode)
	}
	return eng
}

func textCall(model string) *aipapi.Call {
	return &aipapi.Call{
		ID:    "req-1",
		Route: aipapi.RouteIntent{Selector: model},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
}

func collect(t *testing.T, stream aipapi.ManagedEventStream) ([]aipapi.Event, error) {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func TestNewEngine_RequiresExplicitHonor(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.AdminPolicy = "refuse"
	_, err := newEngine(context.Background(), cfg, nil, testDeps(t))
	if err == nil || !strings.Contains(err.Error(), "admin_policy: honor") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewEngine_RequiresAPIKeySecret(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.CredentialSource = "api_key"
	_, err := newEngine(context.Background(), cfg, nil, testDeps(t))
	if err == nil || !strings.Contains(err.Error(), "api_key secret required") {
		t.Fatalf("err=%v", err)
	}
}

func TestEngine_ListsOnlyRuntimeModels(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	response, err := eng.listModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 2 || response.Models[0].NativeModelID != "sonnet" ||
		response.Models[0].CanonicalModelID != "anthropic/sonnet" ||
		response.Models[1].NativeModelID != "opus" {
		t.Fatalf("models=%+v", response.Models)
	}
	if response.InventorySource != "claude-agent-sdk" || response.FetchedUnixMS <= 0 || response.RefreshAfterUnixMS == nil {
		t.Fatalf("inventory provenance=%+v", response)
	}
}

func TestEngine_DiscoveryFailureIsExplicit(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "discover_error")
	if _, err := eng.listModels(context.Background(), 0); err == nil {
		t.Fatal("expected discovery error")
	}
	if _, err := eng.profile(context.Background(), new("anthropic/sonnet")); err == nil {
		t.Fatal("expected resolve discovery error")
	}
}

func TestEngine_ResolveUsesDiscoveredIdentity(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	profile, err := eng.profile(context.Background(), new("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profile.ProfileVersion, "sonnet") || !profile.Capabilities.Streaming || !profile.Capabilities.Tools {
		t.Fatalf("profile=%+v", profile)
	}
	if profile.ReasoningEffortValues == nil ||
		profile.ReasoningEffortValues.ModelID != "anthropic/sonnet" ||
		len(profile.ReasoningEffortValues.Supported) != 2 ||
		profile.ReasoningEffortValues.Supported[0] != "low" ||
		profile.ReasoningEffortValues.Exhaustive {
		t.Fatalf("effort=%+v", profile.ReasoningEffortValues)
	}
	if _, err := eng.profile(context.Background(), new("anthropic/unknown")); err == nil {
		t.Fatal("expected unknown model rejection")
	}
}

func TestEngine_ResolveModelAliasMatchesResolvedID(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	native, row, err := eng.resolveModel(context.Background(), "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if native != "claude-sonnet-5" || row.Value != "sonnet" {
		t.Fatalf("native=%q row=%+v", native, row)
	}
}

func TestEngine_DefaultAliasUsesSDKDefault(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	// The synthetic catalog has no "default" row; the real SDK does. This
	// test proves a canonical resolved id is not rejected when multiple
	// aliases resolve to it.
	eng.discovery = bridgeDiscoveryForTest()
	eng.discovered = time.Now()
	native, row, err := eng.resolveModel(context.Background(), "claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if native != "claude-sonnet-5" || row.Value != "sonnet" {
		t.Fatalf("native=%q row=%+v", native, row)
	}
	native, row, err = eng.resolveModel(context.Background(), "anthropic/default")
	if err != nil || native != "" || row.Value != "default" {
		t.Fatalf("default native=%q row=%+v err=%v", native, row, err)
	}
}

func bridgeDiscoveryForTest() bridge.Discovery {
	return bridge.Discovery{Models: []bridge.Model{
		{Value: "default", ResolvedModel: "claude-opus-5-5"},
		{Value: "opus", ResolvedModel: "claude-opus-5-5"},
		{Value: "sonnet", ResolvedModel: "claude-sonnet-5"},
	}}
}

func TestEngine_TextStreamIsCanonical(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]aipapi.EventKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted, aipapi.EventMessageStarted,
		aipapi.EventTextDelta, aipapi.EventTextDelta,
		aipapi.EventUsageDelta, aipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events=%v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events=%v want=%v", kinds, want)
		}
	}
	if events[2].Delta != "hello " || events[3].Delta != "world" {
		t.Fatalf("text deltas=%q %q", events[2].Delta, events[3].Delta)
	}
	usage := events[4]
	if usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.CacheReadTokens != 2 ||
		!usage.UsagePresence.InputTokens || !usage.UsagePresence.OutputTokens || !usage.UsagePresence.CacheReadTokens {
		t.Fatalf("usage=%+v", usage)
	}
	if events[5].FinishReason != "end_turn" {
		t.Fatalf("finish=%q", events[5].FinishReason)
	}
}

func TestEngine_HistoryReplayIsExplicit(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	call := textCall("anthropic/sonnet")
	call.Messages = []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("answer")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
	}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if warningIndex(events, "history_replay_degraded") < 0 {
		t.Fatalf("first event=%+v", events)
	}
}

func TestEngine_DeveloperInstructionIsExplicitAndStrictRejects(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	call := textCall("anthropic/sonnet")
	call.Instructions = []aipapi.Message{{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("be terse")}}}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if warningIndex(events, "instruction_role_degraded") < 0 {
		t.Fatalf("events=%+v", events)
	}

	strict := testEngine(t, "")
	strict.cfg.StrictSemantics = true
	if _, err := strict.Open(context.Background(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("strict err=%v", err)
	}
}

func TestEngine_RejectsInvalidToolsAndNonText(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "")
	call := textCall("anthropic/sonnet")
	call.Tools = []aipapi.ToolDef{
		{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "Read", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	if _, err := eng.Open(context.Background(), call); err == nil || !strings.Contains(err.Error(), "tool catalogue") {
		t.Fatalf("duplicate tools err=%v", err)
	}
	call = textCall("anthropic/sonnet")
	call.Tools = []aipapi.ToolDef{{Name: "Read", Parameters: json.RawMessage(`not-json`)}}
	if _, err := eng.Open(context.Background(), call); err == nil || !strings.Contains(err.Error(), "parameters") {
		t.Fatalf("invalid schema err=%v", err)
	}
	call = textCall("anthropic/sonnet")
	call.Messages[0].Parts = []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "file://x"}}
	if _, err := eng.Open(context.Background(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("image err=%v", err)
	}
}

func TestEngine_AuthFailureIsTyped(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "auth")
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, stream)
	if err == nil {
		t.Fatal("expected auth error")
	}
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("err=%v", err)
	}
	if terminal.TerminalPluginError().Code != backendplugin.ErrorCodeUnauthenticated {
		t.Fatalf("code=%v", terminal.TerminalPluginError().Code)
	}
}

func TestEngine_RedactsSidecarStderrCanary(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t)
	cfg.CredentialSource = "api_key"
	const canary = "sk-ant-canary-credential-value"
	eng, err := newEngine(context.Background(), cfg, map[string]string{"api_key": canary}, testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	eng.env = append(eng.env, "FAKE_MODE=stderr")
	_, err = eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err == nil {
		t.Fatal("expected startup failure")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestEngine_OutOfOrderSidecarFrameFails(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "bad_sequence")
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, stream)
	if err == nil || !strings.Contains(err.Error(), "out-of-order") {
		t.Fatalf("err=%v", err)
	}
}

func testEngineWithCanary(t *testing.T, mode, canary string) *engine {
	t.Helper()
	cfg := testConfig(t)
	cfg.CredentialSource = "api_key"
	eng, err := newEngine(context.Background(), cfg, map[string]string{"api_key": canary}, testDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	eng.env = append(eng.env, "FAKE_MODE="+mode)
	return eng
}

// warningIndex reports the position of one connector warning. Warnings follow
// response_started so the canonical sequence and the unchanged Anthropic encoder
// both accept them.
func warningIndex(events []aipapi.Event, code string) int {
	for i, event := range events {
		if event.Kind == aipapi.EventWarning && event.WarningCode == code {
			return i
		}
	}
	return -1
}

func limitRuntimes(eng *engine, max int) {
	eng.cfg.MaxRuntimes = max
	eng.cfg.TurnWaitSeconds = 0.2
}

func TestEngine_ForwardsValidatedEffortAndDisclosesUnsupportedControls(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "echo_start")
	call := textCall("anthropic/sonnet")
	maxTokens := 128
	temperature := 0.4
	call.Options = aipapi.GenerationOptions{
		ReasoningEffort:  "high",
		MaxOutputTokens:  &maxTokens,
		Temperature:      &temperature,
		ResponseMIMEType: "application/json",
	}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 5 || warningIndex(events, "generation_controls_degraded") < 0 {
		t.Fatalf("events=%+v", events)
	}
	for _, field := range []string{"max_output_tokens", "temperature", "response_mime_type"} {
		if !strings.Contains(events[1].WarningMessage, field) {
			t.Fatalf("warning %q omitted %s", events[1].WarningMessage, field)
		}
	}
	if events[3].Kind != aipapi.EventTextDelta || events[3].Delta != "effort=high;model=sonnet" {
		t.Fatalf("forwarded effort=%+v", events[3])
	}

	strict := testEngine(t, "echo_start")
	strict.cfg.StrictSemantics = true
	strictCall := textCall("anthropic/sonnet")
	strictCall.Options.ReasoningEffort = "high"
	strictStream, err := strict.Open(context.Background(), strictCall)
	if err != nil {
		t.Fatalf("strict mode rejected a faithfully forwarded effort: %v", err)
	}
	strictEvents, err := collect(t, strictStream)
	if err != nil {
		t.Fatal(err)
	}
	forwarded := ""
	strictAudit := false
	for _, event := range strictEvents {
		switch {
		case event.Kind == aipapi.EventTextDelta:
			forwarded += event.Delta
		case event.Kind == aipapi.EventWarning && event.WarningCode == "reasoning_effort_forwarded":
			strictAudit = true
		case event.Kind == aipapi.EventWarning && event.WarningCode == "reasoning_effort_degraded":
			t.Fatalf("strict mode degraded a supported effort: %+v", event)
		}
	}
	if forwarded != "effort=high;model=sonnet" || !strictAudit {
		t.Fatalf("strict forwarded effort=%q audit=%v events=%+v", forwarded, strictAudit, strictEvents)
	}
}

func TestEngine_UnsupportedEffortIsExplicitAndStrictRejectsBeforeTurn(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "echo_start")
	call := textCall("anthropic/sonnet")
	call.Options.ReasoningEffort = "medium"
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if warningIndex(events, "reasoning_effort_degraded") < 0 {
		t.Fatalf("events=%+v", events)
	}
	if events[3].Delta != "effort=;model=sonnet" {
		t.Fatalf("unsupported effort was forwarded: %q", events[3].Delta)
	}

	strict := testEngine(t, "echo_start")
	if _, err := strict.listModels(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	strict.cfg.StrictSemantics = true
	// The turn process must never start: a missing script would fail with a
	// sidecar error instead of the capability rejection.
	strict.location.Script = filepath.Join(t.TempDir(), "missing.mjs")
	if _, err := strict.Open(context.Background(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("strict err=%v", err)
	}
}

func TestEngine_MaxRuntimesBoundsLiveTurns(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "slow")
	limitRuntimes(eng, 1)
	if _, err := eng.listModels(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	first, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err == nil {
		t.Fatal("a second live turn was admitted with max_runtimes=1")
	}
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) || terminal.TerminalPluginError().Code != backendplugin.ErrorCodeResourceExhausted {
		t.Fatalf("capacity err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("capacity wait took %s", elapsed)
	}
	_ = first.Close()
	second, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
	_ = second.Close()
}

func TestEngine_MaxRuntimesBoundsConcurrentSlowTurns(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "slow")
	limitRuntimes(eng, 1)
	if _, err := eng.listModels(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	type openResult struct {
		stream aipapi.ManagedEventStream
		err    error
	}
	start := make(chan struct{})
	results := make(chan openResult, 2)
	for range 2 {
		go func() {
			<-start
			stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
			results <- openResult{stream: stream, err: err}
		}()
	}
	close(start)
	admitted, capacity := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil {
			admitted++
			if result.stream != nil {
				defer func() { _ = result.stream.Close() }()
			}
			continue
		}
		var terminal backendplugin.TerminalError
		if !errors.As(result.err, &terminal) || terminal.TerminalPluginError().Code != backendplugin.ErrorCodeResourceExhausted {
			t.Fatalf("concurrent admission err=%v", result.err)
		}
		capacity++
	}
	if admitted != 1 || capacity != 1 {
		t.Fatalf("admitted=%d capacity=%d with max_runtimes=1", admitted, capacity)
	}
}

func TestEngine_DiscoverySharesRuntimeBudget(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "slow")
	limitRuntimes(eng, 1)
	if _, err := eng.listModels(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	turn, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = turn.Close() }()
	eng.mu.Lock()
	eng.discovered = time.Time{}
	eng.discovery = bridge.Discovery{}
	eng.mu.Unlock()
	if _, err := eng.listModels(context.Background(), 0); err == nil {
		t.Fatal("discovery spawned beyond the runtime bound")
	}
}

func TestEngine_DiscoveryErrorCannotLeakOpaqueSecret(t *testing.T) {
	t.Parallel()
	const canary = "opaque-canary-credential-value"
	eng := testEngineWithCanary(t, "discover_secret", canary)
	_, err := eng.listModels(context.Background(), 0)
	if err == nil {
		t.Fatal("expected discovery failure")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("discovery error leaked the secret: %v", err)
	}
	if !strings.Contains(err.Error(), "Claude Agent SDK reported a failure") {
		t.Fatalf("discovery error was not mapped to a connector-owned summary: %v", err)
	}
}

func TestEngine_TurnErrorCannotLeakOpaqueSecret(t *testing.T) {
	t.Parallel()
	const canary = "opaque-canary-credential-value"
	eng := testEngineWithCanary(t, "error_secret", canary)
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, stream)
	if err == nil {
		t.Fatal("expected turn failure")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("turn error leaked the secret: %v", err)
	}
	if !strings.Contains(err.Error(), "Claude Agent SDK reported a failure") {
		t.Fatalf("turn error was not mapped to a connector-owned summary: %v", err)
	}
}

func TestEngine_WarningRedactsOpaqueSecret(t *testing.T) {
	t.Parallel()
	const canary = "opaque-canary-credential-value"
	eng := testEngineWithCanary(t, "warning_secret", canary)
	stream, err := eng.Open(context.Background(), textCall("anthropic/sonnet"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind != aipapi.EventWarning {
			continue
		}
		found = true
		if strings.Contains(event.WarningMessage, canary) {
			t.Fatalf("warning leaked the secret: %q", event.WarningMessage)
		}
		if !strings.Contains(event.WarningMessage, "[redacted]") {
			t.Fatalf("warning was not sanitized: %q", event.WarningMessage)
		}
	}
	if !found {
		t.Fatalf("events=%+v", events)
	}
}

func TestEngine_TruncatedSidecarProseCannotLeakCredentialPrefix(t *testing.T) {
	t.Parallel()
	const canary = "OpaquePrivateCredentialThatMustNotEscapeOpaquePrivateCredentialThatMustNotEscape"
	eng := testEngineWithCanary(t, "error_prose_credential", canary)
	stream, err := eng.Open(context.Background(), textCall(""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, stream)
	if err == nil {
		t.Fatal("expected SDK failure")
	}
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("expected typed terminal error: %v", err)
	}
	message := terminal.TerminalPluginError().Message
	if strings.Contains(message, canary[:20]) || strings.Contains(message, canary) {
		t.Fatalf("credential fragment escaped in typed terminal message: %q", message)
	}
	if !strings.Contains(message, "Claude Agent SDK reported a failure") {
		t.Fatalf("terminal message was not a connector-owned summary: %q", message)
	}
}

func TestEngine_SidecarFailureCannotEchoPromptPayload(t *testing.T) {
	t.Parallel()
	const prompt = "PRIVATE_PROMPT_PAYLOAD_CANARY"
	eng := testEngine(t, "error_prompt")
	call := textCall("")
	call.Messages[0].Parts = []aipapi.Part{aipapi.TextPart(prompt)}
	stream, err := eng.Open(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(t, stream)
	if err == nil {
		t.Fatal("expected SDK failure")
	}
	var terminal backendplugin.TerminalError
	if !errors.As(err, &terminal) {
		t.Fatalf("expected typed terminal error: %v", err)
	}
	message := terminal.TerminalPluginError().Message
	if strings.Contains(message, prompt) {
		t.Fatalf("raw prompt payload escaped in typed terminal message: %q", message)
	}
	if !strings.Contains(message, "Claude Agent SDK reported a failure") {
		t.Fatalf("terminal message was not a connector-owned summary: %q", message)
	}
}

func TestEngine_CancelUnblocksBlockedStartWrite(t *testing.T) {
	t.Parallel()
	eng := testEngine(t, "no_read")
	call := textCall("")
	call.Messages[0].Parts = []aipapi.Part{aipapi.TextPart(strings.Repeat("x", 1<<20))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := eng.Open(ctx, call)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("cancellation did not unblock a blocked start write")
	}
}
