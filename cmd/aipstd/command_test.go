package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestParseCommandName_defaultServe(t *testing.T) {
	t.Parallel()
	n, err := parseCommandName(nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != CommandServe {
		t.Fatalf("got %q", n)
	}
	n2, err := parseCommandName([]string{})
	if err != nil || n2 != CommandServe {
		t.Fatalf("empty args: %v %q", err, n2)
	}
}

func TestParseCommandName_explicitSubcommands(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"serve", "check-config", "routes", "inventory", "inspect", "doctor", "migrate"} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			n, err := parseCommandName([]string{s})
			if err != nil {
				t.Fatal(err)
			}
			if string(n) != s {
				t.Fatalf("got %q", n)
			}
		})
	}
}

func TestRunMigrateCommandRequiresEnvironmentDSN(t *testing.T) {
	t.Setenv(migrationPostgresDSNEnv, "")
	var stderr bytes.Buffer
	code := RunCommand(t.Context(), CommandOptions{Name: CommandMigrate, ErrorOut: &stderr})
	if code != 1 || !strings.Contains(stderr.String(), migrationPostgresDSNEnv) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunMigrateCommandUnknownComponents(t *testing.T) {
	t.Setenv(migrationPostgresDSNEnv, "postgres://unused")
	var stderr bytes.Buffer
	code := RunCommand(t.Context(), CommandOptions{
		Name:       CommandMigrate,
		Components: "not-a-component",
		ErrorOut:   &stderr,
	})
	if code != 2 || !strings.Contains(stderr.String(), "not-a-component") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunMigrateCommandSuccessAndMigrateError(t *testing.T) {
	t.Setenv(migrationPostgresDSNEnv, "postgres://admin/db")
	orig := runPostgresMigrate
	t.Cleanup(func() { runPostgresMigrate = orig })

	t.Run("success", func(t *testing.T) {
		var gotDSN string
		var gotComponents []string
		runPostgresMigrate = func(_ context.Context, dsn string, components []string) error {
			gotDSN = dsn
			gotComponents = append([]string(nil), components...)
			return nil
		}
		var stderr bytes.Buffer
		code := RunCommand(t.Context(), CommandOptions{
			Name:       CommandMigrate,
			Components: "metering,concurrency",
			ErrorOut:   &stderr,
		})
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if gotDSN != "postgres://admin/db" {
			t.Fatalf("dsn=%q", gotDSN)
		}
		if len(gotComponents) != 2 || gotComponents[0] != "metering" || gotComponents[1] != "concurrency" {
			t.Fatalf("components=%v", gotComponents)
		}
	})

	t.Run("migrate error", func(t *testing.T) {
		runPostgresMigrate = func(context.Context, string, []string) error {
			return errors.New("migrate boom")
		}
		var stderr bytes.Buffer
		code := RunCommand(t.Context(), CommandOptions{
			Name:     CommandMigrate,
			ErrorOut: &stderr,
		})
		if code != 1 || !strings.Contains(stderr.String(), "migrate boom") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestParseCommandName_unknown(t *testing.T) {
	t.Parallel()
	_, err := parseCommandName([]string{"nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseCLIPrefix_configValueMayEqualSubcommandName(t *testing.T) {
	t.Parallel()
	prefix, n, tail := parseCLIPrefix([]string{"--config", "routes", "check-config"})
	wantPrefix := []string{"--config", "routes"}
	if len(prefix) != len(wantPrefix) || prefix[0] != wantPrefix[0] || prefix[1] != wantPrefix[1] {
		t.Fatalf("prefix %#v", prefix)
	}
	if n != CommandCheckConfig || len(tail) != 0 {
		t.Fatalf("cmd=%q tail=%v", n, tail)
	}
	prefix2, n2, tail2 := parseCLIPrefix([]string{"--config", "inventory", "inventory"})
	if len(prefix2) != 2 || prefix2[0] != "--config" || prefix2[1] != "inventory" {
		t.Fatalf("prefix %#v", prefix2)
	}
	if n2 != CommandInventory || len(tail2) != 0 {
		t.Fatalf("cmd=%q tail=%v", n2, tail2)
	}
}

func TestParseArgs_configPathEqualsSubcommandName(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"--config", "routes", "check-config"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandCheckConfig || cfg != "routes" {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
	var usage2 bytes.Buffer
	cfg2, name2, err2 := ParseArgs([]string{"--config", "inventory", "inventory"}, &usage2)
	if err2 != nil {
		t.Fatal(err2)
	}
	if name2 != CommandInventory || cfg2 != "inventory" {
		t.Fatalf("cfg=%q name=%q", cfg2, name2)
	}
}

func TestParseArgs_routesOnlyUsesDefaultConfigPath(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"routes"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandRoutes {
		t.Fatalf("name=%q", name)
	}
	if cfg != "./config/config.yaml" {
		t.Fatalf("cfg=%q want default", cfg)
	}
}

func TestParseCLIPrefix_subcommandPositions(t *testing.T) {
	t.Parallel()
	prefix, n, tail := parseCLIPrefix([]string{"--config", "a.yaml", "routes"})
	wantPrefix := []string{"--config", "a.yaml"}
	if len(prefix) != len(wantPrefix) || prefix[0] != wantPrefix[0] || prefix[1] != wantPrefix[1] {
		t.Fatalf("prefix %#v", prefix)
	}
	if n != CommandRoutes || len(tail) != 0 {
		t.Fatalf("cmd=%q tail=%v", n, tail)
	}
	prefix2, n2, tail2 := parseCLIPrefix([]string{"serve", "--config", "b.yaml"})
	if len(prefix2) != 0 || n2 != CommandServe {
		t.Fatalf("got prefix=%v cmd=%q", prefix2, n2)
	}
	if len(tail2) != 2 || tail2[0] != "--config" || tail2[1] != "b.yaml" {
		t.Fatalf("tail %#v", tail2)
	}
}

func TestParseArgs_configAfterSubcommand(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, name, err := ParseArgs([]string{"serve", "--config", filepath.Join("..", "..", "config", "config.yaml")}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join("..", "..", "config", "config.yaml")
	if name != CommandServe || filepath.Clean(cfg) != filepath.Clean(wantPath) {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
}

func TestParseArgs_configBeforeSubcommand(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	path := filepath.Join("..", "..", "config", "config.yaml")
	cfg, name, err := ParseArgs([]string{"--config", path, "routes"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if name != CommandRoutes || cfg != path {
		t.Fatalf("cfg=%q name=%q", cfg, name)
	}
}

func TestParseArgs_lastConfigWins(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	cfg, _, err := ParseArgs([]string{"--config", "first.yaml", "serve", "--config", "second.yaml"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != "second.yaml" {
		t.Fatalf("got %q", cfg)
	}
}

func TestParseArgs_help(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	_, _, err := ParseArgs([]string{"-h"}, &usage)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v", err)
	}
	if usage.Len() == 0 {
		t.Fatal("expected usage text")
	}
}

func TestRunCommand_checkConfig_reference(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if out.String() == "" {
		t.Fatal("expected stdout message")
	}
}

func TestRunCommand_routes_emitsJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"effective_default_route"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_inventory_emitsJSON(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"frontends"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"extensions"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_checkConfig_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := writeDogfoodLocalStubDiscoveryConfig(t, false)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_checkConfig_multiInstanceExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.multi-instance.example.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandCheckConfig,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %s", errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("configuration is valid")) {
		t.Fatalf("stdout: %s", out.String())
	}
}

func TestRunCommand_routes_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := writeDogfoodLocalStubDiscoveryConfig(t, false)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandRoutes,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"effective_default_route"`)) {
		t.Fatalf("stdout: %s", out.String())
	}
	var routes struct {
		EffectiveDefaultRoute string `json:"effective_default_route"`
		CredentialPosture     string `json:"credential_posture"`
	}
	if err := json.Unmarshal(out.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if routes.EffectiveDefaultRoute != "dogfood-local:stub-default" {
		t.Fatalf("effective_default_route=%q", routes.EffectiveDefaultRoute)
	}
	if routes.CredentialPosture != "all_local_stub" {
		t.Fatalf("credential_posture=%q", routes.CredentialPosture)
	}
}

func TestRunCommand_inventory_dogfoodLocalStubExample(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := writeDogfoodLocalStubDiscoveryConfig(t, false)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInventory,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"dogfood-local"`)) {
		t.Fatalf("expected local stub backend id in inventory stdout: %s", out.String())
	}
	var inv struct {
		Frontends  json.RawMessage `json:"frontends"`
		Extensions json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(out.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Frontends) == 0 || len(inv.Extensions) == 0 {
		t.Fatalf("unexpected inventory shape: %s", out.String())
	}
}

func TestRunCommand_serve_bootstrapFailsOnMissingConfig(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: missing,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 1 {
		t.Fatalf("exit %d want 1 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("bootstrap failed:")) {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestRunCommand_unknownName(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{Name: "weird", ErrorOut: &errb})
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestRunCommand_nilContext(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := RunCommand(nil, CommandOptions{Name: CommandServe, ErrorOut: &errb}) //nolint:staticcheck // intentional nil ctx contract
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !bytes.Contains(errb.Bytes(), []byte("nil context")) {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestRunCommand_Inspect_referenceConfig(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInspect,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if strings.Contains(out.String(), "api_key") || strings.Contains(errb.String(), "secret") {
		t.Fatalf("secret leaked in inspect output")
	}
	var rep struct {
		Entries []struct {
			Source string `json:"source"`
			State  string `json:"state"`
			Kind   string `json:"kind"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) == 0 {
		t.Fatal("empty inspect report")
	}
	foundBuiltin := false
	for _, e := range rep.Entries {
		if e.Source == "builtin" {
			foundBuiltin = true
			break
		}
	}
	if !foundBuiltin {
		t.Fatalf("expected builtin entries: %s", out.String())
	}
}

func TestRunCommand_Doctor_RequiresInstance(t *testing.T) {
	t.Parallel()
	var errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandDoctor,
		ConfigPath: filepath.Join("..", "..", "config", "config.yaml"),
		ErrorOut:   &errb,
	})
	if code != 2 || !strings.Contains(errb.String(), "--instance") {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestRunCommand_Doctor_DiscoveredLocalStubInstance(t *testing.T) {
	t.Parallel()
	cfgPath := writeDogfoodLocalStubDiscoveryConfig(t, false)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandDoctor,
		ConfigPath: cfgPath,
		InstanceID: "dogfood-local",
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 0 {
		t.Fatalf("exit %d stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if strings.Contains(out.String(), "api_key") || strings.Contains(out.String(), "sk-") {
		t.Fatal("secret leaked in doctor output")
	}
	if strings.Contains(out.String(), `"state": "failed"`) || strings.Contains(out.String(), `"state":"failed"`) {
		t.Fatalf("doctor failed for discovered local-stub: %s", out.String())
	}
}

func TestParseArgs_DoctorInstanceFlag(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	opts, err := ParseArgsFull([]string{"doctor", "--instance", "ext-1", "--config", "c.yaml"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Name != CommandDoctor || opts.InstanceID != "ext-1" || opts.ConfigPath != "c.yaml" {
		t.Fatalf("%+v", opts)
	}
}

func TestRunCommand_MissingPlugin_Inspect(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "cfg.yaml")
	body := `
continuity:
  in_memory: true
access:
  mode: single_user
plugins:
  backend_discovery:
    enabled: true
    paths:
      - ` + filepath.ToSlash(root) + `
    development_mode: true
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
  backends:
    - kind: missing-external-kind
      id: missing-plugin-1
      enabled: true
      config: {}
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandInspect,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	// Bootstrap may fail on mandatory distribution requirements before inspect;
	// accept either inspect-level missing report or bootstrap failure without secrets.
	if strings.Contains(out.String(), "api_key") || strings.Contains(errb.String(), "super-secret") {
		t.Fatal("secret leak")
	}
	if code == 0 && !strings.Contains(out.String(), "enabled_missing") && !strings.Contains(out.String(), "missing-plugin-1") {
		t.Fatalf("expected missing plugin signal, code=%d out=%s err=%s", code, out.String(), errb.String())
	}
}

func TestParseArgs_autoResumeFlagMayDisable(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	opts, err := ParseArgsFull([]string{"--auto-resume=true", "serve", "--auto-resume=false"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Name != CommandServe {
		t.Fatalf("name=%q", opts.Name)
	}
	if opts.StreamRecovery.CLIEnabled == nil || *opts.StreamRecovery.CLIEnabled {
		t.Fatalf("expected trailing CLI false override, got %#v", opts.StreamRecovery.CLIEnabled)
	}
}

func TestParseArgs_autoResumeDurations(t *testing.T) {
	t.Parallel()
	var usage bytes.Buffer
	opts, err := ParseArgsFull([]string{"serve", "--auto-resume-idle-timeout=20s", "--auto-resume-grace-period=2s"}, &usage)
	if err != nil {
		t.Fatal(err)
	}
	if opts.StreamRecovery.CLIIdleTimeout.String() != "20s" {
		t.Fatalf("idle timeout=%s", opts.StreamRecovery.CLIIdleTimeout)
	}
	if opts.StreamRecovery.CLIGracePeriod.String() != "2s" {
		t.Fatalf("grace period=%s", opts.StreamRecovery.CLIGracePeriod)
	}
}

func TestParseArgs_multiUserFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want *bool
	}{
		{name: "not_set", args: []string{"serve"}, want: nil},
		{name: "prefix", args: []string{"--multi-user", "serve"}, want: new(true)},
		{name: "tail", args: []string{"serve", "--multi-user"}, want: new(true)},
		{name: "equals_true", args: []string{"serve", "--multi-user=true"}, want: new(true)},
		{name: "explicit_false", args: []string{"serve", "--multi-user=false"}, want: new(false)},
		{name: "prefix_then_tail_overrides", args: []string{"--multi-user=true", "serve", "--multi-user=false"}, want: new(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var usage bytes.Buffer
			opts, err := ParseArgsFull(tc.args, &usage)
			if err != nil {
				t.Fatal(err)
			}
			if opts.Name != CommandServe {
				t.Fatalf("name=%q", opts.Name)
			}
			got := opts.MultiUser
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("want nil, got %v", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("want %v, got nil", *tc.want)
			case tc.want != nil && *tc.want != *got:
				t.Fatalf("want %v, got %v", *tc.want, *got)
			}
		})
	}
}

//go:fix inline
func boolPtr(b bool) *bool { return new(b) }

func TestRunCommand_serve_multiUserFlagInconsistentWithSingleUserConfig(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	cfgPath := writeDogfoodLocalStubDiscoveryConfig(t, false)
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: cfgPath,
		MultiUser:  new(true),
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 2 {
		t.Fatalf("exit %d want 2 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--multi-user")) {
		t.Fatalf("stderr should mention --multi-user: %q", errb.String())
	}
}

func TestRunCommand_serve_multiUserConfigRequiresFlag(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	var out, errb bytes.Buffer
	code := RunCommand(context.Background(), CommandOptions{
		Name:       CommandServe,
		ConfigPath: cfgPath,
		Output:     &out,
		ErrorOut:   &errb,
	})
	if code != 2 {
		t.Fatalf("exit %d want 2 stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("--multi-user")) {
		t.Fatalf("stderr should mention --multi-user: %q", errb.String())
	}
}

// TestBuildHost_multiUserConfigLocalStubPassesPosture confirms BuildHost's
// inline multi-user CLI gate (req 4.2-4.3) did not disable runtimebundle
// posture/security validation: a multi_user config whose only enabled backend
// is local-stub (BackendAccessAny, CredentialNone) still assembles
// successfully when the --multi-user flag is supplied and consistent.
func TestBuildHost_multiUserConfigLocalStubPassesPosture(t *testing.T) {
	t.Parallel()
	cfgPath := writeMultiUserTempConfig(t)
	flagTrue := true
	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:              cfgPath,
		Mandatory:               aipsdk.StandardDistributionRequirements(),
		LogWriter:               io.Discard,
		HandlerComposer:         stdhttp.ComposeStandardHTTP,
		EnforceMultiUserCLIGate: true,
		MultiUser:               &flagTrue,
	})
	if err != nil {
		t.Fatalf("BuildHost failed: %v", err)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("Host.Close: %v", err)
	}
}

func writeDogfoodLocalStubDiscoveryConfig(t *testing.T, multiUser bool) string {
	t.Helper()
	pluginRoot := bpkit.StageLocalStub(t)
	access := "single_user"
	authBlock := ""
	serverExtra := ""
	devMode := "true"
	if multiUser {
		access = "multi_user"
		serverExtra = "\n  auth_mode: external"
		authBlock = `
auth:
  handler: local_api_key
  required_level: api_key
  local_api_keys:
    - key_id: k1
      principal_id: p1
      key: "test-key-at-least-16-chars"
`
		devMode = "false"
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:18080"%s
access:
  mode: %s
%srouting:
  max_attempts: 3
  default_route: "dogfood-local:stub-default"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  backend_discovery:
    enabled: true
    development_mode: %s
    paths:
      - %q
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
    - kind: local-stub
      id: dogfood-local
      enabled: true
      config:
        text: "[dogfood] local stub"
        input_tokens: 3
        output_tokens: 7
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
    - id: tool-call-repair
      enabled: true
      config: {}
`, serverExtra, access, authBlock, devMode, filepath.ToSlash(pluginRoot))
	path := filepath.Join(t.TempDir(), "dogfood-local-stub.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMultiUserTempConfig(t *testing.T) string {
	t.Helper()
	return writeDogfoodLocalStubDiscoveryConfig(t, true)
}
