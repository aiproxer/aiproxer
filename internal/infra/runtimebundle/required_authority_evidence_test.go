package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// plainUsageLimitPort implements only the four-method port, with no optional
// host surface: the required-evidence guard must refuse it by name.
type plainUsageLimitPort struct{}

var _ usagelimit.Authority = plainUsageLimitPort{}

func (plainUsageLimitPort) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}, nil
}

func (plainUsageLimitPort) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{}, nil
}

func (plainUsageLimitPort) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{}, nil
}

func (plainUsageLimitPort) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

// requiredEvidenceConfig is the process-assembly config for the guard: control
// plane enabled with a durable store and required_pre_work + accounting_authority.
func requiredEvidenceConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := baseAuthorityConfig(true, "fail_closed")
	cfg.ControlPlane = config.ControlPlaneConfig{
		Enabled:            true,
		Store:              "sqlite",
		SQLitePath:         filepath.Join(t.TempDir(), "control-plane.db"),
		RecordingPolicy:    "required_pre_work",
		RequiredCategories: []string{"accounting_authority"},
	}
	return cfg
}

// TestRequiredAuthorityEvidence_NilPortRefusedAtProcessAssembly is the R1
// guard's G1 startup control: with accounting_authority required under
// required_pre_work, a host without a settable usage-limit port is refused by
// name before any resource acquisition.
func TestRequiredAuthorityEvidence_NilPortRefusedAtProcessAssembly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := requiredEvidenceConfig(t)
	storePath := cfg.ControlPlane.SQLitePath
	reg := pluginreg.NewRegistry()

	_, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err == nil {
		t.Fatal("nil port must be refused when accounting_authority evidence is required")
	}
	for _, want := range []string{"required_pre_work", `capability "usage-limits"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q missing %q", err.Error(), want)
		}
	}
	if _, statErr := os.Stat(storePath); statErr == nil {
		t.Fatalf("guard must refuse before resource acquisition; control-plane store %s was created", storePath)
	}

	// A pure four-method port cannot receive the evidence and is refused by name.
	_, err = runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: reg,
			Production:     runtimebundle.ProductionOptions{UsageLimits: plainUsageLimitPort{}},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err == nil || !strings.Contains(err.Error(), "missing SetEvidenceSink") {
		t.Fatalf("non-setter port must be refused by name, got %v", err)
	}

	// A setter-capable port satisfies the requirement and serves normally.
	port := &recordingUsageLimitPort{}
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Production:     runtimebundle.ProductionOptions{UsageLimits: port},
		},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatalf("setter-capable port must build: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, evidence := port.attachCounts(); evidence != 1 {
		t.Fatalf("required evidence sink attach count=%d, want 1", evidence)
	}
}

func TestRequiredAuthorityEvidence_PrebuildCompileCandidateRefusesMissingPort(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	opts := baseAuthorityOptions(t)
	// A non-nil prebuilt surface selects CompileCandidate's direct assembly path.
	opts.FeatureLifecycles = []aipplugin.Lifecycle{}
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  baseAuthorityConfig(false, "fail_closed"),
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() {
		if err := ps.Close(); err != nil {
			t.Errorf("ProcessServices.Close: %v", err)
		}
	})

	_, err = runtimebundle.CompileCandidate(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: requiredEvidenceConfig(t),
	})
	if err == nil {
		t.Fatal("prebuilt CompileCandidate must refuse required accounting-authority evidence without an injected port")
	}
	for _, want := range []string{"required_pre_work", `capability "usage-limits"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q missing %q", err.Error(), want)
		}
	}
}

// governanceHostConfig renders the reload guard's host configuration: control
// plane enabled with a durable store, authority enabled without rules, and
// (when required) the required_pre_work accounting_authority recording keys.
func governanceHostConfig(aliasPattern string, required bool, cpPath string) string {
	recording := "  recording_policy: best_effort\n"
	if required {
		recording = "  recording_policy: required_pre_work\n  required_categories:\n    - accounting_authority\n"
	}
	body := strings.Replace(usageLimitHostConfig("http://127.0.0.1:1/v1", aliasPattern), `accounting:
  enabled: true
  mode: local_only
  authority:
    enabled: true
    mode: strict
    store: memory
    startup_posture: fail_closed
    rules:
      - id: "tenant.requests"
        kind: quota
        mode: strict
        unit: requests
        limit: 10
        basis: legacy_provider_preferred_attempt
`, `accounting:
  enabled: true
  mode: local_only
  authority:
    enabled: true
    mode: strict
    store: memory
    startup_posture: fail_closed
`, 1)
	body = strings.Replace(body, "diagnostics:\n", "control_plane:\n  enabled: true\n  store: sqlite\n  sqlite_path: "+fmt.Sprintf("%q", cpPath)+"\n"+recording+"diagnostics:\n", 1)
	return body
}

// TestRequiredAuthorityEvidence_ReloadRefusalRetainsGeneration is the R1
// guard's G2 reload control: a reload that turns on required accounting
// authority evidence is rejected as ResultInvalid/StageLoad when the captured
// port cannot receive it, the live generation is retained and keeps serving,
// and a subsequent valid reload publishes.
func TestRequiredAuthorityEvidence_ReloadRefusalRetainsGeneration(t *testing.T) {
	t.Parallel()
	upstream := usageLimitUpstream(t)

	for _, tc := range []struct {
		name string
		port usagelimit.Authority
	}{
		{name: "nil_port", port: nil},
		{name: "non_setter_port", port: plainUsageLimitPort{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cpPath := filepath.Join(t.TempDir(), "control-plane.yaml.db")
			baseConfig := func(alias string, required bool) string {
				return strings.Replace(governanceHostConfig(alias, required, cpPath), "http://127.0.0.1:1/v1", upstream.URL+"/v1", 1)
			}
			configPath := writeUsageLimitHostConfig(t, baseConfig("^alias-chat$", false))

			host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
				ConfigPath:      configPath,
				Mandatory:       aipsdk.StandardDistributionRequirements(),
				LogWriter:       io.Discard,
				HandlerComposer: stdhttp.ComposeStandardHTTP,
				Production:      runtimebundle.ProductionOptions{UsageLimits: tc.port},
			})
			if err != nil {
				t.Fatalf("BuildHost: %v", err)
			}
			t.Cleanup(func() { hostServeCleanup(t, host) })

			serve := func(id string) {
				t.Helper()
				stream, err := host.ExecutorView().Execute(t.Context(), &aipapi.Call{
					ID:       id,
					Route:    aipapi.RouteIntent{Selector: "wire-backend:gpt-4o-mini"},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}},
				})
				if err != nil {
					t.Fatalf("%s: Execute: %v", id, err)
				}
				if _, err := aipapi.Collect(t.Context(), stream); err != nil {
					t.Fatalf("%s: Collect: %v", id, err)
				}
			}
			serve("required-evidence-before")

			activeBefore := runtimebundle.HostManager(host).Active()
			if activeBefore == nil {
				t.Fatal("no active generation before reload")
			}

			// The candidate passes config validation (durable control-plane
			// store) and is refused by the authority-evidence wiring guard.
			candidate := baseConfig("^alias-chat$", true)
			result := publishUsageLimitReload(t, host, configPath, candidate)
			if result.Category != sdkreload.ResultInvalid {
				t.Fatalf("reload category=%q want invalid; reason=%q", result.Category, result.ReasonCategory)
			}
			if result.ReasonCategory != "load" {
				t.Fatalf("reload reason=%q want load", result.ReasonCategory)
			}
			if active := runtimebundle.HostManager(host).Active(); active != activeBefore {
				t.Fatalf("refused reload must retain the live generation (id %v -> %v)", activeBefore.ID(), active.ID())
			}
			serve("required-evidence-retained")

			// A subsequent valid reload (reloadable alias change only) publishes.
			valid := baseConfig("^alias-chat-v2$", false)
			if result := publishUsageLimitReload(t, host, configPath, valid); result.Category != sdkreload.ResultPublished {
				t.Fatalf("valid reload category=%q want published; reason=%q", result.Category, result.ReasonCategory)
			}
		})
	}
}
