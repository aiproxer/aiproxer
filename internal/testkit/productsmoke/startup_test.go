//go:build productsmoke

package productsmoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProduct_Startup proves the shipped binary starts and serves, and that an
// invalid configuration is refused with the setting named.
func TestProduct_Startup(t *testing.T) {
	t.Run("ready", testStartupReady)
	t.Run("invalid_config", testStartupInvalidConfig)
}

func testStartupReady(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	h := startSUT(t, sutConfig{Canary: canary})
	failures := failureContext{sut: h, provider: h.ReadyProvider(), wire: h.LastReadyWire()}
	if probes := h.ReadyProbes(); probes < 1 {
		failf(t, failures, "the harness declared the proxy ready without a successful inference request (probes=%d)", probes)
	}
	if !h.Alive() {
		failf(t, failures, "the proxy exited after reporting readiness")
	}
	readiness := h.LastReadyWire()
	if readiness == nil {
		failf(t, failures, "the readiness exchange was not retained for diagnostics")
	}
	if readiness.Status != http.StatusOK {
		failf(t, failures, "the retained readiness exchange reports status %d, want 200", readiness.Status)
	}
	requests := h.ReadyProvider().Requests()
	if len(requests) == 0 {
		failf(t, failures, "the readiness probe never reached the backend: the port opened but no inference request arrived")
	}
	probe := requests[len(requests)-1]
	if probe.Path != openAIChatPath {
		failf(t, failures, "readiness probe reached path %q, want %q", probe.Path, openAIChatPath)
	}
	if !probe.KeyMatched {
		failf(t, failures, "readiness probe did not carry the canary credential")
	}
	var translated translatedChat
	if err := json.Unmarshal(probe.Body, &translated); err != nil {
		failf(t, failures, "readiness probe body is not JSON: %v", err)
	}
	if translated.Model != readyNative {
		failf(t, failures, "readiness probe used model %q, want the readiness backend model %q", translated.Model, readyNative)
	}
	if len(translated.Messages) == 0 {
		failf(t, failures, "readiness probe carried no messages")
	}
	h.evidence.write("startup.txt", fmt.Sprintf(
		"ready_probes=%d provider_requests=%d path=%s model=%s\n",
		h.ReadyProbes(), len(requests), probe.Path, translated.Model))
	t.Logf("startup ready passed: %d readiness probe(s), evidence %s", h.ReadyProbes(), h.evidence.dir)
}

func testStartupInvalidConfig(t *testing.T) {
	t.Parallel()
	bin := requireProductSmokeBinary(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "invalid.yaml")
	const invalidConfig = `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
logging:
  level: verbose
  format: text
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
`
	if err := os.WriteFile(configPath, []byte(invalidConfig), 0o600); err != nil {
		t.Fatalf("product smoke: write invalid config: %v", err)
	}
	ev := newEvidence(t, "")
	ev.write("invalid-config.yaml", invalidConfig)

	ctx, cancel := context.WithTimeout(t.Context(), sutStartupDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", configPath, "serve")
	// Reuse the explicit allowlist of startSUT so an ambient AIP_* override or a
	// foreign credential root cannot change this outcome. There are no backends
	// here, so the environment carries no canary root.
	cmd.Env = sutEnvironment(nil, "")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	ev.write("invalid-config-stdout.txt", stdout.String())
	ev.write("invalid-config-stderr.txt", stderr.String())
	report := func(format string, args ...any) {
		t.Fatalf("product smoke journey %s failed: %s\nstdout:\n%s\nstderr:\n%s\nevidence: %s",
			t.Name(), fmt.Sprintf(format, args...), stdout.String(), stderr.String(), ev.dir)
	}
	if ctx.Err() != nil {
		report("aipstd did not exit with an invalid configuration within %s", sutStartupDeadline)
	}
	if runErr == nil {
		report("aipstd exited 0 for an invalid configuration, want non-zero")
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		report("aipstd did not report an exit status: %v", runErr)
	}
	if code := exitErr.ExitCode(); code == 0 {
		report("aipstd reported exit code 0 for an invalid configuration")
	}
	for _, want := range []string{"logging.level", "verbose"} {
		if !strings.Contains(stderr.String(), want) {
			report("stderr does not name the invalid setting %q", want)
		}
	}
	t.Logf("invalid config refused with exit %d, evidence %s", exitErr.ExitCode(), ev.dir)
}
