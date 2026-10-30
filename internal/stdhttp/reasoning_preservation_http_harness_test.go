package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	refanth "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

const (
	rpE2EModel     = "moonshot-v1-8k"
	rpE2EAnthModel = "claude-3-5-haiku-20241022"
	rpE2EFakeKey   = "sk-test-reasoning-e2e"
	rpE2EAnthKey   = "sk-ant-test-reasoning-e2e"
	// rpHarnessMaxTurnsPerSession is the reasoning store FIFO bound used by the
	// HTTP harness YAML and retention-aware restore validators. Keep a single
	// source of truth — do not hardcode 64 in reasoninge2e.
	rpHarnessMaxTurnsPerSession = 64
)

// rpFeatureRowMode selects how the reasoning-output-preservation features row is written.
type rpFeatureRowMode int

const (
	// rpFeatureRowExplicit writes an explicit observe/restore/disabled row
	// (use_builtin_catalog: false) — existing deterministic / matrix semantics.
	rpFeatureRowExplicit rpFeatureRowMode = iota
	// rpFeatureRowOmit omits the feature row so BuildHost injects standard defaults.
	rpFeatureRowOmit
)

// rpChatStackOpts configures the full-HTTP BuildHost + stdhttp chat stack.
type rpChatStackOpts struct {
	FeatureRow        rpFeatureRowMode
	Action            string // observe|restore|disabled when FeatureRow==rpFeatureRowExplicit
	UseBuiltinCatalog bool
	Model             string // empty -> rpE2EModel
	// OnUnrepresentable overrides feature policy (reject|log_skip). Empty -> reject.
	OnUnrepresentable string
	// AttemptPolicy sets routing.attempt_policy for the dual stack (reject|log_skip).
	// Empty leaves the standard-distribution default (log_skip), which unifies
	// proxy-restored and client-sent reasoning per attempt. Dual-stack writer only.
	AttemptPolicy string
	// FeatureRuleBackends, when non-empty, emits one explicit rule per backend id.
	// Empty -> single rule for the stack's primary backend.
	FeatureRuleBackends []string
	// MaxAttempts overrides routing.max_attempts (0 -> 1).
	MaxAttempts int
	// FailoverRoute, for dual stacks, writes primary|secondary default_route.
	FailoverRoute bool
}

type rpHTTPStack struct {
	proxyURL   string
	proxy      *httptest.Server
	emulator   *httptest.Server
	ledger     *refchat.OracleLedger
	respLedger *refresponses.OracleLedger
	oracleCh   chan []byte // optional diagnostics for negative-control body inspection
	cleanup    func()
}

// rpDualHTTPStack runs Chat + Responses backends under one proxy for route-switch cells.
type rpDualHTTPStack struct {
	proxyURL     string
	proxy        *httptest.Server
	chatEmu      *httptest.Server
	respEmu      *httptest.Server
	chatLedger   *refchat.OracleLedger
	respLedger   *refresponses.OracleLedger
	chatOracleCh chan []byte
	respOracleCh chan []byte
	cleanup      func()
}

func startReasoningPreservationChatStack(t *testing.T, action string, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) *rpHTTPStack {
	t.Helper()
	return startReasoningPreservationChatStackOpts(t, rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
	}, turns, validators...)
}

func startReasoningPreservationChatStackOpts(t *testing.T, opts rpChatStackOpts, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) *rpHTTPStack {
	t.Helper()
	stack, err := startReasoningPreservationChatStackOptsErr(opts, turns, validators...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startReasoningPreservationChatStackErr(action string, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) (*rpHTTPStack, error) {
	return startReasoningPreservationChatStackOptsErr(rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
	}, turns, validators...)
}

func startReasoningPreservationChatStackOptsErr(opts rpChatStackOpts, turns []refchat.ScriptedTurn, validators ...refchat.RequestValidator) (*rpHTTPStack, error) {
	model := opts.Model
	if model == "" {
		model = rpE2EModel
	}
	var ledger *refchat.OracleLedger
	if len(validators) > 0 {
		ledger = refchat.NewOracleLedger(validators...)
	}
	oracleCh := make(chan []byte, len(turns)+4)
	emu := httptest.NewServer(refchat.NewHandler(refchat.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			if ledger != nil {
				ledger.Observe(body)
			}
			cloned := append([]byte(nil), body...)
			select {
			case oracleCh <- cloned:
			default:
			}
		},
		Responder: refchat.ScriptedResponder(turns),
	}))
	cfgPath, err := writeReasoningPreservationConfigOptsErr(opts, "openai-legacy", emu.URL+"/v1", rpE2EFakeKey, model)
	if err != nil {
		emu.Close()
		return nil, err
	}
	proxy, proxyCleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		emu.Close()
		_ = os.RemoveAll(filepath.Dir(cfgPath))
		return nil, err
	}
	stack := &rpHTTPStack{
		proxyURL: proxy.URL,
		proxy:    proxy,
		emulator: emu,
		ledger:   ledger,
		oracleCh: oracleCh,
		cleanup: func() {
			proxy.Close()
			emu.Close()
			proxyCleanup()
			_ = os.RemoveAll(filepath.Dir(cfgPath))
		},
	}
	return stack, nil
}

func startReasoningPreservationResponsesStack(t *testing.T, action string, turns []refresponses.ScriptedTurn, validators ...refresponses.RequestValidator) *rpHTTPStack {
	t.Helper()
	return startReasoningPreservationResponsesStackOpts(t, rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
	}, turns, validators...)
}

func startReasoningPreservationResponsesStackOpts(t *testing.T, opts rpChatStackOpts, turns []refresponses.ScriptedTurn, validators ...refresponses.RequestValidator) *rpHTTPStack {
	t.Helper()
	stack, err := startReasoningPreservationResponsesStackOptsErr(opts, turns, validators...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startReasoningPreservationResponsesStackOptsErr(opts rpChatStackOpts, turns []refresponses.ScriptedTurn, validators ...refresponses.RequestValidator) (*rpHTTPStack, error) {
	model := opts.Model
	if model == "" {
		model = rpE2EModel
	}
	var ledger *refresponses.OracleLedger
	if len(validators) > 0 {
		ledger = refresponses.NewOracleLedger(validators...)
	}
	oracleCh := make(chan []byte, len(turns)+4)
	emu := httptest.NewServer(refresponses.NewHandler(refresponses.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			if ledger != nil {
				ledger.Observe(body)
			}
			cloned := append([]byte(nil), body...)
			select {
			case oracleCh <- cloned:
			default:
			}
		},
		Responder: refresponses.ScriptedResponder(turns),
	}))
	cfgPath, err := writeReasoningPreservationConfigOptsErr(opts, "openai-responses", emu.URL+"/v1", rpE2EFakeKey, model)
	if err != nil {
		emu.Close()
		return nil, err
	}
	proxy, proxyCleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		emu.Close()
		_ = os.RemoveAll(filepath.Dir(cfgPath))
		return nil, err
	}
	return &rpHTTPStack{
		proxyURL:   proxy.URL,
		proxy:      proxy,
		emulator:   emu,
		respLedger: ledger,
		oracleCh:   oracleCh,
		cleanup: func() {
			proxy.Close()
			emu.Close()
			proxyCleanup()
			_ = os.RemoveAll(filepath.Dir(cfgPath))
		},
	}, nil
}

func startReasoningPreservationDualStack(t *testing.T, opts rpChatStackOpts, primaryBackend string, chatTurns []refchat.ScriptedTurn, respTurns []refresponses.ScriptedTurn, chatValidators []refchat.RequestValidator, respValidators []refresponses.RequestValidator) *rpDualHTTPStack {
	t.Helper()
	model := opts.Model
	if model == "" {
		model = rpE2EModel
	}
	var chatLedger *refchat.OracleLedger
	if len(chatValidators) > 0 {
		chatLedger = refchat.NewOracleLedger(chatValidators...)
	}
	var respLedger *refresponses.OracleLedger
	if len(respValidators) > 0 {
		respLedger = refresponses.NewOracleLedger(respValidators...)
	}
	chatOracleCh := make(chan []byte, len(chatTurns)+4)
	respOracleCh := make(chan []byte, len(respTurns)+4)
	chatEmu := httptest.NewServer(refchat.NewHandler(refchat.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			if chatLedger != nil {
				chatLedger.Observe(body)
			}
			cloned := append([]byte(nil), body...)
			select {
			case chatOracleCh <- cloned:
			default:
			}
		},
		Responder: refchat.ScriptedResponder(chatTurns),
	}))
	respEmu := httptest.NewServer(refresponses.NewHandler(refresponses.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			if respLedger != nil {
				respLedger.Observe(body)
			}
			cloned := append([]byte(nil), body...)
			select {
			case respOracleCh <- cloned:
			default:
			}
		},
		Responder: refresponses.ScriptedResponder(respTurns),
	}))
	dir := t.TempDir()
	cfgPath, err := writeReasoningPreservationDualConfigOptsTo(dir, opts, primaryBackend, model, chatEmu.URL+"/v1", rpE2EFakeKey, respEmu.URL+"/v1", rpE2EFakeKey)
	if err != nil {
		chatEmu.Close()
		respEmu.Close()
		t.Fatal(err)
	}
	proxy := startRPBootstrapProxy(t, cfgPath)
	stack := &rpDualHTTPStack{
		proxyURL:     proxy.URL,
		proxy:        proxy,
		chatEmu:      chatEmu,
		respEmu:      respEmu,
		chatLedger:   chatLedger,
		respLedger:   respLedger,
		chatOracleCh: chatOracleCh,
		respOracleCh: respOracleCh,
		cleanup: func() {
			proxy.Close()
			chatEmu.Close()
			respEmu.Close()
		},
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startReasoningPreservationAnthropicStack(t *testing.T, action string, turns []refanth.ThinkingTurn) *rpHTTPStack {
	t.Helper()
	oracleCh := make(chan []byte, len(turns)+4)
	emu := httptest.NewServer(refanth.NewHandler(refanth.Config{
		AllowMissingAPIKey: false,
		OnRequestBody: func(body []byte) {
			oracleCh <- append([]byte(nil), body...)
		},
		Responder: refanth.ScriptedThinkingResponder(turns),
	}))
	cfgPath := writeReasoningPreservationConfig(t, action, "anthropic", emu.URL, rpE2EAnthKey, rpE2EAnthModel)
	proxy := startRPBootstrapProxy(t, cfgPath)
	stack := &rpHTTPStack{
		proxyURL: proxy.URL,
		proxy:    proxy,
		emulator: emu,
		oracleCh: oracleCh,
		cleanup: func() {
			proxy.Close()
			emu.Close()
		},
	}
	t.Cleanup(stack.cleanup)
	return stack
}

func startRPBootstrapProxy(t *testing.T, cfgPath string) *httptest.Server {
	t.Helper()
	srv, cleanup, err := startRPBootstrapProxyErr(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return srv
}

func startRPBootstrapProxyErr(cfgPath string) (*httptest.Server, func(), error) {
	ctx := context.Background()
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("BuildHost: %w", err)
	}
	h := host.HTTPHandler()
	if h == nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_ = host.Close(shutdownCtx)
		return nil, nil, fmt.Errorf("nil host HTTP handler")
	}
	srv := httptest.NewServer(h)
	cleanup := func() {
		srv.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = host.Close(shutdownCtx)
	}
	return srv, cleanup, nil
}

func writeReasoningPreservationConfig(t *testing.T, action, backendKind, baseURL, apiKey, model string) string {
	t.Helper()
	path, err := writeReasoningPreservationConfigOptsTo(t.TempDir(), rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
		Model:      model,
	}, backendKind, baseURL, apiKey, model)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeReasoningPreservationConfigErr(action, backendKind, baseURL, apiKey, model string) (string, error) {
	return writeReasoningPreservationConfigOptsErr(rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     action,
		Model:      model,
	}, backendKind, baseURL, apiKey, model)
}

func writeReasoningPreservationConfigOptsErr(opts rpChatStackOpts, backendKind, baseURL, apiKey, model string) (string, error) {
	dir, err := os.MkdirTemp("", "rp-e2e-*")
	if err != nil {
		return "", err
	}
	path, err := writeReasoningPreservationConfigOptsTo(dir, opts, backendKind, baseURL, apiKey, model)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// writeReasoningPreservationConfigOptsTo writes a literal-free harness config:
// provider credentials come from the host environment (see TestMain), never
// from the file (requirement 2.1). apiKey stays in the signature so existing
// callers read unchanged; the value documents which host root backs the stack.
func writeReasoningPreservationConfigOptsTo(dir string, opts rpChatStackOpts, backendKind, baseURL, apiKey, model string) (string, error) {
	featureSection, err := reasoningPreservationFeatureYAML(opts, backendKind)
	if err != nil {
		return "", err
	}
	route := backendKind + ":" + model
	enableResponses := backendKind == "openai-responses"
	enableLegacy := backendKind == "openai-legacy"
	enableAnth := backendKind == "anthropic"
	respURL := ""
	legacyURL := ""
	anthURL := ""
	switch backendKind {
	case "openai-responses":
		respURL = baseURL
	case "openai-legacy":
		legacyURL = baseURL
	case "anthropic":
		anthURL = baseURL
	}
	yaml := fmt.Sprintf(`
server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
  default_route: %q
continuity:
  in_memory: true
  store: memory
secure_session:
  store: memory
  non_durable_warning: silent
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: %t
      config:
        base_url: %q
    - id: openai-legacy
      enabled: %t
      config:
        base_url: %q
    - id: anthropic
      enabled: %t
      config:
        base_url: %q
    - id: gemini
      enabled: false
      config: {}
    - id: openai-codex
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
%s
`, route,
		enableResponses, respURL,
		enableLegacy, legacyURL,
		enableAnth, anthURL,
		featureSection)
	path := filepath.Join(dir, "rp-e2e.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// writeReasoningPreservationDualConfigOptsTo writes a literal-free dual-backend
// harness config (see writeReasoningPreservationConfigOptsTo for the credential
// contract). chatKey/respKey document the backing host roots; values travel via
// the host environment, never the file.
func writeReasoningPreservationDualConfigOptsTo(dir string, opts rpChatStackOpts, primaryBackend, model, chatURL, chatKey, respURL, respKey string) (string, error) {
	if opts.FeatureRuleBackends == nil {
		opts.FeatureRuleBackends = []string{"openai-legacy", "openai-responses"}
	}
	featureSection, err := reasoningPreservationFeatureYAML(opts, primaryBackend)
	if err != nil {
		return "", err
	}
	route := primaryBackend + ":" + model
	if opts.FailoverRoute {
		secondary := "openai-legacy"
		if primaryBackend == "openai-legacy" {
			secondary = "openai-responses"
		}
		route = primaryBackend + ":" + model + "|" + secondary + ":" + model
	}
	maxAttempts := 1
	if opts.MaxAttempts > 0 {
		maxAttempts = opts.MaxAttempts
	}
	attemptPolicyLine := ""
	if opts.AttemptPolicy != "" {
		attemptPolicyLine = "\n  attempt_policy: " + opts.AttemptPolicy
	}
	yaml := fmt.Sprintf(`
server:
  address: "127.0.0.1:0"
routing:
  max_attempts: %d
  default_route: %q%s
continuity:
  in_memory: true
  store: memory
secure_session:
  store: memory
  non_durable_warning: silent
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: true
      config:
        base_url: %q
    - id: openai-legacy
      enabled: true
      config:
        base_url: %q
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: openai-codex
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
%s
`, maxAttempts, route, attemptPolicyLine, respURL, chatURL, featureSection)
	path := filepath.Join(dir, "rp-e2e-dual.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// startReasoningPreservationFailoverStack wires Responses primary + Chat secondary with
// failover route and max_attempts>=2 for HTTP failover/secondary-reachability controls
// (429 pre-output positive control + malformed-after-visible-output terminal behavior).
func startReasoningPreservationFailoverStack(
	t *testing.T,
	primaryResponder refresponses.Responder,
	secondaryTurns []refchat.ScriptedTurn,
) (*rpDualHTTPStack, *atomic.Int64) {
	t.Helper()
	secondaryHits := &atomic.Int64{}
	chatEmu := httptest.NewServer(refchat.NewHandler(refchat.Config{
		AllowMissingBearer: false,
		OnRequestBody: func(body []byte) {
			secondaryHits.Add(1)
			_ = body
		},
		Responder: refchat.ScriptedResponder(secondaryTurns),
	}))
	respEmu := httptest.NewServer(refresponses.NewHandler(refresponses.Config{
		AllowMissingBearer: false,
		Responder:          primaryResponder,
	}))
	dir := t.TempDir()
	opts := rpChatStackOpts{
		FeatureRow:          rpFeatureRowExplicit,
		Action:              "disabled",
		MaxAttempts:         2,
		FailoverRoute:       true,
		FeatureRuleBackends: []string{"openai-legacy", "openai-responses"},
	}
	cfgPath, err := writeReasoningPreservationDualConfigOptsTo(
		dir, opts, "openai-responses", rpE2EModel,
		chatEmu.URL+"/v1", rpE2EFakeKey, respEmu.URL+"/v1", rpE2EFakeKey,
	)
	if err != nil {
		chatEmu.Close()
		respEmu.Close()
		t.Fatal(err)
	}
	proxy := startRPBootstrapProxy(t, cfgPath)
	stack := &rpDualHTTPStack{
		proxyURL: proxy.URL,
		proxy:    proxy,
		chatEmu:  chatEmu,
		respEmu:  respEmu,
		cleanup: func() {
			proxy.Close()
			chatEmu.Close()
			respEmu.Close()
		},
	}
	t.Cleanup(stack.cleanup)
	return stack, secondaryHits
}

func reasoningPreservationFeatureYAML(opts rpChatStackOpts, backendKind string) (string, error) {
	switch opts.FeatureRow {
	case rpFeatureRowOmit:
		return "", nil
	case rpFeatureRowExplicit:
		enabled := "true"
		onUnrep := opts.OnUnrepresentable
		if onUnrep == "" {
			onUnrep = "reject"
		}
		rules := opts.FeatureRuleBackends
		if len(rules) == 0 && !opts.UseBuiltinCatalog {
			rules = []string{backendKind}
		}
		var rulesYAML strings.Builder
		for i, be := range rules {
			fmt.Fprintf(&rulesYAML, `
          - id: e2e-rule-%d
            backend: %s
            enabled: true`, i+1, be)
		}
		rulesValue := rulesYAML.String()
		if len(rules) == 0 {
			rulesValue = " []"
		}
		featureBlock := ""
		switch opts.Action {
		case "disabled":
			enabled = "false"
			catalog := "false"
			if opts.UseBuiltinCatalog {
				catalog = "true"
			}
			featureBlock = fmt.Sprintf(`
        action: observe
        use_builtin_catalog: %s
        rules:%s
        on_ambiguous: log_skip
        on_unrepresentable: %s
        on_state_error: reject
        state:
          ttl: 1h
          max_turns_per_session: %d
          max_reasoning_bytes_per_turn: 65536
          max_session_bytes: 1048576
`, catalog, rulesValue, onUnrep, rpHarnessMaxTurnsPerSession)
		case "observe", "restore":
			catalog := "false"
			if opts.UseBuiltinCatalog {
				catalog = "true"
			}
			featureBlock = fmt.Sprintf(`
        action: %s
        use_builtin_catalog: %s
        rules:%s
        on_ambiguous: log_skip
        on_unrepresentable: %s
        on_state_error: reject
        state:
          ttl: 1h
          max_turns_per_session: %d
          max_reasoning_bytes_per_turn: 65536
          max_session_bytes: 1048576
`, opts.Action, catalog, rulesValue, onUnrep, rpHarnessMaxTurnsPerSession)
		default:
			return "", fmt.Errorf("unknown action %q", opts.Action)
		}
		return fmt.Sprintf(`    - id: reasoning-output-preservation
      enabled: %s
      config:%s`, enabled, featureBlock), nil
	default:
		return "", fmt.Errorf("unknown feature row mode %d", opts.FeatureRow)
	}
}

func chatReasoning(text string) []reasoninge2e.ReasoningBlock {
	return []reasoninge2e.ReasoningBlock{{
		Dialect: reasoninge2e.DialectOpenAIChatTextV1,
		Text:    text,
	}}
}

func planTurnIDs(plan reasoninge2e.Plan) []string {
	turns := plan.Turns()
	ids := make([]string, len(turns))
	for i := range turns {
		ids[i] = turns[i].ID
	}
	return ids
}

// chatRestoreValidators precomputes per-request oracle checks against plan ExpectedBackend
// with FIFO artifact retention bounded by maxArtifactTurns.
// Request i is expected to carry assistant history prefix length i.
// When requireStream is non-nil, each backend body must decode with that stream flag.
func chatRestoreValidators(plan reasoninge2e.Plan, requestCount int, requireStream *bool, maxArtifactTurns int) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		out[i] = func(body []byte) error {
			if requireStream != nil {
				var probe struct {
					Stream bool `json:"stream"`
				}
				if err := json.Unmarshal(body, &probe); err != nil {
					return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
				}
				if probe.Stream != *requireStream {
					return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: stream_flag", plan.Seed)
				}
			}
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			return reasoninge2e.CheckPrefixRetention(plan, obs, maxArtifactTurns)
		}
	}
	return out
}

// chatRestoreValidatorsPerTurnStream is like chatRestoreValidators and asserts the
// backend stream flag matches each turn's expected stream mode.
// Client stream vs non-stream framing is asserted by the HTTP driver against the plan.
func chatRestoreValidatorsPerTurnStream(plan reasoninge2e.Plan, requestCount int, maxArtifactTurns int) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	turns := plan.Turns()
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		wantStream := true
		if i < len(turns) {
			wantStream = turns[i].Observed.Streaming
		}
		out[i] = func(body []byte) error {
			var probe struct {
				Stream bool `json:"stream"`
			}
			if err := json.Unmarshal(body, &probe); err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			if probe.Stream != wantStream {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: stream_flag", plan.Seed)
			}
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			return reasoninge2e.CheckPrefixRetention(plan, obs, maxArtifactTurns)
		}
	}
	return out
}

// chatNoRestoreValidators asserts history turns carry no restored reasoning (disabled/observe).
func chatNoRestoreValidators(plan reasoninge2e.Plan, requestCount int) []refchat.RequestValidator {
	ids := planTurnIDs(plan)
	out := make([]refchat.RequestValidator, requestCount)
	for i := range requestCount {
		histLen := i
		out[i] = func(body []byte) error {
			prefixIDs := ids
			if histLen < len(ids) {
				prefixIDs = ids[:histLen]
			}
			obs, err := reasoninge2e.ObserveChatBackendRequest(body, prefixIDs)
			if err != nil {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: backend_body_parse", plan.Seed)
			}
			if len(obs.AssistantTurns) != histLen {
				return fmt.Errorf("reasoninge2e oracle: seed=%d structural mismatch: turn_count got=%d want=%d",
					plan.Seed, len(obs.AssistantTurns), histLen)
			}
			for _, turn := range obs.AssistantTurns {
				if len(turn.Reasoning) != 0 {
					return fmt.Errorf("reasoninge2e oracle: seed=%d turn=%s structural mismatch: unexpected_reasoning_insertion",
						plan.Seed, turn.TurnID)
				}
			}
			return nil
		}
	}
	return out
}

func drainOracleBodies(t *testing.T, ch <-chan []byte, n int) [][]byte {
	t.Helper()
	out := make([][]byte, 0, n)
	deadline := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case b := <-ch:
			out = append(out, b)
		case <-deadline:
			t.Fatalf("oracle bodies: got %d want %d", len(out), n)
		}
	}
	return out
}

func requireLedgerOK(t *testing.T, stack *rpHTTPStack) {
	t.Helper()
	if stack.ledger != nil {
		if err := stack.ledger.Err(); err != nil {
			t.Fatalf("oracle ledger: %v", err)
		}
	}
	if stack.respLedger != nil {
		if err := stack.respLedger.Err(); err != nil {
			t.Fatalf("responses oracle ledger: %v", err)
		}
	}
}

func requireDualLedgerOK(t *testing.T, stack *rpDualHTTPStack) {
	t.Helper()
	if stack.chatLedger != nil {
		if err := stack.chatLedger.Err(); err != nil {
			t.Fatalf("chat oracle ledger: %v", err)
		}
	}
	if stack.respLedger != nil {
		if err := stack.respLedger.Err(); err != nil {
			t.Fatalf("responses oracle ledger: %v", err)
		}
	}
}

// aipCarrierTransport injects/captures X-AIP session (+ optional route) headers for official SDK clients.
type aipCarrierTransport struct {
	base  http.RoundTripper
	sid   *string
	token *string
	route string
}

func (t *aipCarrierTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	if t.sid != nil && *t.sid != "" {
		req.Header.Set("X-AIP-Session-Id", *t.sid)
	}
	if t.token != nil && *t.token != "" {
		req.Header.Set("X-AIP-Resume-Token", *t.token)
	}
	if t.route != "" {
		req.Header.Set("X-AIP-Route", t.route)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.sid != nil {
		if v := strings.TrimSpace(resp.Header.Get("X-AIP-Session-Id")); v != "" {
			*t.sid = v
		}
	}
	if t.token != nil {
		if v := strings.TrimSpace(resp.Header.Get("X-AIP-Resume-Token")); v != "" {
			*t.token = v
		}
	}
	return resp, nil
}

func newResponsesProxyClient(proxy *httptest.Server, sid, token *string, route string) *http.Client {
	base := proxy.Client()
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &http.Client{
		Transport: &aipCarrierTransport{base: rt, sid: sid, token: token, route: route},
		Timeout:   base.Timeout,
	}
}

func requireHTTPOK(t *testing.T, status int, body []byte) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("proxy status=%d body_bytes=%d", status, len(body))
	}
}

func requireStreamWire(t *testing.T, resp reasoninge2e.ChatTurnResponse) {
	t.Helper()
	if !strings.Contains(strings.ToLower(resp.ContentType), "text/event-stream") {
		t.Fatalf("stream Content-Type structural mismatch: want text/event-stream")
	}
	if !bytes.Contains(resp.RawBody, []byte("[DONE]")) {
		t.Fatal("stream framing structural mismatch: missing SSE [DONE]")
	}
}

type logWriter struct{ b *strings.Builder }

func (w *logWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func truncateRunLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestHarness_BootstrapFailureCleansConfigDir proves a stack whose bootstrap
// fails after writeReasoningPreservationConfigOptsErr created its rp-e2e-*
// config dir reclaims that dir on the error path. Non-parallel so the
// before/after os.TempDir snapshot cannot race the parallel reasoning tests.
func TestHarness_BootstrapFailureCleansConfigDir(t *testing.T) {
	before := rpE2ETempDirs()
	// An invalid on_unrepresentable policy is rejected during BuildHost feature
	// validation, i.e. after the harness already created its rp-e2e-* config dir.
	stack, err := startReasoningPreservationChatStackOptsErr(rpChatStackOpts{
		FeatureRow:        rpFeatureRowExplicit,
		Action:            "restore",
		OnUnrepresentable: "bogus",
	}, nil)
	if err == nil {
		stack.cleanup()
		t.Fatal("expected BuildHost rejection of invalid on_unrepresentable policy")
	}
	after := rpE2ETempDirs()
	for d := range after {
		if !before[d] {
			t.Fatalf("bootstrap failure leaked config dir %q", d)
		}
	}
}

func rpE2ETempDirs() map[string]bool {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "rp-e2e-*"))
	set := make(map[string]bool, len(matches))
	for _, m := range matches {
		set[m] = true
	}
	return set
}

func TestHarnessMaxTurnsBound_yamlAndValidatorsShareConstant(t *testing.T) {
	t.Parallel()
	yaml, err := reasoningPreservationFeatureYAML(rpChatStackOpts{
		FeatureRow: rpFeatureRowExplicit,
		Action:     "restore",
	}, "openai_chat")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("max_turns_per_session: %d", rpHarnessMaxTurnsPerSession)
	if !strings.Contains(yaml, want) {
		t.Fatalf("feature YAML missing %q in %q", want, yaml)
	}
	if n := strings.Count(yaml, "max_turns_per_session:"); n != 1 {
		t.Fatalf("want exactly one max_turns_per_session in state block, got %d in %q", n, yaml)
	}
	if !strings.Contains(yaml, "state:") {
		t.Fatalf("feature YAML missing state block: %q", yaml)
	}

	plan, err := reasoninge2e.BuildPlan(reasoninge2e.PlanConfig{
		Seed:   490,
		Policy: reasoninge2e.DropAllReasoning,
		Turns: []reasoninge2e.TurnSpec{
			{VisibleText: "a", Reasoning: chatReasoning("ra")},
			{VisibleText: "b", Reasoning: chatReasoning("rb")},
			{VisibleText: "c", Reasoning: chatReasoning("rc")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const tinyBound = 2
	turns := plan.Turns()
	// Eviction-blind observation at request_turn=3 must fail tiny-bound validators.
	blind := map[string]any{
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "u0"},
			{"role": "assistant", "content": turns[0].ExpectedBackend.VisibleText, "reasoning_content": "ra"},
			{"role": "user", "content": "u1"},
			{"role": "assistant", "content": turns[1].ExpectedBackend.VisibleText, "reasoning_content": "rb"},
			{"role": "user", "content": "u2"},
			{"role": "assistant", "content": turns[2].ExpectedBackend.VisibleText, "reasoning_content": "rc"},
			{"role": "user", "content": "u3"},
		},
	}
	body, err := json.Marshal(blind)
	if err != nil {
		t.Fatal(err)
	}
	validators := chatRestoreValidatorsPerTurnStream(plan, 4, tinyBound)
	if err := validators[3](body); err == nil {
		t.Fatal("bound=2 validator must reject restored reasoning for evicted turn A")
	} else if !strings.Contains(err.Error(), "artifact_state=evicted") {
		t.Fatalf("want artifact_state=evicted: %v", err)
	} else if !strings.Contains(err.Error(), "history_turn_id="+turns[0].ID) {
		t.Fatalf("want history_turn_id: %v", err)
	}
}
