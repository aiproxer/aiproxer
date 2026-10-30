package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	refbackendopenresponses "github.com/aiproxer/aiproxer/internal/refbackend/openresponses"
	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// Official suite env gate. The full compliance script sets this so the ACTUAL
// pinned official suite runs on the full independent deployment and fails when
// it cannot. Default `go test` never sets it, so plain builds/unit tests keep
// requiring no JavaScript runtime (Requirement 11.10).
const officialSuiteEnv = "AIP_RUN_OFFICIAL_COMPLIANCE"

// complianceToolDir is the isolated, non-production JS tool directory relative
// to the repository root.
const complianceToolDir = "tools/openresponses-compliance"

// repoRoot returns the repository root derived from this source file's path.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// officialSuiteRequested reports whether the ACTUAL official suite must run.
func officialSuiteRequested(t *testing.T) bool {
	t.Helper()
	v := os.Getenv(officialSuiteEnv)
	if v == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "":
		return false
	default:
		return true
	}
}

// requireOfficialSuiteTooling ensures Node and the pinned tool dependencies are
// available. When the suite is requested but tooling is missing this FAILS
// (never silently skips); when not requested it skips so default `go test`
// requires no JavaScript runtime.
func requireOfficialSuiteTooling(t *testing.T) {
	t.Helper()
	if !officialSuiteRequested(t) {
		t.Skip("official compliance suite disabled; set " + officialSuiteEnv + "=1 to run the ACTUAL pinned suite (requires Node + `npm ci` in " + complianceToolDir + ")")
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("official compliance suite requested but no `node` binary found in PATH: %v", err)
	}
	toolRoot := filepath.Join(repoRoot(), complianceToolDir)
	nodeModules := filepath.Join(toolRoot, "node_modules")
	if _, err := os.Stat(nodeModules); err != nil {
		t.Fatalf("official compliance suite requested but tool dependencies are not installed: run `npm ci` in %s first (setup, pinned by package-lock.json); stat: %v", toolRoot, err)
	}
	for _, mod := range []string{"esbuild", "ws", "zod"} {
		if _, err := os.Stat(filepath.Join(nodeModules, mod)); err != nil {
			t.Fatalf("official compliance suite requested but pinned dependency %q is missing under %s: run `npm ci` there (setup)", mod, toolRoot)
		}
	}
	runner := filepath.Join(toolRoot, "scripts", "run.mjs")
	if _, err := os.Stat(runner); err != nil {
		t.Fatalf("official compliance suite requested but runner %s is missing", runner)
	}
	_ = nodePath
}

// complianceScript wraps the independent refbackend emulator with deterministic
// request-conditional script selection, so the heterogeneous official suite
// (JSON, SSE, compact, tool, WS) receives a provider response matched to each
// request while every interaction still runs through the ACTUAL independent
// refbackend server (capture, mismatch, authorize, malformed handling).
type complianceScriptSelector struct {
	ref *refbackendopenresponses.Server
	mu  sync.Mutex
}

func (s *complianceScriptSelector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var scriptID string
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/responses"):
		scriptID = "ws-completed"
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses/compact"):
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if hasModelField(body) {
			scriptID = "compact-ok"
		} else {
			scriptID = "compact-missing-model"
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses"):
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		switch {
		case hasStreamFlag(body):
			scriptID = "sse-completed"
		case hasToolsField(body):
			scriptID = "json-tool"
		default:
			scriptID = "json-completed"
		}
	default:
		http.NotFound(w, r)
		return
	}

	if err := s.ref.Select(scriptID); err != nil {
		http.Error(w, "compliance origin: select script: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.ref.Handler().ServeHTTP(w, r)
}

func hasModelField(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	_, ok := m["model"]
	return ok
}

func hasStreamFlag(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	var stream bool
	if raw, ok := m["stream"]; ok && json.Unmarshal(raw, &stream) == nil {
		return stream
	}
	return false
}

func hasToolsField(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	_, ok := m["tools"]
	return ok
}

// complianceOriginScripts registers the independent refbackend scripts the
// official suite needs and returns the selecting handler.
func complianceOriginScripts(t *testing.T, model string) http.Handler {
	t.Helper()
	ref := refbackendopenresponses.NewServer(refbackendopenresponses.Options{
		AllowMissingBearer: true,
	})
	const createdAt = 1719900000
	message := func(id, text string) refbackendopenresponses.Item {
		return refbackendopenresponses.Item{
			Type:    "message",
			ID:      id,
			Status:  "completed",
			Role:    "assistant",
			Content: []refbackendopenresponses.ContentPart{{Type: "output_text", Text: text}},
		}
	}
	scripts := []*refbackendopenresponses.Script{
		{
			ID:          "json-completed",
			Description: "official suite: non-streaming JSON create",
			Mode:        refbackendopenresponses.ModeJSON,
			Resource: refbackendopenresponses.NewResource(
				"resp_compliance_json", model, createdAt,
				[]refbackendopenresponses.Item{message("msg_compliance_json", "compliance-json-ok")},
			),
		},
		{
			ID:          "json-tool",
			Description: "official suite: tool-calling create returns a function_call item",
			Mode:        refbackendopenresponses.ModeJSON,
			Resource: refbackendopenresponses.NewResource(
				"resp_compliance_tool", model, createdAt,
				[]refbackendopenresponses.Item{
					message("msg_compliance_tool", "compliance-tool-ok"),
					refbackendopenresponses.NewFunctionCallItem(
						"call_compliance_1", "call_compliance_1", "get_weather",
						`{"location":"San Francisco, CA"}`,
					),
				},
			),
		},
		{
			ID:          "sse-completed",
			Description: "official suite: streaming SSE create",
			Mode:        refbackendopenresponses.ModeSSE,
			Resource: refbackendopenresponses.NewResource(
				"resp_compliance_sse", model, createdAt,
				[]refbackendopenresponses.Item{message("msg_compliance_sse", "compliance-sse-ok")},
			),
		},
		{
			ID:          "ws-completed",
			Description: "official suite: WebSocket turn stream",
			Mode:        refbackendopenresponses.ModeWebSocket,
			Resource: refbackendopenresponses.NewResource(
				"resp_compliance_ws", model, createdAt,
				[]refbackendopenresponses.Item{message("msg_compliance_ws", "compliance-ws-ok")},
			),
		},
		{
			ID:          "compact-ok",
			Description: "official suite: compact endpoint",
			Mode:        refbackendopenresponses.ModeCompact,
			CompactResource: refbackendopenresponses.NewCompactResource("compact_compliance", model, createdAt,
				[]refbackendopenresponses.Item{refbackendopenresponses.NewCompactionItem("compaction_compliance", "")}),
		},
		{
			ID:          "compact-missing-model",
			Description: "official suite: compact without model is rejected",
			Mode:        refbackendopenresponses.ModeCompact,
			Error: &refbackendopenresponses.ErrorStep{
				Status:  http.StatusBadRequest,
				Type:    "invalid_request",
				Code:    "model_required",
				Message: "The model parameter is required for compaction.",
				Param:   "model",
			},
		},
	}
	if err := ref.Register(scripts...); err != nil {
		t.Fatalf("register compliance refbackend scripts: %v", err)
	}
	return &complianceScriptSelector{ref: ref}
}

// officialSuiteResult captures one run of the ACTUAL pinned official suite.
type officialSuiteResult struct {
	Summary struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Skipped int `json:"skipped"`
		Total   int `json:"total"`
	} `json:"summary"`
	Cases []struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Status       string   `json:"status"`
		DurationMS   float64  `json:"duration"`
		Errors       []string `json:"errors"`
		StreamEvents *int     `json:"streamEvents"`
	} `json:"results"`
}

// officialSuiteCaseIDs is the authoritative 17-case list vendored with the
// pinned upstream runner. Keep this list explicit so a smaller or duplicated
// result set cannot pass on summary counts alone.
var officialSuiteCaseIDs = [...]string{
	"basic-response", "assistant-phase", "response-output-phase-schema",
	"streaming-response", "websocket-response", "websocket-sequential-responses",
	"websocket-continuation", "websocket-reconnect-store-false-recovery",
	"websocket-previous-response-not-found", "websocket-failed-continuation-evicts-cache",
	"websocket-compact-new-chain", "system-prompt", "tool-calling", "image-input",
	"multi-turn", "compact-response", "compact-missing-model",
}

// runOfficialSuite executes the vendored official runner against baseURL and
// returns the parsed JSON result plus the runner exit code. A non-zero exit
// means at least one official case failed (the runner's own contract); the
// JSON is still parsed so the harness can report every case.
func runOfficialSuite(t *testing.T, baseURL, model, apiKey string) (officialSuiteResult, int) {
	t.Helper()
	toolRoot := filepath.Join(repoRoot(), complianceToolDir)
	runner := filepath.Join(toolRoot, "scripts", "run.mjs")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(
		ctx, "node", runner,
		"--base-url", baseURL,
		"--api-key", apiKey,
		"--model", model,
		"--json",
	)
	cmd.Dir = toolRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if ctx.Err() != nil {
			t.Fatalf("official compliance suite timed out: %v; stderr: %s", ctx.Err(), stderr.String())
		}
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("official compliance suite failed to start: %v\nstderr: %s", runErr, stderr.String())
		}
	}

	var res officialSuiteResult
	if derr := json.Unmarshal(stdout.Bytes(), &res); derr != nil {
		t.Fatalf("official compliance suite produced invalid JSON output: %v\nstdout: %s\nstderr: %s", derr, stdout.String(), stderr.String())
	}
	return res, exitCode
}

// TestOfficialComplianceSuite_FullDeployment runs the ACTUAL pinned official
// compliance suite (vendored upstream src/lib/compliance-tests.ts + upstream
// runner) against a full independent deployment:
//
//	official JS client -> OpenResponses frontend -> core executor ->
//	generic OpenResponses backend -> independent refbackend origin.
//
// The suite is the source of truth; Go-native mirror tests are separate. The
// test fails (never skips) when the suite is requested but cannot run, and
// fails when any official case fails or is skipped.
//
// Gate: set AIP_RUN_OFFICIAL_COMPLIANCE=1 (the full compliance scripts do this).
func TestOfficialComplianceSuite_FullDeployment(t *testing.T) {
	requireOfficialSuiteTooling(t)

	const model = "gpt-4o-mini"
	const apiKey = "sk-official-compliance"

	refHandler := complianceOriginScripts(t, model)
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Model:         model,
		OriginHandler: refHandler,
	})
	if d == nil {
		t.Fatal("Deploy(frontend=openresponses, backend=openresponses) failed")
	}
	defer func() { _ = d.Close() }()

	baseURL := d.Server.URL + "/openresponses/v1"
	res, exitCode := runOfficialSuite(t, baseURL, model, apiKey)

	for _, c := range res.Cases {
		detail := ""
		if len(c.Errors) > 0 {
			detail = ": " + strings.Join(c.Errors, "; ")
		}
		t.Logf("official case %-48s status=%-7s%s", c.ID, c.Status, detail)
	}

	if exitCode != 0 && res.Summary.Total == 0 {
		t.Fatalf("official compliance suite exited %d with no parsed cases (runner failed to produce results)", exitCode)
	}

	if res.Summary.Skipped > 0 {
		var skippedIDs []string
		for _, c := range res.Cases {
			if c.Status == "skipped" {
				skippedIDs = append(skippedIDs, c.ID)
			}
		}
		t.Fatalf("official compliance suite SKIPPED %d case(s) [%s]; the ACTUAL suite must run (no skips allowed)", res.Summary.Skipped, strings.Join(skippedIDs, ", "))
	}
	if res.Summary.Failed > 0 {
		var failed []string
		for _, c := range res.Cases {
			if c.Status == "failed" {
				failed = append(failed, fmt.Sprintf("%s: %s", c.ID, strings.Join(c.Errors, "; ")))
			}
		}
		t.Fatalf("official compliance suite: %d/%d failed against the full deployment:\n%s", res.Summary.Failed, res.Summary.Total, strings.Join(failed, "\n"))
	}
	if res.Summary.Total == 0 {
		t.Fatal("official compliance suite reported zero cases; expected the pinned suite to run")
	}
	if res.Summary.Total != 17 {
		t.Fatalf("official compliance suite reported %d cases; expected the pinned 17-case suite", res.Summary.Total)
	}
	if len(res.Cases) != len(officialSuiteCaseIDs) {
		t.Fatalf("official compliance suite returned %d results; expected exactly %d", len(res.Cases), len(officialSuiteCaseIDs))
	}
	expected := make(map[string]struct{}, len(officialSuiteCaseIDs))
	for _, id := range officialSuiteCaseIDs {
		expected[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(res.Cases))
	for _, c := range res.Cases {
		if _, ok := expected[c.ID]; !ok {
			t.Fatalf("official compliance suite returned unexpected result ID %q", c.ID)
		}
		if _, ok := seen[c.ID]; ok {
			t.Fatalf("official compliance suite returned duplicate result ID %q", c.ID)
		}
		seen[c.ID] = struct{}{}
		if c.Status != "passed" {
			t.Fatalf("official compliance suite result %q has status %q; every result must pass", c.ID, c.Status)
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("official compliance suite omitted %d authoritative result ID(s)", len(expected)-len(seen))
	}
	if res.Summary.Passed != len(res.Cases) || res.Summary.Failed != 0 || res.Summary.Skipped != 0 || res.Summary.Total != len(res.Cases) {
		t.Fatalf("official compliance suite summary is inconsistent: summary=%+v results=%d", res.Summary, len(res.Cases))
	}
	t.Logf("official compliance suite: %d/%d passed, %d failed, %d skipped", res.Summary.Passed, res.Summary.Total, res.Summary.Failed, res.Summary.Skipped)
}
