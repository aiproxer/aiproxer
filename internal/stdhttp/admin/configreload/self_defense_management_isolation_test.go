package configreload_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp"
	adminreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// TestManagementRecoveryListenerStaysUsableAfterSelfDefenseCandidateFailures is
// the operator-safety proof for requirements 1.3, 7.5 and 10.5. It drives the
// real management/recovery handler over a real serialized reload coordinator
// with a real fixed configuration source, and pins three properties at once:
//
//   - the management listener is not wrapped by ingress self-defense: the very
//     loopback address the operator connects from is actively quarantined in the
//     shared adaptive state, the data plane refuses that address with a generic
//     429, and the management status endpoint still answers 200 for the same
//     address at the same instant;
//   - a sequence of bad data-plane self-defense policy candidates (invalid
//     duration, malformed CIDR, restart-required capacity, and a mixed
//     reloadable+restart-required candidate) are each rejected while the
//     last-good generation stays published and the management listener keeps
//     answering, so the feature can never lock the operator out of recovery;
//   - a subsequent valid policy-only candidate still publishes, so repeated
//     rejections do not wedge the reload path.
func TestManagementRecoveryListenerStaysUsableAfterSelfDefenseCandidateFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := writeSelfDefenseManagementConfig(t, selfDefenseAdaptiveYAML("1m", 100000, ""))
	eff, activeSource, startupOwner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(ctx, path, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatalf("LoadBootstrapEffectiveWithSource: %v", err)
	}
	// Guard startup ownership before any process/generation construction; the
	// coordinator consumes this slot only after successful construction.
	t.Cleanup(func() {
		if err := startupOwner.Close(context.Background()); err != nil {
			t.Errorf("startup source cleanup: %v", err)
		}
	})
	ps := newSelfDefenseManagementProcess(t, eff.Config)

	boot, err := runtimebundle.CompileGeneration(ctx, runtimebundle.GenerationCompileInput{
		Process: ps, Compose: mgmtreload.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration boot: %v", err)
	}
	mgr := runtimehost.NewManager(4, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("boot", boot)); err != nil {
		t.Fatalf("publish boot: %v", err)
	}
	lastGoodID := mgr.Active().ID()

	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		t.Fatalf("NewFixedSource: %v", err)
	}
	loader := runtimehost.FuncEffectiveLoader(func(loadCtx context.Context, _ []byte) (*config.EffectiveConfig, error) {
		// The fixed source is the only accepted configuration input, so the
		// effective load always re-reads the startup path the operator edited.
		loaded, _, oneShotOwner, _, loadErr := runtimebundle.LoadBootstrapEffectiveWithSource(loadCtx, path, config.StreamRecoveryOverrides{}, nil)
		if oneShotOwner != nil {
			loadErr = errors.Join(loadErr, oneShotOwner.Close(loadCtx))
		}
		return loaded, loadErr
	})
	compiler := runtimehost.FuncCompiler(func(compileCtx context.Context, candidate *config.Config, live map[string]int) (runtimehost.PublishedRequestPlane, error) {
		plane, compileErr := runtimebundle.CompileGeneration(compileCtx, runtimebundle.GenerationCompileInput{
			Process: ps, Candidate: candidate, Compose: mgmtreload.ComposeStandardHTTP, LiveFactoryKinds: live,
		})
		if compileErr != nil {
			return nil, compileErr
		}
		return plane, nil
	})
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: src, Loader: loader, Classify: configreload.ClassifyEffective, Compile: compiler,
		Manager: mgr, Timeout: 30 * time.Second, ActiveEffective: eff, ActiveSource: activeSource, ActiveSourceOwner: startupOwner,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	t.Cleanup(coord.BeginShutdown)

	mgmt, err := adminreload.NewHandler(adminreload.Options{
		Address: "127.0.0.1:0", AuthMode: adminreload.AuthModeLocalTrust,
	}, coord)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(mgmt.Mux())
	t.Cleanup(server.Close)

	// Quarantine the operator's own loopback address in the shared adaptive
	// state. From here on the data plane must refuse it, and only the management
	// listener must remain usable.
	operator := netip.MustParseAddr("127.0.0.1")
	policy := ingressdefense.Policy{
		Enabled: true, AuthFailures: 5, FailureWindow: time.Minute,
		InitialQuarantine: time.Hour, MaxQuarantine: 2 * time.Hour,
	}
	ps.IngressDefense.RecordProbe(operator, time.Now(), policy)
	if !ps.IngressDefense.IsQuarantined(operator, time.Now()) {
		t.Fatal("the fixture must leave the operator address quarantined in the shared state")
	}
	entriesBefore := ps.IngressDefense.Len()

	dataPlane := runtimehost.NewGenerationDispatcher(mgr)
	dataReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://data-plane.test/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Same direct peer as the loopback management connection below.
	dataReq.RemoteAddr = operator.String() + ":5555"
	dataRec := httptest.NewRecorder()
	dataPlane.ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusTooManyRequests {
		t.Fatalf("data plane = %d, want the self-defense quarantine refusal 429 for the quarantined operator address", dataRec.Code)
	}

	assertManagementAnswers(t, server.URL, entriesBefore, func() int { return ps.IngressDefense.Len() })

	badCandidates := []struct {
		name        string
		adaptive    string
		wantCode    int
		wantFields  []string
		absentField string
	}{
		{
			name:        "invalid failure window",
			adaptive:    selfDefenseAdaptiveYAML("999ms", 100000, ""),
			wantCode:    http.StatusUnprocessableEntity,
			absentField: "access.self_defense.adaptive.window",
		},
		{
			name:        "malformed adaptive exemption cidr",
			adaptive:    selfDefenseAdaptiveYAML("1m", 100000, "not-a-cidr"),
			wantCode:    http.StatusUnprocessableEntity,
			absentField: "access.self_defense.adaptive.exempt_cidrs",
		},
		{
			name:       "restart required capacity",
			adaptive:   selfDefenseAdaptiveYAML("1m", 4096, ""),
			wantCode:   http.StatusConflict,
			wantFields: []string{"access.self_defense.adaptive.max_entries"},
		},
		{
			name:       "mixed reloadable exemption and restart required capacity",
			adaptive:   selfDefenseAdaptiveYAML("2m", 4096, "192.0.2.0/24"),
			wantCode:   http.StatusConflict,
			wantFields: []string{"access.self_defense.adaptive.max_entries"},
		},
	}
	for _, tc := range badCandidates {
		replaceConfigFile(t, path, selfDefenseManagementYAML(tc.adaptive))
		code, result := postReload(t, server.URL)
		if code != tc.wantCode {
			t.Fatalf("%s: reload status = %d, want %d (body %+v)", tc.name, code, tc.wantCode, result)
		}
		if result.ActiveGeneration != lastGoodID {
			t.Fatalf("%s: result active generation = %d, want the last-good generation %d", tc.name, result.ActiveGeneration, lastGoodID)
		}
		for _, field := range tc.wantFields {
			if !slices.Contains(result.RestartFields, field) {
				t.Fatalf("%s: restart fields %v missing %q", tc.name, result.RestartFields, field)
			}
		}
		if tc.absentField != "" && slices.Contains(result.RestartFields, tc.absentField) {
			t.Fatalf("%s: an invalid candidate must never be reported restart-required: %v", tc.name, result.RestartFields)
		}
		if got := mgr.Active().ID(); got != lastGoodID {
			t.Fatalf("%s: active generation = %d, want the last-good generation %d to stay published", tc.name, got, lastGoodID)
		}
		assertManagementAnswers(t, server.URL, entriesBefore, func() int { return ps.IngressDefense.Len() })
	}

	// A valid policy-only candidate must still publish, so the rejected
	// candidates left the reload path usable rather than wedged.
	replaceConfigFile(t, path, selfDefenseManagementYAML(selfDefenseAdaptiveYAML("30s", 100000, "")))
	code, result := postReload(t, server.URL)
	if code != http.StatusOK || result.Category != string(sdkreload.ResultPublished) {
		t.Fatalf("valid policy-only reload = %d %+v, want a published 200", code, result)
	}
	if got := mgr.Active().ID(); got == lastGoodID {
		t.Fatal("a valid policy-only candidate must publish a new generation after the rejected ones")
	}
	if ps.IngressDefense.Len() != entriesBefore {
		t.Fatal("a policy-only reload must not disturb the process-owned adaptive state")
	}
	// The management listener is still outside the feature after a successful
	// publication, from the still-quarantined operator address.
	assertManagementAnswers(t, server.URL, entriesBefore, func() int { return ps.IngressDefense.Len() })
}

// assertManagementAnswers proves the management listener answers both fixed
// management paths, that its own routing (not the self-defense matcher) owns an
// impossible path, and that answering changed no adaptive state at all.
func assertManagementAnswers(t *testing.T, baseURL string, entriesBefore int, entries func() int) {
	t.Helper()

	statusCode, statusBody := getManagement(t, baseURL+adminreload.StatusPath)
	if statusCode != http.StatusOK {
		t.Fatalf("management status = %d, want 200 (body %s)", statusCode, statusBody)
	}
	var status adminreload.StatusDTO
	if err := json.Unmarshal([]byte(statusBody), &status); err != nil {
		t.Fatalf("management status body is not the status DTO: %v (%s)", err, statusBody)
	}
	if status.ActiveGeneration <= 0 {
		t.Fatalf("management status reported no active generation: %+v", status)
	}

	notFoundCode, notFoundBody := getManagement(t, baseURL+"/.env")
	if notFoundCode != http.StatusNotFound {
		t.Fatalf("management impossible path = %d, want its own 404", notFoundCode)
	}
	if notFoundBody == "Not Found\n" {
		t.Fatal("the management surface must not answer with the self-defense generic refusal body")
	}
	if got := entries(); got != entriesBefore {
		t.Fatalf("adaptive entries = %d, want %d: the management listener must be outside the feature", got, entriesBefore)
	}
}

func getManagement(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url) //nolint:noctx // short-lived loopback management probe
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return res.StatusCode, string(body)
}

func postReload(t *testing.T, baseURL string) (int, adminreload.ResultDTO) {
	t.Helper()
	res, err := http.Post(baseURL+adminreload.ReloadPath, "application/json", strings.NewReader("{}")) //nolint:noctx // short-lived loopback management probe
	if err != nil {
		t.Fatalf("POST reload: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read reload body: %v", err)
	}
	var result adminreload.ResultDTO
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("reload body is not the result DTO: %v (%s)", err, body)
	}
	return res.StatusCode, result
}

func newSelfDefenseManagementProcess(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.IngressDefense == nil {
		t.Fatal("ProcessServices must own the process-lifetime adaptive state")
	}
	return ps
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replaceConfigFile performs the atomic replacement the fixed source requires:
// the candidate is written beside the target and renamed over it, so every
// reload attempt presents a new file identity instead of a torn in-place write.
func replaceConfigFile(t *testing.T, path, body string) {
	t.Helper()
	next := path + ".next"
	writeFile(t, next, body)
	if err := os.Rename(next, path); err != nil {
		_ = os.Remove(next)
		t.Fatalf("atomic config replacement: %v", err)
	}
}

func writeSelfDefenseManagementConfig(t *testing.T, adaptive string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ingress-self-defense.yaml")
	writeFile(t, path, selfDefenseManagementYAML(adaptive))
	return path
}

func selfDefenseAdaptiveYAML(window string, maxEntries int, exempt string) string {
	exemptLine := ""
	if exempt != "" {
		exemptLine = fmt.Sprintf("      exempt_cidrs: [%q]\n", exempt)
	}
	return fmt.Sprintf(`      auth_failures: 5
      window: %q
      initial_quarantine: 1m
      max_quarantine: 2h
      state_ttl: 24h
      max_entries: %d
%s`, window, maxEntries, exemptLine)
}

// selfDefenseManagementYAML is a real operator configuration for the standard
// distribution: a local API-key auth chain (the only handler kind whose
// credential-presence probe can prove a request cannot authenticate), a shared
// trusted client-address boundary, and the self-defense adaptive block under
// test. Every reload attempt in the test rewrites only the adaptive block.
func selfDefenseManagementYAML(adaptive string) string {
	return `server:
  address: "127.0.0.1:18099"

access:
  mode: single_user
  geoip:
    client_ip:
      source: x_forwarded_for
      trusted_proxies: ["10.0.0.0/8"]
  self_defense:
    enabled: true
    impossible_paths: true
    adaptive:
` + adaptive + `
auth:
  handler: local_api_key
  local_api_keys:
    - key_id: k1
      principal_id: p1
      key: "sk-aip-self-defense-mgmt-0123456789"

routing:
  max_attempts: 3
  default_route: "sd-mgmt-stub:stub-default"

continuity:
  in_memory: true
  store: memory

logging:
  level: info
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
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - kind: local-stub
      id: sd-mgmt-stub
      enabled: true
      config:
        text: "ingress self-defense management fixture"
        input_tokens: 1
        output_tokens: 1
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
`
}
