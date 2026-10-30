package runtimebundle_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Synthetic test fixture for the protected-mount handoff tests; not a real
// credential. The inline annotation keeps the configured gitleaks gate green.
const healthHandoffSecret = "ocbs-health-4-2-secret" // gitleaks:allow

// healthHandoffConfig is the real standard-distribution config used by the
// protected-mount handoff tests: a diagnostics secret and one feature that can
// be toggled to force a published reload.
func healthHandoffConfig(metricsEnabled, promptFeatureEnabled bool) string {
	promptFeature := "    - id: model-system-prompt\n      enabled: false\n"
	if promptFeatureEnabled {
		promptFeature = `    - id: model-system-prompt
      enabled: true
      config:
        rules:
          - id: health-handoff
            model_pattern: ".*"
            append: "health handoff reload"
`
	}
	return fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "backend:test-model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
  shared_secret: %q
observability:
  metrics:
    enabled: %v
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
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: backend
      kind: openai-responses
      enabled: false
      config: {}
    - id: bad
      kind: openai-responses
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
%s`, healthHandoffSecret, metricsEnabled, promptFeature)
}

func writeHealthHandoffConfig(t *testing.T, metricsEnabled, promptFeatureEnabled bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "health-handoff.yaml")
	if err := os.WriteFile(path, []byte(healthHandoffConfig(metricsEnabled, promptFeatureEnabled)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serveHealthHandoff(handler http.Handler, path, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if secret != "" {
		req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// healthHandoffHostInput parameterizes one real host build for the handoff
// tests.
type healthHandoffHostInput struct {
	MetricsEnabled bool
	Production     runtimebundle.ProductionOptions
}

func buildHealthHandoffHost(t *testing.T, in healthHandoffHostInput) *runtimebundle.Host {
	t.Helper()
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      writeHealthHandoffConfig(t, in.MetricsEnabled, false),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		Production:      in.Production,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	return host
}

// enterpriseMountProbe records what the composed host delivered to a
// caller-registered mount.
type enterpriseMountProbe struct {
	body  string
	paths []string
}

func (p *enterpriseMountProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.paths = append(p.paths, r.URL.Path)
	_, _ = fmt.Fprint(w, p.body)
}

func TestEnterpriseAdminMounts_ServedSnapshottedAndRetainedAcrossReload(t *testing.T) {
	t.Parallel()

	probe := &enterpriseMountProbe{body: "enterprise-probe"}
	mounts := []runtimebundle.AdminMount{{Path: "/admin/enterprise-probe", Handler: probe}}
	host := buildHealthHandoffHost(t, healthHandoffHostInput{Production: runtimebundle.ProductionOptions{AdminMounts: mounts}})

	denied := serveHealthHandoff(host.HTTPHandler(), "/admin/enterprise-probe", "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("registered mount without secret status=%d want 403", denied.Code)
	}
	allowed := serveHealthHandoff(host.HTTPHandler(), "/admin/enterprise-probe", healthHandoffSecret)
	if allowed.Code != http.StatusOK || allowed.Body.String() != "enterprise-probe" {
		t.Fatalf("registered mount status=%d body=%q", allowed.Code, allowed.Body.String())
	}
	if sub := serveHealthHandoff(host.HTTPHandler(), "/admin/enterprise-probe/sub", healthHandoffSecret); sub.Code != http.StatusOK {
		t.Fatalf("registered mount subtree status=%d body=%q", sub.Code, sub.Body.String())
	}
	// The mount strips its prefix, so the handler owns its own relative routes
	// (an exact mount match leaves the empty path, like the pre-4.2 billing mount).
	if len(probe.paths) != 2 || probe.paths[0] != "" || probe.paths[1] != "/sub" {
		t.Fatalf("registered mount received paths=%#v want the stripped mount-relative paths", probe.paths)
	}

	// The caller's slice is snapshotted at the process boundary: mutating it
	// after startup must not change what the running host serves.
	mounts[0] = runtimebundle.AdminMount{Path: "/admin/mutated", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})}
	survived := serveHealthHandoff(host.HTTPHandler(), "/admin/enterprise-probe", healthHandoffSecret)
	if survived.Code != http.StatusOK || survived.Body.String() != "enterprise-probe" {
		t.Fatalf("caller mutation changed the served mount: status=%d body=%q", survived.Code, survived.Body.String())
	}
	if mutated := serveHealthHandoff(host.HTTPHandler(), "/admin/mutated", healthHandoffSecret); mutated.Code != http.StatusNotFound {
		t.Fatalf("mutated path must stay unmounted: status=%d", mutated.Code)
	}

	// A published reload retains the process-owned registrations.
	candidate := filepath.Join(t.TempDir(), "enterprise-mount-reload.yaml")
	if err := os.WriteFile(candidate, []byte(healthHandoffConfig(false, true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceTestFile(candidate, host.FixedSourcePath()); err != nil {
		t.Fatal(err)
	}
	result := host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "enterprise-mount"})
	if result.Category != sdkreload.ResultPublished {
		t.Fatalf("reload category=%q reason=%q", result.Category, result.ReasonCategory)
	}
	afterReload := serveHealthHandoff(host.HTTPHandler(), "/admin/enterprise-probe", healthHandoffSecret)
	if afterReload.Code != http.StatusOK || afterReload.Body.String() != "enterprise-probe" {
		t.Fatalf("reload dropped the registered mount: status=%d body=%q", afterReload.Code, afterReload.Body.String())
	}
}
