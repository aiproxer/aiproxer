package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/backendplugin/runner"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

// gateSpec binds a gate name to an executable command observed in this process.
type gateSpec struct {
	Name    string
	Kind    string // go_test | make | builtin | external
	WorkDir string // relative to root; empty => root
	Args    []string
	// Timeout overrides defaultMakeGateTimeout for `make` gates whose real cost
	// is known and larger. Zero keeps the default; `go_test` gates keep their
	// own fixed budget.
	Timeout time.Duration
	Notes   string
}

// defaultMakeGateTimeout bounds a `make` gate that does not declare a budget.
const defaultMakeGateTimeout = 20 * time.Minute

func (g gateSpec) makeTimeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return defaultMakeGateTimeout
}

type gateResult struct {
	Gate    string `json:"gate"`
	OK      bool   `json:"ok"`
	Status  string `json:"status"` // local_executable|external_blocker|unsupported|failed|pending
	Command string `json:"command,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func rootGateCatalog() []gateSpec {
	return []gateSpec{
		{Name: "root_go_mod_independence", Kind: "builtin"},
		{Name: "structural_module_discovery", Kind: "builtin"},
		{Name: "requirements_parse", Kind: "builtin"},
		{Name: "module_gowork_off_test_build_vet_tidy", Kind: "builtin"},
		{Name: "module_advertised_capability_conformance_filters", Kind: "builtin"},

		{Name: "manifest_unknown_field_strict", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/infra/backendplugins/manifest/"}},
		{Name: "hundred_manifest_discovery", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/infra/backendplugins/discovery/"}},
		{Name: "adapter_stream_session", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/infra/backendplugins/adapter/"}},
		{Name: "processhost_lifecycle_ownership", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/infra/backendplugins/processhost/"}},
		{Name: "adversarial_exact_digest_peer_stale", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/infra/backendplugins/trust/", "./internal/infra/backendplugins/security/", "./internal/infra/backendplugins/processhost/"}},
		{Name: "pkg_aipsdk_backendplugin_contract_tests", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./pkg/aipsdk/backendplugin/..."}},
		{Name: "runtimebundle_mixed_inventory_accounting", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=15m", "./internal/infra/runtimebundle/", "-run", "TestHundred_|TestMixed_|TestPhase45_|TestPhase5_|TestDiscovered_|TestUnknownKind_|TestBuild_strictAuthoritative|TestBuildWiresTokenAccounting|TestConfigExamples_|TestInventorySnapshot"}},
		{Name: "archtest_backend_plugin_release_gates", Kind: "go_test", Args: []string{"test", "-count=1", "-timeout=10m", "./internal/archtest/", "-run", "TestBackendPlugin|Phase8_|Phase7_|NoForbidden|Essential|Optional|Phase91_"}},

		{Name: "package_plugin_smoke", Kind: "make", Args: []string{"package-plugin-smoke"}},
		{Name: "backend_plugin_security_checks", Kind: "make", Args: []string{"backend-plugin-security-checks"}},
		{Name: "backend_plugin_absence_checks", Kind: "make", Args: []string{"backend-plugin-absence-checks"}},
		{Name: "isolated_root_qa", Kind: "make", Args: []string{"isolated-root-qa"}},
		{Name: "installed_plugin_smoke", Kind: "make", Args: []string{"installed-plugin-smoke"}},
		{Name: "race_scan", Kind: "external", Notes: "strict Linux race matrix must pass for this SHA before the release-gates aggregate runs"},
		{Name: "security_fuzz_subset", Kind: "builtin", Notes: "covered by backend-plugin-security-checks FuzzManifest/FuzzServerFrame; full make test-fuzz not required in this gate"},

		{Name: "security_external_ci", Kind: "external", Notes: "phase9-task93 linux race/security + darwin peer-cred"},
		{Name: "cross_platform_matrix_9_4", Kind: "external", Notes: "phase9-task94 native multi-OS CI; darwin host channel unsupported"},
		{Name: "native_linux_macos_windows_ci", Kind: "external", Notes: "phase9-task94/95 multi-OS workflows must be observed for current SHA"},
	}
}

// requirementGateMap maps each acceptance id to a catalog gate name.
func requirementGateMap() map[string]gateSpecRef {
	type ref = gateSpecRef
	m := map[string]ref{}
	defaults := map[string]ref{
		"1.1":   {Gate: "archtest_backend_plugin_release_gates", Fallback: "local_executable"},
		"1.2":   {Gate: "isolated_root_qa"},
		"1.3":   {Gate: "archtest_backend_plugin_release_gates"},
		"1.4":   {Gate: "archtest_backend_plugin_release_gates"},
		"1.5":   {Gate: "root_go_mod_independence"},
		"1.6":   {Gate: "module_gowork_off_test_build_vet_tidy"},
		"1.7":   {Gate: "archtest_backend_plugin_release_gates"},
		"1.8":   {Gate: "archtest_backend_plugin_release_gates"},
		"2.1":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.2":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.3":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.4":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.5":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.6":   {Gate: "module_advertised_capability_conformance_filters"},
		"2.7":   {Gate: "module_gowork_off_test_build_vet_tidy"},
		"2.8":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.9":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"2.10":  {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"3.1":   {Gate: "hundred_manifest_discovery"},
		"3.2":   {Gate: "hundred_manifest_discovery"},
		"3.3":   {Gate: "manifest_unknown_field_strict"},
		"3.4":   {Gate: "manifest_unknown_field_strict"},
		"3.5":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"3.6":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"3.7":   {Gate: "hundred_manifest_discovery"},
		"3.8":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"3.9":   {Gate: "manifest_unknown_field_strict"},
		"3.10":  {Gate: "hundred_manifest_discovery"},
		"4.1":   {Gate: "processhost_lifecycle_ownership"},
		"4.2":   {Gate: "hundred_manifest_discovery"},
		"4.3":   {Gate: "backend_plugin_absence_checks"},
		"4.4":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"4.5":   {Gate: "adversarial_exact_digest_peer_stale"},
		"4.6":   {Gate: "installed_plugin_smoke"},
		"4.7":   {Gate: "adversarial_exact_digest_peer_stale"},
		"4.8":   {Gate: "backend_plugin_security_checks"},
		"5.1":   {Gate: "processhost_lifecycle_ownership"},
		"5.2":   {Gate: "processhost_lifecycle_ownership"},
		"5.3":   {Gate: "processhost_lifecycle_ownership"},
		"5.4":   {Gate: "processhost_lifecycle_ownership", Notes: "macos native tree: external via phase6 blocker; local tests still run"},
		"5.5":   {Gate: "adapter_stream_session"},
		"5.6":   {Gate: "installed_plugin_smoke"},
		"5.7":   {Gate: "processhost_lifecycle_ownership"},
		"5.8":   {Gate: "processhost_lifecycle_ownership"},
		"5.9":   {Gate: "manifest_unknown_field_strict"},
		"6.1":   {Gate: "adapter_stream_session"},
		"6.2":   {Gate: "adapter_stream_session"},
		"6.3":   {Gate: "adapter_stream_session"},
		"6.4":   {Gate: "adapter_stream_session"},
		"6.5":   {Gate: "adapter_stream_session"},
		"6.6":   {Gate: "adapter_stream_session"},
		"6.7":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"6.8":   {Gate: "module_advertised_capability_conformance_filters"},
		"6.9":   {Gate: "processhost_lifecycle_ownership"},
		"6.10":  {Gate: "adapter_stream_session"},
		"7.1":   {Gate: "backend_plugin_security_checks"},
		"7.2":   {Gate: "adversarial_exact_digest_peer_stale"},
		"7.3":   {Gate: "adversarial_exact_digest_peer_stale", Notes: "darwin channel fail-closed; linux race CI external"},
		"7.4":   {Gate: "backend_plugin_security_checks"},
		"7.5":   {Gate: "backend_plugin_security_checks"},
		"7.6":   {Gate: "adversarial_exact_digest_peer_stale"},
		"7.7":   {Gate: "backend_plugin_security_checks"},
		"7.8":   {Gate: "backend_plugin_security_checks"},
		"7.9":   {Gate: "backend_plugin_security_checks"},
		"7.10":  {Gate: "backend_plugin_security_checks"},
		"7.11":  {Gate: "backend_plugin_security_checks"},
		"7.12":  {Gate: "adapter_stream_session"},
		"7.13":  {Gate: "security_external_ci", Fallback: "external_blocker"},
		"8.1":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"8.2":   {Gate: "backend_plugin_security_checks"},
		"8.3":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"8.4":   {Gate: "backend_plugin_security_checks"},
		"8.5":   {Gate: "installed_plugin_smoke"},
		"8.6":   {Gate: "backend_plugin_security_checks"},
		"8.7":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"8.8":   {Gate: "backend_plugin_security_checks"},
		"9.1":   {Gate: "module_advertised_capability_conformance_filters"},
		"9.2":   {Gate: "adapter_stream_session"},
		"9.3":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"9.4":   {Gate: "pkg_aipsdk_backendplugin_contract_tests"},
		"9.5":   {Gate: "module_advertised_capability_conformance_filters"},
		"9.6":   {Gate: "adapter_stream_session"},
		"9.7":   {Gate: "runtimebundle_mixed_inventory_accounting"},
		"9.8":   {Gate: "adapter_stream_session"},
		"10.1":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.2":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.3":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.4":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.5":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.6":  {Gate: "archtest_backend_plugin_release_gates"},
		"10.7":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"10.8":  {Gate: "backend_plugin_absence_checks"},
		"10.9":  {Gate: "archtest_backend_plugin_release_gates"},
		"11.1":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"11.2":  {Gate: "root_go_mod_independence"},
		"11.3":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"11.4":  {Gate: "package_plugin_smoke"},
		"11.5":  {Gate: "package_plugin_smoke"},
		"11.6":  {Gate: "package_plugin_smoke"},
		"11.7":  {Gate: "structural_module_discovery"},
		"11.8":  {Gate: "isolated_root_qa"},
		"11.9":  {Gate: "installed_plugin_smoke"},
		"11.10": {Gate: "archtest_backend_plugin_release_gates"},
		"11.11": {Gate: "cross_platform_matrix_9_4", Fallback: "external_blocker"},
		"11.12": {Gate: "module_gowork_off_test_build_vet_tidy"},
		"12.1":  {Gate: "backend_plugin_security_checks"},
		"12.2":  {Gate: "backend_plugin_security_checks"},
		"12.3":  {Gate: "module_advertised_capability_conformance_filters"},
		"12.4":  {Gate: "archtest_backend_plugin_release_gates"},
		"12.5":  {Gate: "structural_module_discovery"},
		"12.6":  {Gate: "module_gowork_off_test_build_vet_tidy"},
		"12.7":  {Gate: "hundred_manifest_discovery"},
		"12.8":  {Gate: "adapter_stream_session"},
		"12.9":  {Gate: "backend_plugin_security_checks"},
		"12.10": {Gate: "requirements_parse", Notes: "design phase complete; evidence in spec artifacts"},
		"12.11": {Gate: "native_linux_macos_windows_ci", Fallback: "external_blocker"},
	}
	maps.Copy(m, defaults)
	return m
}

type gateSpecRef struct {
	Gate     string
	Fallback string // external_blocker when gate is external
	Notes    string
}

func catalogByName() map[string]gateSpec {
	m := map[string]gateSpec{}
	for _, g := range rootGateCatalog() {
		m[g.Name] = g
	}
	return m
}

func runGate(root string, g gateSpec, observed map[string]gateResult) gateResult {
	if prev, ok := observed[g.Name]; ok {
		return prev
	}
	cmdStr := normalizeCommand(strings.Join(append([]string{g.Kind}, g.Args...), " "))
	res := gateResult{Gate: g.Name, Command: cmdStr, Status: "pending"}
	switch g.Kind {
	case "external":
		res.Status = "external_blocker"
		res.OK = true
		res.Detail = g.Notes
	case "builtin":
		res.Status = "pending"
		res.Detail = "filled by orchestrator"
	case "go_test":
		args := append([]string{}, g.Args...)
		result := runner.Run(context.Background(), runner.Request{
			Argv:    append([]string{"go"}, args...),
			Dir:     root,
			Env:     []string{"GOWORK=off"},
			Timeout: 15 * time.Minute,
			Output:  taskrunner.Capture,
			Label:   "release_gates:" + g.Name,
		})
		res.Command = normalizeCommand("go " + strings.Join(args, " "))
		if result.Kind != taskrunner.Success {
			fmt.Fprintf(os.Stderr, "release_gates: gate %s failed:\n%s\n", g.Name, result.Stdout)
			res.Status = "failed"
			res.OK = false
			res.Detail = sanitizeFailureDetail(root, runner.Error(result).Error())
		} else {
			res.Status = "local_executable"
			res.OK = true
			res.Detail = stableOK
		}
	case "make":
		result := runner.Run(context.Background(), runner.Request{
			Argv:    append([]string{"make"}, g.Args...),
			Dir:     root,
			Timeout: g.makeTimeout(),
			Output:  taskrunner.Capture,
			Label:   "release_gates:" + g.Name,
		})
		res.Command = normalizeCommand("make " + strings.Join(g.Args, " "))
		if result.Kind != taskrunner.Success {
			fmt.Fprintf(os.Stderr, "release_gates: gate %s failed:\n%s\n", g.Name, result.Stdout)
			res.Status = "failed"
			res.OK = false
			res.Detail = sanitizeFailureDetail(root, runner.Error(result).Error())
		} else {
			res.Status = "local_executable"
			res.OK = true
			res.Detail = stableOK
		}
	default:
		res.Status = "unsupported"
		res.Detail = "unknown gate kind"
	}
	observed[g.Name] = res
	return res
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type selectorCheck struct {
	pkg, pattern string
}

func selectorChecks() []selectorCheck {
	return []selectorCheck{
		{"./internal/infra/backendplugins/manifest/", "TestParseStrict_"},
		{"./internal/infra/backendplugins/discovery/", "TestHundred"},
		{"./internal/infra/backendplugins/adapter/", "TestPostOutput_|TestInventory_|TestStream_|TestRecv_Stress"},
		{"./internal/infra/backendplugins/processhost/", "TestLeak_|TestActivate_|TestPeer_"},
		{"./internal/infra/backendplugins/trust/", "TestDigestHandle"},
		{"./internal/infra/runtimebundle/", "TestHundred_|TestMixed_|TestPhase45_|TestDiscovered_Duplicate|TestBuild_strictAuthoritative|TestBuildWiresTokenAccounting"},
	}
}

func validateSelectors(root string) error {
	checks := selectorChecks()
	names := make([]string, len(checks))
	for i, check := range checks {
		names[i] = "(" + check.pattern + ")"
	}
	args := []string{"go", "test", "-json", "-list", strings.Join(names, "|")}
	patterns := make([]*regexp.Regexp, len(checks))
	for i, check := range checks {
		args = append(args, check.pkg)
		pattern, err := regexp.Compile(check.pattern)
		if err != nil {
			return fmt.Errorf("selector %s: %w", check.pkg, err)
		}
		patterns[i] = pattern
	}
	// One Go invocation loads and compiles the shared dependency graph once.
	// Package identity stays attached to every discovered test name.
	result := runCommand(context.Background(), runner.Request{
		Argv: args, Dir: root, Timeout: 8 * time.Minute,
		Output: taskrunner.Capture, Label: "release_gates:selectors",
	})
	if result.Kind != taskrunner.Success {
		return fmt.Errorf("gate selector discovery: %w", runner.Error(result))
	}
	if result.StdoutTruncated {
		return fmt.Errorf("gate selector discovery output truncated")
	}
	matched := make([]bool, len(checks))
	scanner := bufio.NewScanner(bytes.NewReader(result.Stdout))
	for scanner.Scan() {
		var event listEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("gate selector discovery JSON: %w", err)
		}
		if event.Action != "output" {
			continue
		}
		name := strings.TrimSpace(event.Output)
		if !strings.HasPrefix(name, "Test") || strings.ContainsAny(name, " \t\n") {
			continue
		}
		for i, check := range checks {
			suffix := "/" + strings.Trim(check.pkg, "./")
			if strings.HasSuffix(event.Package, suffix) && patterns[i].MatchString(name) {
				matched[i] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("gate selector discovery scan: %w", err)
	}
	var failures []string
	for i, check := range checks {
		if !matched[i] {
			failures = append(failures, fmt.Sprintf("%s -list %q matched 0 tests", check.pkg, check.pattern))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("gate selector validation failed:\n%s", strings.Join(failures, "\n"))
	}
	return nil
}

func markBuiltin(observed map[string]gateResult, name, detail string, ok bool) {
	st := "local_executable"
	if !ok {
		st = "failed"
	}
	observed[name] = gateResult{Gate: name, OK: ok, Status: st, Detail: detail, Command: "builtin:" + name}
}

func bytesContainsFold(b, sub []byte) bool {
	return strings.Contains(strings.ToLower(string(b)), strings.ToLower(string(sub)))
}
