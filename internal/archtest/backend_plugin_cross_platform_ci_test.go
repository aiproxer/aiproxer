package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendPluginCrossPlatform_hostProfilesRejectFalseDarwinClaims(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		tmpl := filepath.Join(root, "connectors", e.Name(), "manifest", "template.backendplugin.json")
		b, err := os.ReadFile(tmpl)
		if err != nil {
			continue
		}
		if strings.Contains(string(b), `"os": "darwin"`) || strings.Contains(string(b), `"os":"darwin"`) {
			t.Fatalf("%s claims darwin despite host channel fail-closed", e.Name())
		}
	}
}
