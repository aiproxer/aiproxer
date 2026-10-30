package runtimebundle

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

type provisioningEnv struct {
	values   map[string]string
	lookups  []string
	onLookup func()
}

func (e *provisioningEnv) Lookup(name string) (string, bool) {
	if e.onLookup != nil {
		e.onLookup()
	}
	e.lookups = append(e.lookups, name)
	v, ok := e.values[name]
	return v, ok
}

func TestBackendProvisioning_CaptureAggregateBoundsAndMissingExecution(t *testing.T) {
	for _, tc := range []struct {
		size, refs int
		rejected   bool
	}{{65536, 4, false}, {65536, 5, true}, {65537, 1, true}} {
		cfg, reg, exports := provisioningFixture(t)
		binding := cfg.Plugins.BackendDiscovery.Instances["agent"]
		binding.SecretRefs = map[string]config.BackendSecretRef{}
		for i := 0; i < tc.refs; i++ {
			binding.SecretRefs[string(rune('a'+i))] = config.BackendSecretRef{Env: "KEY_REF"}
		}
		cfg.Plugins.BackendDiscovery.Instances["agent"] = binding
		got, err := captureBackendProvisioning(cfg, reg, exports, &provisioningEnv{values: map[string]string{"KEY_REF": strings.Repeat("s", tc.size)}})
		if (err != nil) != tc.rejected {
			t.Fatalf("size/refs %d/%d: %v", tc.size, tc.refs, err)
		}
		if err == nil && len(got["agent"].LaunchEnv) != 0 {
			t.Fatal("missing execution values were not omitted")
		}
	}
	for _, size := range []int{16384, 16385} {
		cfg, reg, exports := provisioningFixture(t)
		_, err := captureBackendProvisioning(cfg, reg, exports, &provisioningEnv{values: map[string]string{"KEY_REF": "synthetic", "HOME": strings.Repeat("h", size)}})
		if (err != nil) != (size > 16384) {
			t.Fatalf("execution bound %d: %v", size, err)
		}
	}
}

func TestBackendProvisioning_FactoryConstructionCannotMisdeliverFrozenInputs(t *testing.T) {
	_, _, exports := provisioningFixture(t)
	_, err := buildDiscoveredBackend(nil, exports[0], "otherkind", "agent", yaml.Node{}, nil, backendplugin.RuntimePolicy{}, execbackend.ConnectorEconomicEvidenceDegrade, new(atomic.Uint64), backendProvisioningInput{FactoryKind: "agentkind"}, nil)
	if err == nil || !strings.Contains(err.Error(), "factory binding mismatch") {
		t.Fatalf("wrong factory reached activation: %v", err)
	}
}

func TestBackendProvisioning_StockInstallAuthorizationAndCaptureRollback(t *testing.T) {
	// Isolate the temp root: production creates staging via
	// os.MkdirTemp("", "aip-plugin-serve-*"), which lands in os.TempDir()
	// — a process-global directory shared with every concurrent go test
	// process. Without isolation, a staging directory created by another
	// process inside this test's snapshot→lookup window is misattributed to
	// this test and the rollback assertion fails spuriously. Pointing the
	// temp root at a per-test directory makes the before/after glob observe
	// only this test's own staging.
	// Cross-platform: os.TempDir honours TMPDIR on Unix but TMP/TEMP on
	// Windows, so all three are redirected. This test is top-level and never
	// calls t.Parallel (t.Setenv would panic otherwise), and Go runs
	// sequential top-level tests exclusively — parallel tests in this package
	// stay paused until sequential ones finish — so no other test can observe
	// the redirected variables concurrently.
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	t.Setenv("TMP", isolated)
	t.Setenv("TEMP", isolated)
	root := StageLocalStubForTest(t)
	manifestPath := filepath.Join(root, "plugin.backendplugin.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// The rollback proof needs a valid static agent-runtime target: under the
	// closed credential contract, an unclassified static export fails at
	// discovery, before any capture could run. Retarget the staged manifest
	// at the registered cursorsdk static namespace with its canonical
	// contract so the failure under test stays the missing-secret capture,
	// not manifest admission. The staged binary is never activated: capture
	// fails first.
	body = []byte(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(body),
		`"access_scope":"any"`, `"access_scope":"local_only", "execution_class":"agent_runtime"`),
		`"credential_mode":"none"`, `"credential_mode":"static"`),
		`"kind":"local-stub"`, `"kind":"cursorsdk"`))
	body = []byte(strings.Replace(string(body),
		`"execution_class":"agent_runtime"`,
		`"execution_class":"agent_runtime", "credential_contract":{"version":2,"class":"api_key","source":"host_env","env_root":"CURSOR_API_KEY","slot":"api_key","selection":"session_admission","quota_domain":"cursor"}`, 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"multi_user", "single_user"} {
		cfg := &config.Config{Access: config.AccessConfig{Mode: mode}, Plugins: config.PluginsConfig{
			Backends:         []config.PluginConfig{{ID: "agent", Kind: "cursorsdk", Enabled: true}},
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: true, Paths: []string{root}, Instances: map[string]config.BackendInstanceProvisioning{"agent": {Kind: "cursorsdk", SecretRefs: map[string]config.BackendSecretRef{"key": {Env: "MISSING"}}}}},
		}}
		before, _ := filepath.Glob(filepath.Join(os.TempDir(), "aip-plugin-serve-*"))
		seen := map[string]bool{}
		for _, path := range before {
			seen[path] = true
		}
		var acquired []string
		env := &provisioningEnv{onLookup: func() {
			paths, _ := filepath.Glob(filepath.Join(os.TempDir(), "aip-plugin-serve-*"))
			for _, path := range paths {
				if !seen[path] {
					acquired = append(acquired, path)
				}
			}
		}}
		install, err := installDiscoveredBackendExports(cfg, pluginreg.NewRegistry(), env)
		if err == nil || install != nil {
			t.Fatalf("want denied/missing-secret failure: %v", err)
		}
		if mode == "multi_user" && len(env.lookups) != 0 {
			t.Fatal("denied host captured before access check")
		}
		if mode == "single_user" {
			if len(env.lookups) != 1 || len(acquired) == 0 {
				t.Fatal("capture did not follow verified catalog acquisition")
			}
			for _, path := range acquired {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("capture failure leaked staging")
				}
			}
		}
	}
}

// TestBackendProvisioning_DisabledDoctorTargetRefusedBeforeLaunch proves the
// selected-instance doctor never falls back to a disabled row: with only a
// disabled target left, the refusal is the disabled-target reason and no
// install/launch path runs.
// Source: DoctorBackendPlugin selected-instance delivery authority.
// Mutant: drop the b.Enabled filter in doctorProvisionedBackend → FAIL.
func TestBackendProvisioning_DisabledDoctorTargetRefusedBeforeLaunch(t *testing.T) {
	cfg, reg, _ := provisioningFixture(t)
	cfg.Plugins.Backends[0].Enabled = false
	_, err := DoctorBackendPlugin(context.Background(), cfg, reg, "agent", nil)
	if err == nil {
		t.Fatal("disabled doctor target admitted")
	}
	if !strings.Contains(err.Error(), "enabled selected target required") {
		t.Fatalf("disabled doctor target must be refused before any launch, got: %v", err)
	}
}
func (e *provisioningEnv) Snapshot() []string { panic("provisioning must never snapshot") }

// Claims: authorization before Lookup, requested-name-only snapshots, clone isolation.
// Source: design Stock host. Mutants: capture before profile checks; shallow clone.
func TestBackendProvisioning_AuthorizedRequestedOnlyAndFrozen(t *testing.T) {
	cfg, reg, exports := provisioningFixture(t)
	env := &provisioningEnv{values: map[string]string{"KEY_REF": "synthetic", "HOME": "/owned/home", "PATH": "/bin", "UNREQUESTED": "not-forwarded"}}
	got, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env.lookups, []string{"KEY_REF", "HOME", "PATH"}) {
		t.Fatalf("unexpected lookups: %v", env.lookups)
	}
	input := got["agent"].clone()
	got["agent"].Secrets.Values["api_key"][0] = 'X'
	got["agent"].LaunchEnv["HOME"] = "/changed"
	if string(input.Secrets.Values["api_key"]) != "synthetic" || input.LaunchEnv["HOME"] != "/owned/home" {
		t.Fatal("frozen input aliases capture")
	}
	base := testBackendResourcePhysicalInput()
	base.LaunchEnv = input.LaunchEnv
	a, _, err := physicalIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	base.LaunchEnv = map[string]string{"PATH": "/bin", "HOME": "/owned/home"}
	b, _, _ := physicalIdentity(base)
	if a != b {
		t.Fatal("map order changed identity")
	}
	base.LaunchEnv["HOME"] = "/other"
	c, _, _ := physicalIdentity(base)
	if a == c {
		t.Fatal("execution context aliased physical identity")
	}
}

func TestBackendProvisioning_DeniedDisabledAndBadTargetsNeverLookup(t *testing.T) {
	for _, mode := range []string{"denied", "disabled", "wrong-kind", "shared", "builtin", "missing-profile"} {
		t.Run(mode, func(t *testing.T) {
			cfg, reg, exports := provisioningFixture(t)
			wantErr := true
			switch mode {
			case "denied":
				cfg.Access.Mode = "multi_user"
			case "disabled":
				cfg.Plugins.Backends[0].Enabled = false
				wantErr = false
			case "wrong-kind":
				cfg.Plugins.Backends[0].Kind = "other"
			case "shared":
				exports[0].Model = processhost.ProcessModelSharedArtifact
			case "builtin":
				exports = nil
			case "missing-profile":
				reg = pluginreg.NewRegistry()
			}
			env := &provisioningEnv{}
			_, err := captureBackendProvisioning(cfg, reg, exports, env)
			if (err != nil) != wantErr || len(env.lookups) != 0 {
				t.Fatalf("err=%v lookups=%v", err, env.lookups)
			}
		})
	}
}

func TestBackendProvisioning_CaptureLimitsAndNULAreValueFree(t *testing.T) {
	for _, value := range []string{"", "CANARY\x00SECRET", strings.Repeat("S", 65537)} {
		cfg, reg, exports := provisioningFixture(t)
		env := &provisioningEnv{values: map[string]string{"KEY_REF": value}}
		_, err := captureBackendProvisioning(cfg, reg, exports, env)
		if err == nil || strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("want safe capture rejection: %v", err)
		}
	}
}

func provisioningFixture(t *testing.T) (*config.Config, *pluginreg.Registry, []ValidatedExport) {
	t.Helper()
	cfg := &config.Config{Access: config.AccessConfig{Mode: "single_user"}, Plugins: config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "agent", Kind: "agentkind", Enabled: true}},
		BackendDiscovery: config.BackendDiscoveryConfig{Enabled: true, Instances: map[string]config.BackendInstanceProvisioning{
			"agent": {Kind: "agentkind", SecretRefs: map[string]config.BackendSecretRef{"api_key": {Env: "KEY_REF"}}, ExecutionEnv: []string{"HOME", "PATH"}},
		}},
	}}
	reg := pluginreg.NewRegistry()
	exports := []ValidatedExport{{Kind: "agentkind", Profile: pluginreg.BackendSecurityProfile{AccessScope: pluginreg.BackendAccessLocalOnly, CredentialMode: pluginreg.CredentialStatic}, ExecProfile: aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionAgentRuntime}, Artifact: &trust.VerifiedArtifact{DigestHex: "verified"}, Model: processhost.ProcessModelPerInstance}}
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := InstallDiscoveredExports(reg, host, exports, DiscoveredInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	return cfg, reg, exports
}
