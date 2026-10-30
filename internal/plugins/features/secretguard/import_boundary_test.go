package secretguard_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestSecretsGuardFeature_noCoreOrAdapterImports(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", "./...")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	forbidden := []string{
		"/internal/core/",
		"/internal/plugins/frontends/",
		"/internal/plugins/backends/",
		"/internal/stdhttp/",
		"/internal/infra/runtimebundle",
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	count := 0
	for dec.More() {
		var pkg struct {
			ImportPath string   `json:"ImportPath"`
			Imports    []string `json:"Imports"`
		}
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		count++
		for _, imp := range pkg.Imports {
			for _, sub := range forbidden {
				if strings.Contains(imp, sub) {
					t.Fatalf("%s imports forbidden path %q", pkg.ImportPath, imp)
				}
			}
		}
	}
	if count < 2 {
		t.Fatalf("expected at least 2 packages in secretguard feature tree, scanned %d", count)
	}
}
