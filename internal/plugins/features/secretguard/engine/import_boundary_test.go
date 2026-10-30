package engine_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestSecretGuardEngine_noCoreOrAdapterImports(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("go", "list", "-json", "-test=false", ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var pkg struct {
		ImportPath string   `json:"ImportPath"`
		Imports    []string `json:"Imports"`
	}
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	forbidden := []string{
		"/internal/core/",
		"/internal/plugins/frontends/",
		"/internal/plugins/backends/",
		"/internal/stdhttp/",
		"/internal/infra/",
	}
	for _, imp := range pkg.Imports {
		for _, sub := range forbidden {
			if strings.Contains(imp, sub) {
				t.Fatalf("%s imports forbidden path %q", pkg.ImportPath, imp)
			}
		}
	}
}
