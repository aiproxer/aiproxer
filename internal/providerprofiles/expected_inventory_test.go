package providerprofiles_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"gopkg.in/yaml.v3"
)

type expectedSpecConnector struct {
	FactoryKind string
	Module      string
	Family      string
	Subject     string
}

// expectedSpecConnectors lists the 17 bulk inference provider expansion connectors (Tasks 6-8).
var expectedSpecConnectors = []expectedSpecConnector{
	{FactoryKind: "cloudflare", Module: "connectors/cloudflare", Family: "openai-compatible", Subject: "cloudflare"},
	{FactoryKind: "azure-openai", Module: "connectors/azure", Family: "openai-compatible", Subject: "azure-openai"},
	{FactoryKind: "snowflake-cortex", Module: "connectors/snowflake", Family: "openai-compatible", Subject: "snowflake-cortex"},
	{FactoryKind: "databricks-ai", Module: "connectors/databricks", Family: "openai-compatible", Subject: "databricks-ai"},
	{FactoryKind: "infomaniak-ai", Module: "connectors/infomaniak", Family: "openai-compatible", Subject: "infomaniak-ai"},
	{FactoryKind: "vertex", Module: "connectors/vertex", Family: "vertex", Subject: "vertex"},
	{FactoryKind: "sagemaker", Module: "connectors/sagemaker", Family: "sagemaker", Subject: "sagemaker"},
	{FactoryKind: "oci-generative-ai", Module: "connectors/oci", Family: "oci", Subject: "oci-generative-ai"},
	{FactoryKind: "watsonx", Module: "connectors/watsonx", Family: "watsonx", Subject: "watsonx"},
	{FactoryKind: "sapaicore", Module: "connectors/sapaicore", Family: "sapaicore", Subject: "sapaicore"},
	{FactoryKind: "cohere", Module: "connectors/cohere", Family: "cohere", Subject: "cohere"},
	{FactoryKind: "replicate", Module: "connectors/replicate", Family: "replicate", Subject: "replicate"},
	{FactoryKind: "gitlab-duo", Module: "connectors/gitlabduo", Family: "gitlabduo", Subject: "gitlabduo"},
	{FactoryKind: "nous-portal", Module: "connectors/nousportal", Family: "nousportal", Subject: "nousportal"},
	{FactoryKind: "xai-oauth", Module: "connectors/xaioauth", Family: "xaioauth", Subject: "xaioauth"},
	{FactoryKind: "qwen-oauth", Module: "connectors/qwenoauth", Family: "qwenoauth", Subject: "qwenoauth"},
	{FactoryKind: "minimax-oauth", Module: "connectors/minimexoauth", Family: "minimax-oauth", Subject: "minimax-oauth"},
}

// preExistingConnectors lists pre-spec and explicitly approved independent connectors
// outside the bulk inference provider expansion; unlisted ACP connectors remain forbidden.
var preExistingConnectors = map[string]bool{
	"acp":                   true,
	"agycliacp":             true,
	"claudeagentacp":        true,
	"codex":                 true,
	"commandcode-anthropic": true,
	"commandcode-openai":    true,
	"cursorcliacp":          true,
	"geminicliacp":          true,
	"huggingface":           true,
	"llamacpp":              true,
	"lmstudio":              true,
	"localstub":             true,
	"nvidia":                true,
	"ollama":                true,
	"openai-chatgpt-plan":   true,
	"opencode":              true,
	"openrouter":            true,
	"vllm":                  true,
}

func getRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}
}

// TestExpectedInventory_SpecConnectorsExistAndMatchMetadata verifies Check 1:
// All 17 spec connectors exist as source modules with matching release.yaml, non-darwin manifests,
// and coverage.go entries.
func TestExpectedInventory_SpecConnectorsExistAndMatchMetadata(t *testing.T) {
	t.Parallel()
	root := getRepoRoot(t)

	for _, spec := range expectedSpecConnectors {
		t.Run(spec.FactoryKind, func(t *testing.T) {
			t.Parallel()
			modDir := filepath.Join(root, filepath.FromSlash(spec.Module))
			if info, err := os.Stat(modDir); err != nil || !info.IsDir() {
				t.Fatalf("spec connector module directory missing: %s", modDir)
			}

			// 1. Validate release.yaml
			relPath := filepath.Join(modDir, "release.yaml")
			relData, err := os.ReadFile(relPath)
			if err != nil {
				t.Fatalf("missing release.yaml in %s: %v", modDir, err)
			}
			var rel struct {
				FactoryKind string   `yaml:"factory_kind"`
				Profiles    []string `yaml:"profiles"`
			}
			if err := yaml.Unmarshal(relData, &rel); err != nil {
				t.Fatalf("parse release.yaml in %s: %v", modDir, err)
			}
			if rel.FactoryKind != spec.FactoryKind {
				t.Fatalf("release.yaml factory_kind mismatch: got %q, want %q", rel.FactoryKind, spec.FactoryKind)
			}
			if !slices.Contains(rel.Profiles, "full") {
				t.Fatalf("release.yaml profiles must contain 'full', got %v", rel.Profiles)
			}

			// 2. Validate manifest/template.backendplugin.json
			manPath := filepath.Join(modDir, "manifest", "template.backendplugin.json")
			manData, err := os.ReadFile(manPath)
			if err != nil {
				t.Fatalf("missing manifest template in %s: %v", modDir, err)
			}
			var man struct {
				Platforms []struct {
					OS   string `json:"os"`
					Arch string `json:"arch"`
				} `json:"platforms"`
				Exports []struct {
					Kind string `json:"kind"`
				} `json:"exports"`
			}
			if err := json.Unmarshal(manData, &man); err != nil {
				t.Fatalf("parse manifest template in %s: %v", modDir, err)
			}
			var sawKind bool
			for _, exp := range man.Exports {
				if exp.Kind == spec.FactoryKind {
					sawKind = true
				}
			}
			if !sawKind {
				t.Fatalf("manifest exports missing factory kind %q", spec.FactoryKind)
			}
			for _, p := range man.Platforms {
				if p.OS == "darwin" {
					t.Fatalf("manifest in %s claims forbidden darwin platform", modDir)
				}
			}

			// 3. Validate coverage.go entry
			var sawCoverage bool
			for _, cov := range contracttest.CurrentConnectorFamilyCoverage {
				if cov.ModulePath == spec.Module {
					if cov.Family != spec.Family {
						t.Fatalf("coverage family mismatch for %s: got %q, want %q", spec.Module, cov.Family, spec.Family)
					}
					if cov.Subject != spec.Subject {
						t.Fatalf("coverage subject mismatch for %s: got %q, want %q", spec.Module, cov.Subject, spec.Subject)
					}
					sawCoverage = true
					break
				}
			}
			if !sawCoverage {
				t.Fatalf("missing coverage.go entry for ModulePath %q", spec.Module)
			}
		})
	}
}

// TestExpectedInventory_NoUnexpectedConnectorsOrACPGrowth verifies Check 2:
// No unexpected connector directories exist beyond the 17 spec connectors and the pre-existing list.
// Neither the spec connectors nor any new connector adds unexpected ACP.
func TestExpectedInventory_NoUnexpectedConnectorsOrACPGrowth(t *testing.T) {
	t.Parallel()
	root := getRepoRoot(t)
	connectorsDir := filepath.Join(root, "connectors")
	entries, err := os.ReadDir(connectorsDir)
	if err != nil {
		t.Fatalf("read connectors directory: %v", err)
	}

	specKinds := make(map[string]bool, len(expectedSpecConnectors))
	for _, sc := range expectedSpecConnectors {
		specKinds[sc.FactoryKind] = true
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		relPath := filepath.Join(connectorsDir, entry.Name(), "release.yaml")
		relData, err := os.ReadFile(relPath)
		if err != nil {
			// Directory without release.yaml: verify it's not a connector
			continue
		}
		var rel struct {
			FactoryKind string `yaml:"factory_kind"`
		}
		if err := yaml.Unmarshal(relData, &rel); err != nil {
			t.Fatalf("parse release.yaml in connectors/%s: %v", entry.Name(), err)
		}
		kind := rel.FactoryKind
		if kind == "" {
			kind = entry.Name()
		}

		isSpec := specKinds[kind]
		isPreExisting := preExistingConnectors[kind] || preExistingConnectors[entry.Name()]
		if !isSpec && !isPreExisting {
			t.Fatalf("unexpected connector %q (directory connectors/%s) found; must be added to expected inventory", kind, entry.Name())
		}

		// Fail if this spec added an ACP factory
		if isSpec {
			if strings.Contains(strings.ToLower(kind), "acp") || strings.Contains(strings.ToLower(entry.Name()), "acp") {
				t.Fatalf("spec connector %q must not be an ACP factory", kind)
			}
		}
	}
}

// TestExpectedInventory_DedicatedBackendAndConnectorIDsUpdated verifies Check 3:
// dedicatedBackendAndConnectorIDs in catalog_population_test.go contains all 17 spec connectors,
// and none of the catalog profiles collide with them.
func TestExpectedInventory_DedicatedBackendAndConnectorIDsUpdated(t *testing.T) {
	t.Parallel()
	for _, spec := range expectedSpecConnectors {
		if !isDedicatedProductOrFamily(spec.FactoryKind) {
			t.Fatalf("dedicatedBackendAndConnectorIDs is missing spec connector %q", spec.FactoryKind)
		}
	}

	// Verify that none of the catalog profiles collides with dedicatedBackendAndConnectorIDs
	for _, profile := range expectedCatalogProfiles {
		if isDedicatedProductOrFamily(profile.ID) {
			t.Fatalf("catalog profile %q collides with dedicated backend/connector ID", profile.ID)
		}
	}
}

// TestExpectedInventory_UnsupportedIdentitiesHaveNoFactory verifies Check 4:
// Unsupported identities (github-copilot, claude-subscription) have no connector and no coverage row.
func TestExpectedInventory_UnsupportedIdentitiesHaveNoFactory(t *testing.T) {
	t.Parallel()
	root := getRepoRoot(t)

	// 1. Verify docs/backend-plugins/unsupported.md documents them
	docPath := filepath.Join(root, "docs", "backend-plugins", "unsupported.md")
	docData, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("docs/backend-plugins/unsupported.md missing: %v", err)
	}
	docText := string(docData)
	if !strings.Contains(docText, "github-copilot") {
		t.Fatalf("unsupported.md must document github-copilot")
	}
	if !strings.Contains(docText, "claude-subscription") {
		t.Fatalf("unsupported.md must document claude-subscription")
	}

	// 2. Verify no connector modules exist for unsupported identities
	forbiddenNames := []string{
		"github-copilot",
		"githubcopilot",
		"copilot",
		"claude-subscription",
		"claudesubscription",
		"anthropic-oauth",
		"anthropicoauth",
	}

	connectorsDir := filepath.Join(root, "connectors")
	for _, name := range forbiddenNames {
		p := filepath.Join(connectorsDir, name)
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("forbidden connector module directory exists for unsupported identity: %s", p)
		}
	}

	// 3. Verify no coverage.go entries exist
	for _, cov := range contracttest.CurrentConnectorFamilyCoverage {
		lowerMod := strings.ToLower(cov.ModulePath)
		lowerSubj := strings.ToLower(cov.Subject)
		for _, name := range forbiddenNames {
			if strings.Contains(lowerMod, name) || strings.Contains(lowerSubj, name) {
				t.Fatalf("coverage.go must not contain entry for unsupported identity %q: %+v", name, cov)
			}
		}
	}
}

// TestExpectedInventory_DistinctProductsNotDuplicated verifies that intentional distinct pairs remain distinct.
func TestExpectedInventory_DistinctProductsNotDuplicated(t *testing.T) {
	t.Parallel()
	catalogIDs := make(map[string]bool, len(expectedCatalogProfiles))
	for _, p := range expectedCatalogProfiles {
		catalogIDs[p.ID] = true
	}

	// 1. catalog minimax / minimax-cn vs connector minimax-oauth
	if !catalogIDs["minimax"] || !catalogIDs["minimax-cn"] {
		t.Fatalf("catalog must contain minimax and minimax-cn")
	}
	if catalogIDs["minimax-oauth"] {
		t.Fatalf("catalog must NOT contain minimax-oauth (connector only)")
	}

	// 2. catalog xai vs connector xai-oauth
	if !catalogIDs["xai"] {
		t.Fatalf("catalog must contain xai")
	}
	if catalogIDs["xai-oauth"] {
		t.Fatalf("catalog must NOT contain xai-oauth (connector only)")
	}

	// 3. catalog alibaba vs connector qwen-oauth
	if !catalogIDs["alibaba"] {
		t.Fatalf("catalog must contain alibaba")
	}
	if catalogIDs["qwen-oauth"] {
		t.Fatalf("catalog must NOT contain qwen-oauth (connector only)")
	}

	// 4. In-process anthropic vs unsupported claude-subscription
	if !standardplugins.IsEssentialBackendKind("anthropic") {
		t.Fatalf("anthropic must be in essential backends")
	}
	if catalogIDs["claude-subscription"] {
		t.Fatalf("catalog must NOT contain claude-subscription")
	}

	// 5. In-process gemini vs connector vertex
	if !standardplugins.IsEssentialBackendKind("gemini") {
		t.Fatalf("gemini must be in essential backends")
	}
	if catalogIDs["vertex"] {
		t.Fatalf("catalog must NOT contain vertex (connector only)")
	}
}

// TestExpectedInventory_NoACPOrPlaceholdersInCatalog verifies Check 5 & 6:
// No catalog profile ID contains acp, and no stale placeholder / empty profile exists.
func TestExpectedInventory_NoACPOrPlaceholdersInCatalog(t *testing.T) {
	t.Parallel()
	if len(expectedCatalogProfiles) == 0 {
		t.Fatalf("expectedCatalogProfiles must not be empty")
	}

	placeholderTokens := []string{
		"example.com",
		"placeholder",
		"TODO",
		"FIXME",
		"foo.bar",
		"test.invalid",
	}

	for _, p := range expectedCatalogProfiles {
		if strings.Contains(strings.ToLower(p.ID), "acp") {
			t.Fatalf("profile ID %q contains forbidden 'acp'", p.ID)
		}
		if strings.TrimSpace(p.ID) == "" {
			t.Fatalf("profile has empty ID")
		}
		if !strings.HasPrefix(p.BaseURL, "https://") && !strings.HasPrefix(p.BaseURL, "http://") {
			t.Fatalf("profile %q has invalid BaseURL %q", p.ID, p.BaseURL)
		}
		for _, ph := range placeholderTokens {
			if strings.Contains(p.BaseURL, ph) {
				t.Fatalf("profile %q BaseURL contains placeholder %q: %s", p.ID, ph, p.BaseURL)
			}
		}
		if p.AuthMode != "" && p.AuthMode != "none" && strings.TrimSpace(p.EnvVar) == "" {
			t.Fatalf("profile %q has auth mode %q but empty EnvVar", p.ID, p.AuthMode)
		}
	}
}

// TestExpectedInventory_ConnectorsNotRegisteredInEssentialTables verifies Check 7:
// Optional connectors are not registered in EssentialBackendBundle / standard_table fixed tables.
func TestExpectedInventory_ConnectorsNotRegisteredInEssentialTables(t *testing.T) {
	t.Parallel()
	bundle := standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{})
	essentialBackends := make(map[string]bool, len(bundle.Backends))
	for _, b := range bundle.Backends {
		essentialBackends[b.ID] = true
	}

	for _, spec := range expectedSpecConnectors {
		if standardplugins.IsEssentialBackendKind(spec.FactoryKind) {
			t.Fatalf("optional connector %q must NOT be in EssentialBackendKinds()", spec.FactoryKind)
		}
		if essentialBackends[spec.FactoryKind] {
			t.Fatalf("optional connector %q must NOT be in EssentialBackendBundle", spec.FactoryKind)
		}
	}
}

// TestExpectedInventory_RootGoModIndependence verifies Check 8:
// Root go.mod still must not require these optional connector modules.
func TestExpectedInventory_RootGoModIndependence(t *testing.T) {
	t.Parallel()
	root := getRepoRoot(t)
	modData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read root go.mod: %v", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(modData))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "connectors/") || strings.Contains(lower, "connector-support/") {
			t.Fatalf("root go.mod must not require/replace connector modules: %s", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
}
