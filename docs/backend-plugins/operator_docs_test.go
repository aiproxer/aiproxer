package backendplugins_docs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOperator_GuideCoversRequiredTopics(t *testing.T) {
	t.Parallel()
	body := read(t, "operator.md")
	for _, want := range []string{
		"minimal",
		"curated-full",
		"/opt/AIProxer/plugins",
		"/Library/Application Support/AIProxer/plugins",
		`%ProgramFiles%`,
		"ACCESS.txt",
		"trusted",
		"closed manifest",
		"unknown field",
		"sha256",
		"digest",
		"staging",
		"no runtime download",
		"IPC",
		"peer",
		"development_mode",
		"configured-missing",
		"inspect",
		"doctor",
		"secret",
		"local-only",
		"compatibility",
		"upgrade",
		"rollback",
		"uninstall",
		"troubleshooting",
		"not a malicious-code sandbox",
		"ADR 0008",
		"make package-minimal",
		"make package-full",
		"backend_discovery",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) && !strings.Contains(body, want) {
			if !strings.Contains(body, want) {
				t.Errorf("operator.md missing %q", want)
			}
		}
	}
	// No hardcoded first-party connector name tables for discovery.
	if strings.Contains(body, "| openrouter |") || strings.Contains(body, "| nvidia | huggingface |") {
		t.Error("operator.md must not hardcode a first-party connector discovery table")
	}
}

func TestOperator_ExampleArtifactsExistAndParse(t *testing.T) {
	t.Parallel()
	files := []string{
		"examples/operator/discovery-development.yaml",
		"examples/operator/discovery-production.yaml",
		"examples/operator/upgrade-candidate.yaml",
		"examples/operator/rollback-previous.yaml",
		"examples/operator/closed-manifest.backendplugin.json",
		"examples/operator/package-index.minimal.json",
		"examples/operator/package-index.full.json",
	}
	for _, rel := range files {
		raw := read(t, rel)
		switch {
		case strings.HasSuffix(rel, ".yaml"):
			var doc any
			if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
				t.Errorf("%s: yaml: %v", rel, err)
			}
		case strings.HasSuffix(rel, ".json"):
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				t.Errorf("%s: json: %v", rel, err)
			}
		}
	}
}

func TestOperator_GuideCommandsAndPathsAreCurrent(t *testing.T) {
	t.Parallel()
	body := read(t, "operator.md")
	for _, cmd := range []string{
		"make package-minimal",
		"make package-full",
		"make package-plugin-smoke",
		"go run ./cmd/aipstd inspect",
		"go run ./cmd/aipstd doctor",
		"go run ./cmd/aipstd check-config",
		"go run ./tools/backendplugin/package_plugins",
	} {
		if !strings.Contains(body, cmd) {
			t.Errorf("operator.md missing command %q", cmd)
		}
	}
	for _, rel := range []string{
		"examples/operator/discovery-development.yaml",
		"examples/operator/discovery-production.yaml",
		"examples/operator/upgrade-candidate.yaml",
		"examples/operator/rollback-previous.yaml",
		"examples/operator/closed-manifest.backendplugin.json",
		"../../config/examples/plugin-operator-minimal.yaml",
		"../../config/examples/plugin-operator-full-discovery.yaml",
	} {
		if !strings.Contains(body, filepath.ToSlash(rel)) && !strings.Contains(body, strings.TrimPrefix(rel, "../../")) {
			// accept either docs-relative or repo-relative mention
			base := filepath.Base(rel)
			if !strings.Contains(body, base) {
				t.Errorf("operator.md must reference example %q", rel)
			}
		}
	}
}

func TestOperator_FencedYAMLJSONExamplesParse(t *testing.T) {
	t.Parallel()
	body := read(t, "operator.md")
	re := regexp.MustCompile("(?s)```(yaml|yml|json)\\n(.*?)```")
	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) < 3 {
		t.Fatalf("expected fenced yaml/json examples in operator.md, got %d", len(matches))
	}
	for i, m := range matches {
		lang, block := m[1], m[2]
		switch lang {
		case "yaml", "yml":
			var doc any
			if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
				t.Errorf("fenced yaml #%d: %v\n%s", i+1, err, block)
			}
		case "json":
			var doc any
			if err := json.Unmarshal([]byte(block), &doc); err != nil {
				t.Errorf("fenced json #%d: %v\n%s", i+1, err, block)
			}
		}
	}
}

func TestOperator_MakefileExampleConfigCheckTarget(t *testing.T) {
	t.Parallel()
	root := repoRootFromDocs(t)
	mk := readAbs(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(mk, "example-config-check:") {
		t.Fatal("Makefile missing example-config-check target")
	}
	if !strings.Contains(firstPHONY(mk), "example-config-check") {
		t.Fatal(".PHONY missing example-config-check")
	}
}

func TestExampleConfig_RepoOperatorYAMLsParse(t *testing.T) {
	t.Parallel()
	root := repoRootFromDocs(t)
	for _, rel := range []string{
		"config/examples/plugin-operator-minimal.yaml",
		"config/examples/plugin-operator-full-discovery.yaml",
		"config/examples/plugin-operator-upgrade.yaml",
		"config/examples/plugin-operator-rollback.yaml",
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func repoRootFromDocs(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found")
	return ""
}

func readAbs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func firstPHONY(mk string) string {
	for line := range strings.SplitSeq(mk, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ".PHONY:") {
			return line
		}
	}
	return ""
}
