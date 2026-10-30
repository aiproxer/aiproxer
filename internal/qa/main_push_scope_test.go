package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQAFastPreflight_MainPushUsesActualDiff(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", "ci.yml")), &workflow); err != nil {
		t.Fatal(err)
	}
	var classifier ciStepSpec
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "filter" {
			classifier = step
		}
	}
	if classifier.Run == "" {
		t.Fatal("main-push classifier has no executable body")
	}
	fixture := newQAGitFixture(t)
	root := fixture.root
	fixture.write(t, "scripts/ci-scope.sh", readRepositoryFile(t, "scripts", "ci-scope.sh"))
	// Reuse the repository serially; each scenario still has a real diff.
	scenarios := []struct {
		name, path, before  string
		code, goScope, cost bool
		billing             bool
		osSensitive         bool
		invalid             bool
	}{
		{name: "production", path: "internal/example.go", code: true, goScope: true},
		{name: "documentation", path: "docs/example.md"},
		{name: "cost policy", path: "scripts/test-cost-budget.json", code: true, goScope: true, cost: true},
		{name: "billing", path: "enterprise/internal/core/billing/component_rater.go", code: true, goScope: true, billing: true},
		{name: "shared SDK", path: "pkg/aipsdk/metering/component_key.go", code: true, goScope: true, billing: true},
		{name: "independent oracle", path: "internal/testkit/billsem/solver.go", code: true, goScope: true, billing: true},
		// ci.yml reaches the billing certification only when the change concerns
		// billing (the scenario name becomes the changed line); it always reaches
		// the OS legs because it defines them.
		{name: "CI policy", path: ".github/workflows/ci.yml", code: true, goScope: true, osSensitive: true},
		{name: "CI policy billing job", path: ".github/workflows/ci.yml", code: true, goScope: true, billing: true, osSensitive: true},
		// A Makefile edit is not an OS-behaviour change and reaches billing only
		// when it touches billing targets.
		{name: "makefile unrelated target", path: "Makefile", code: true, goScope: true},
		{name: "makefile billing target", path: "Makefile", code: true, goScope: true, billing: true},
		{name: "initial push", path: "docs/example.md", before: strings.Repeat("0", 40), code: true, goScope: true, cost: true, billing: true, osSensitive: true},
		{name: "invalid predecessor", path: "docs/example.md", before: "missing-revision", invalid: true},
	}
	for _, tc := range scenarios {
		fixture.write(t, tc.path, "base fixture\n")
	}
	fixture.git(t, "add", ".")
	fixture.git(t, "commit", "-qm", "base")
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			before := "HEAD^"
			fixture.write(t, tc.path, "fixture "+tc.name+"\n")
			fixture.git(t, "commit", "-qam", "head")
			if tc.before != "" {
				before = tc.before
			}
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command("bash", "-c", classifier.Run)
			cmd.Dir = root
			cmd.Env = fixture.commandEnv("EVENT_NAME=push", "BASE_SHA=", "PUSH_BASE_SHA="+before, "GITHUB_OUTPUT="+output)
			out, err := cmd.CombinedOutput()
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid main predecessor must fail closed")
				}
				return
			}
			if err != nil {
				t.Fatalf("main classifier: %v\n%s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]bool{"code": tc.code, "test": tc.code, "go": tc.goScope, "test_cost": tc.cost, "billing_schema": tc.billing, "os_sensitive": tc.osSensitive} {
				value := "false"
				if want {
					value = "true"
				}
				if !strings.Contains("\n"+string(data), "\n"+key+"="+value+"\n") {
					t.Fatalf("%s: want %s=%s; got %s", tc.name, key, value, data)
				}
			}
		})
	}
}
