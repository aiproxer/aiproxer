package archtest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Approved B-leg connectors for issue #147 User-Agent transport (literals locked).
// Phase 7 migrated OpenRouter/NVIDIA/Hugging Face to external plugins (attribution
// and HTTP policy live in connector config, not root factory wrapping).
var identityTransportApprovedBackendIDs = []string{
	"openai-responses",
	"openai-legacy",
	"anthropic",
	"gemini",
	"bedrock",
}

// Excluded connectors must not receive identity HTTP policy wrapping.
// Local OpenAI-compatible runtimes and Ollama were migrated in Phase 7.
var identityTransportExcludedBackendIDs = []string{
	"openai-codex",
	"openai-codex-app-server",
	"opencode-go",
	"opencode-zen",
	"custom-openai-legacy-compatible",
	"custom-openai-responses-compatible",
	"custom-anthropic-compatible",
}

func TestIdentityTransport_approvedAllowlistLiteral(t *testing.T) {
	t.Parallel()
	want := []string{
		"openai-responses",
		"openai-legacy",
		"anthropic",
		"gemini",
		"bedrock",
	}
	if len(identityTransportApprovedBackendIDs) != len(want) {
		t.Fatalf("approved count=%d want %d", len(identityTransportApprovedBackendIDs), len(want))
	}
	for i := range want {
		if identityTransportApprovedBackendIDs[i] != want[i] {
			t.Fatalf("approved[%d]=%q want %q", i, identityTransportApprovedBackendIDs[i], want[i])
		}
	}
}

func TestIdentityTransport_excludedListLiteral(t *testing.T) {
	t.Parallel()
	want := []string{
		"openai-codex",
		"openai-codex-app-server",
		"opencode-go",
		"opencode-zen",
		"custom-openai-legacy-compatible",
		"custom-openai-responses-compatible",
		"custom-anthropic-compatible",
	}
	if len(identityTransportExcludedBackendIDs) != len(want) {
		t.Fatalf("excluded count=%d want %d", len(identityTransportExcludedBackendIDs), len(want))
	}
	for i := range want {
		if identityTransportExcludedBackendIDs[i] != want[i] {
			t.Fatalf("excluded[%d]=%q want %q", i, identityTransportExcludedBackendIDs[i], want[i])
		}
	}
}

// ID-147-ALLOW: single reconstruction lock for approved + excluded connector IDs (disjoint).
func TestIdentityTransport_ID147_allowlistAndExclusionLiterals(t *testing.T) {
	t.Parallel()
	approved := []string{
		"openai-responses", "openai-legacy", "anthropic", "gemini", "bedrock",
	}
	excluded := []string{
		"openai-codex",
		"openai-codex-app-server", "opencode-go", "opencode-zen",
		"custom-openai-legacy-compatible", "custom-openai-responses-compatible", "custom-anthropic-compatible",
	}
	if len(identityTransportApprovedBackendIDs) != len(approved) {
		t.Fatalf("approved len=%d want %d", len(identityTransportApprovedBackendIDs), len(approved))
	}
	if len(identityTransportExcludedBackendIDs) != len(excluded) {
		t.Fatalf("excluded len=%d want %d", len(identityTransportExcludedBackendIDs), len(excluded))
	}
	seen := map[string]string{}
	for i, id := range approved {
		if identityTransportApprovedBackendIDs[i] != id {
			t.Fatalf("approved[%d]=%q want %q", i, identityTransportApprovedBackendIDs[i], id)
		}
		seen[id] = "approved"
	}
	for i, id := range excluded {
		if identityTransportExcludedBackendIDs[i] != id {
			t.Fatalf("excluded[%d]=%q want %q", i, identityTransportExcludedBackendIDs[i], id)
		}
		if prev, ok := seen[id]; ok {
			t.Fatalf("id %q appears in both %s and excluded", id, prev)
		}
		seen[id] = "excluded"
	}
}

func TestIdentityTransport_excludedPackagesDoNotImportHTTPIdentity(t *testing.T) {
	t.Parallel()
	excludedPkgs := []string{
		"./internal/plugins/backends/localstub",
	}
	assertDepsExcludeForbidden(t, excludedPkgs, []forbiddenDep{
		{
			Substr: "/internal/plugins/backends/httpidentity",
			ErrMsg: "excluded backend packages must not import httpidentity",
		},
	})
}

func TestIdentityTransport_coreDoesNotImportHTTPIdentity(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, []forbiddenDep{
		{
			Substr: "/internal/plugins/backends/httpidentity",
			ErrMsg: "internal/core must not depend on httpidentity (adapter-edge transport)",
		},
	})
}

func TestIdentityTransport_standardpluginsImportsHTTPIdentity(t *testing.T) {
	t.Parallel()
	// Approved wiring lives in standardplugins; the package must reference httpidentity.
	out, err := cachedGoList(t, "-json", "-test=false", "./internal/standardplugins")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkg goListPackage
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const want = "github.com/aiproxer/aiproxer/internal/plugins/backends/httpidentity"
	if slices.Contains(pkg.Imports, want) {
		return
	}
	t.Fatalf("standardplugins must import httpidentity for approved connector wrapping; imports=%v", pkg.Imports)
}

// identityTransportExcludedCompatibleLifecycleFns are built-in compatible factories
// colocated with protocol families (not identity-transport wrapped).
var identityTransportExcludedCompatibleLifecycleFns = []struct {
	dir string
	fn  string
}{
	{dir: "internal/plugins/backends/openaicompat", fn: "LifecycleOpenAILegacyCompatible"},
	{dir: "internal/plugins/backends/openaicompat", fn: "LifecycleOpenAIResponsesCompatible"},
	{dir: "internal/plugins/backends/anthropic", fn: "LifecycleAnthropicCompatible"},
}

// historicalPartialStandardpluginsIdentityScan is the pre-fix incomplete file list that
// silently skipped excluded factories living outside these paths.
var historicalPartialStandardpluginsIdentityScan = []string{
	"backends_openai.go",
	"backends_anthropic.go",
	"backends_gemini.go",
	"backends_bedrock.go",
	"standard_table.go",
}

func TestIdentityTransport_partialStandardpluginsScanMissesExcludedFactories(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "standardplugins")
	partial := readStandardpluginsNamedGo(t, dir, historicalPartialStandardpluginsIdentityScan)

	var missed []string
	for _, entry := range identityTransportExcludedCompatibleLifecycleFns {
		protoDir := filepath.Join(root, entry.dir)
		full := readStandardpluginsProductionGo(t, protoDir)
		inPartial := factoryFuncBody(partial, entry.fn) != ""
		inFull := factoryFuncBody(full, entry.fn) != ""
		if !inFull {
			t.Fatalf("protocol package scan missing compatible lifecycle factory %s in %s", entry.fn, entry.dir)
		}
		if !inPartial {
			missed = append(missed, entry.fn)
		}
	}
	if len(missed) == 0 {
		t.Fatal("partial historical standardplugins scan unexpectedly found every compatible lifecycle factory; update demonstration if file layout changed")
	}
	if !slices.Contains(missed, "LifecycleOpenAILegacyCompatible") {
		t.Fatalf("expected partial scan to miss LifecycleOpenAILegacyCompatible; missed=%v", missed)
	}
}

func TestIdentityTransport_approvedFactoriesCallResolveIdentityHTTP(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "standardplugins")
	src := readStandardpluginsProductionGo(t, dir)

	// Each approved connector factory must wrap identity HTTP (literal allowlist).
	wantResolve := []struct {
		id  string
		fn  string
		lit string
	}{
		{id: "openai-responses", fn: "backendOpenAIResponses", lit: `resolveIdentityHTTP(upstream, idCfg, n, "openairesponses backend config")`},
		{id: "openai-legacy", fn: "backendOpenAILegacy", lit: `resolveIdentityHTTP(upstream, idCfg, n, "openailegacy backend config")`},
		{id: "anthropic", fn: "backendAnthropic", lit: `resolveIdentityHTTP(upstream, idCfg, n, "anthropic backend config")`},
		{id: "gemini", fn: "backendGemini", lit: `resolveIdentityHTTP(upstream, idCfg, n, "gemini backend config")`},
		{id: "bedrock", fn: "backendBedrock", lit: `resolveIdentityHTTP(upstream, idCfg, n, "bedrock backend config")`},
	}
	if len(wantResolve) != len(identityTransportApprovedBackendIDs) {
		t.Fatalf("resolve lock count=%d want %d approved", len(wantResolve), len(identityTransportApprovedBackendIDs))
	}
	for i, row := range wantResolve {
		if row.id != identityTransportApprovedBackendIDs[i] {
			t.Fatalf("resolve lock[%d].id=%q want approved %q", i, row.id, identityTransportApprovedBackendIDs[i])
		}
		body := factoryFuncBody(src, row.fn)
		if body == "" {
			t.Fatalf("%s (%s): factory not found in standardplugins production sources", row.id, row.fn)
		}
		if !strings.Contains(body, row.lit) {
			t.Fatalf("%s (%s): missing identity resolve literal %q", row.id, row.fn, row.lit)
		}
	}

	// Essential factories project identity through BackendFactoryDeps.Identity.
	wantDeps := []string{
		"return backendOpenAIResponses(n, upstream, keys, deps.Identity)",
		"return backendOpenAILegacy(n, upstream, keys, deps.Identity)",
		"return backendAnthropic(n, upstream, keys, deps.Identity)",
		"return backendGemini(n, upstream, keys, deps.Identity)",
		"return backendBedrock(n, upstream, deps.Identity)",
	}
	for _, lit := range wantDeps {
		if !strings.Contains(src, lit) {
			t.Fatalf("standard_table missing approved identity wiring: %q", lit)
		}
	}

	// Compatible lifecycle factories colocated in protocol packages must not receive identity HTTP wrapping.
	for _, entry := range identityTransportExcludedCompatibleLifecycleFns {
		protoSrc := readStandardpluginsProductionGo(t, filepath.Join(root, entry.dir))
		body := factoryFuncBody(protoSrc, entry.fn)
		if body == "" {
			t.Fatalf("compatible lifecycle factory %s absent from %s", entry.fn, entry.dir)
		}
		if strings.Contains(body, "resolveIdentityHTTP") {
			t.Fatalf("compatible lifecycle factory %s must not call resolveIdentityHTTP", entry.fn)
		}
		if strings.Contains(body, "httpidentity.WrapClient") {
			t.Fatalf("compatible lifecycle factory %s must not call httpidentity.WrapClient", entry.fn)
		}
		if strings.Contains(body, "deps.Identity") {
			t.Fatalf("compatible lifecycle factory %s must not reference deps.Identity", entry.fn)
		}
	}
}

func readStandardpluginsProductionGo(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		t.Fatal("no production .go files in standardplugins")
	}
	return readStandardpluginsNamedGo(t, dir, names)
}

func readStandardpluginsNamedGo(t *testing.T, dir string, names []string) string {
	t.Helper()
	var all strings.Builder
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	return all.String()
}

func factoryFuncBody(src, fn string) string {
	idx := strings.Index(src, "func "+fn+"(")
	if idx < 0 {
		return ""
	}
	rest := src[idx:]
	if end := strings.Index(rest, "\nfunc "); end > 0 {
		return rest[:end]
	}
	return rest
}

func TestIdentityTransport_openaiCodexUserAgentVendorLiteralUnchanged(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "connectors", "codex", "internal", "codex", "headers.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	if !strings.Contains(raw, `codexOriginator     = "codex_cli_rs"`) {
		t.Fatal("openai-codex vendor originator literal missing or changed")
	}
	if !strings.Contains(raw, `h.Set("User-Agent", codexUserAgent())`) {
		t.Fatal("openai-codex must still set its vendor User-Agent")
	}
	if strings.Contains(raw, "httpidentity") {
		t.Fatal("openai-codex headers must not reference httpidentity")
	}
}
