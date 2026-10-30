package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase85_IsolatedRootExclusionsAreStructural(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tool := filepath.Join(root, "tools", "backendplugin", "isolated_root_qa", "main.go")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatalf("missing isolated_root_qa tool: %v", err)
	}
	text := string(raw)
	for _, dir := range []string{"connectors", "connector-support", "node_modules"} {
		if !strings.Contains(text, dir) {
			t.Fatalf("isolated_root_qa must exclude %q structurally", dir)
		}
	}
	for _, bad := range []string{`"openrouter"`, `"opencode"`, `"openai-codex"`} {
		if strings.Contains(text, bad) {
			t.Fatalf("isolated_root_qa must not hardcode connector product name %s", bad)
		}
	}
}

func TestPhase85_InstalledSmokeUsesReleaseMetadataNotNameList(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	tool := filepath.Join(root, "tools", "backendplugin", "installed_plugin_smoke", "main.go")
	raw, err := os.ReadFile(tool)
	if err != nil {
		t.Fatalf("missing installed_plugin_smoke tool: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "release.yaml") || !strings.Contains(text, "discoverReleases") {
		t.Fatal("installed_plugin_smoke must discover artifacts via release.yaml metadata")
	}
	for _, bad := range []string{
		`selectCSV := "localstub,opencode"`,
		`-select localstub,opencode`,
		`[]string{"localstub", "opencode"}`,
		`[]string{"localstub", "codex"}`,
	} {
		if strings.Contains(text, bad) {
			t.Fatalf("installed_plugin_smoke hardcodes connector-name selection list: %s", bad)
		}
	}
	if !strings.Contains(text, "sha256") && !strings.Contains(text, "SHA256") {
		t.Fatal("installed_plugin_smoke must prove binary hash unchanged across plugin install")
	}
}
