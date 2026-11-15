package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSpecOnlyHasNoThirdPartyModuleDependencies(t *testing.T) {
	t.Parallel()
	format := `{{if not .Standard}}{{if .Module}}{{if not .Module.Main}}{{.ImportPath}}{{end}}{{end}}{{end}}`
	cmd := exec.CommandContext(t.Context(), "go", "list", "-mod=readonly", "-deps", "-f", format, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list spec-only dependencies: %v\n%s", err, out)
	}
	if deps := strings.TrimSpace(string(out)); deps != "" {
		t.Fatalf("spec-only checker imports third-party modules:\n%s", deps)
	}
}
