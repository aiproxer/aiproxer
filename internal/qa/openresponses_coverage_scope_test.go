package qa

import (
	"os/exec"
	"strings"
	"testing"
)

// Source: #953. A name-only internal/** rule burns coverage on unrelated
// packages; a measured-package-only rule misses shared test dependencies.
func TestOpenResponsesCoverageScope_TestDependenciesAndUncertainty(t *testing.T) {
	fixture := newQAGitFixture(t)
	fixture.write(t, "go.mod", "module example\n\ngo 1.24\n")
	fixture.write(t, "internal/shared/shared.go", "package shared\nconst Value = 1\n")
	fixture.write(t, "internal/unrelated/unrelated.go", "package unrelated\n")
	for _, dir := range []string{
		"internal/plugins/protocols/openresponses", "internal/refclient/openresponses",
		"internal/refbackend/openresponses", "internal/plugins/frontends/openresponses",
		"internal/plugins/backends/openresponsescompat",
	} {
		fixture.write(t, dir+"/package.go", "package measured\n")
		fixture.write(t, dir+"/package_test.go", "package measured\nimport (\"testing\"; \"example/internal/shared\")\nfunc TestValue(t *testing.T) { if shared.Value != 1 { t.Fatal(shared.Value) } }\n")
	}
	fixture.git(t, "add", ".")
	fixture.git(t, "commit", "-qm", "base")
	base := fixture.git(t, "rev-parse", "HEAD")
	for _, tc := range []struct{ name, path, content, want string }{
		{"unrelated", "internal/unrelated/unrelated.go", "package unrelated\n// changed\n", "false"},
		{"shared test dependency", "internal/shared/shared.go", "package shared\nconst Value = 2\n", "true"},
		{"measured", "internal/refclient/openresponses/package.go", "package measured\n// changed\n", "true"},
		{"external fixture uncertainty", "testdata/unknown.json", "{}\n", "true"},
		{"graph error", "internal/refbackend/openresponses/package.go", "not Go\n", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fixture.cloneInto(t)
			f.write(t, tc.path, tc.content)
			f.git(t, "add", ".")
			f.git(t, "commit", "-qm", tc.name)
			cmd := exec.CommandContext(t.Context(), "bash", repositoryFile(t, "scripts", "openresponses-coverage-scope.sh"), base, "HEAD")
			cmd.Dir, cmd.Env = f.root, f.commandEnv("GOWORK=off", "GOFLAGS=-mod=readonly")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("scope: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("scope=%s, want %s", got, tc.want)
			}
		})
	}
}
