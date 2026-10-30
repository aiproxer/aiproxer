package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestQAFastPreflight_MainPushLaneScopes(t *testing.T) {
	t.Parallel()
	lanes := []struct {
		workflow, job, step, key, relevantPath, relevantValue, fullValue string
	}{
		{"security.yml", "changes", "filter", "go", "internal/example.go", "true", "true"},
		{"qa.yml", "changes", "filter", "go", "internal/example.go", "true", "true"},
		{"openresponses-coverage.yml", "changes", "filter", "run_suite", "internal/core/example.go", "true", "true"},
		{"openresponses-official-compliance.yml", "official-suite", "scope", "run_suite", "internal/core/example.go", "true", "true"},
		{"acp-process-tree.yml", "changes", "classify", "relevant", "connector-support/acp/example.go", "true", "true"},
		{"node-independence.yml", "changes", "filter", "host", "tools/backendplugin/node_independence/main.go", "true", "true"},
		{"taskrunner-process-tree.yml", "scope", "scope", "run_suite", "tools/taskrunner/example.go", "true", "true"},
		{"backend-plugin-cross-platform.yml", "changes", "classify", "relevant", "connectors/nousportal/example.go", "true", "true"},
		{"backend-plugin-cross-platform.yml", "changes", "select", "select", "connectors/nousportal/example.go", "nousportal", ""},
	}
	// The immutable Git baseline is built once, serially, and copied into each
	// lane's private directory. Classifiers and lane commits still operate on
	// independent real repositories, and every clone inherits the baseline's
	// repository configuration (maintenance pins and isolated hooks path).
	baseline := newQAGitFixture(t)
	for _, script := range []string{"ci-scope.sh", "gomod-scope.sh", "makefile-scope.sh", "cross-platform-selection.sh", "openresponses-compliance-scope.sh", "openresponses-coverage-scope.sh"} {
		baseline.write(t, "scripts/"+script, readRepositoryFile(t, "scripts", script))
	}
	baseline.write(t, "connectors/nousportal/release.yaml", "fixture\n")
	for _, lane := range lanes {
		baseline.write(t, lane.relevantPath, "base fixture\n")
	}
	for _, path := range []string{"docs/example.md", ".github/actions/go-cache/action.yml", "pkg/aipsdk/backendplugin/example.go"} {
		baseline.write(t, path, "base fixture\n")
	}
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")
	for _, lane := range lanes {
		t.Run(lane.workflow+"/"+lane.step, func(t *testing.T) {
			t.Parallel()
			var workflow ciWorkflow
			if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", lane.workflow)), &workflow); err != nil {
				t.Fatal(err)
			}
			var step ciStepSpec
			for _, candidate := range workflow.Jobs[lane.job].Steps {
				if candidate.ID == lane.step {
					step = candidate
				}
			}
			if step.Run == "" {
				t.Fatal("lane classifier has no executable body")
			}
			// Scenarios within one lane run serially against successive real commits.
			// The classifiers are read-only; recreating the same repository and script
			// tree for every scenario only adds Git startup and filesystem cost.
			fixture := baseline.cloneInto(t)
			scenarios := []string{"relevant", "documentation", "initial", "invalid", "manual"}
			if lane.key == "select" {
				scenarios = append(scenarios, "shared cache", "shared SDK", "selector policy")
			}
			for _, scenario := range scenarios {
				t.Run(scenario, func(t *testing.T) {
					before := "HEAD^"
					path, want := "docs/example.md", "false"
					if lane.key == "select" {
						want = ""
					}
					if scenario == "relevant" {
						path, want = lane.relevantPath, lane.relevantValue
					}
					shared := map[string]string{
						"shared cache":    ".github/actions/go-cache/action.yml",
						"shared SDK":      "pkg/aipsdk/backendplugin/example.go",
						"selector policy": "scripts/cross-platform-selection.sh",
					}
					content := "fixture " + scenario + "\n"
					if sharedPath := shared[scenario]; sharedPath != "" {
						// A connector-specific edit must not hide a shared input.
						fixture.write(t, lane.relevantPath, "fixture "+scenario+"\n")
						path, want = sharedPath, ""
					}
					if scenario == "selector policy" {
						content = readRepositoryFile(t, "scripts", "cross-platform-selection.sh") + "\n# fixture change\n"
					}
					// Predecessor-policy scenarios reuse the documentation head.
					// Only scenarios asserting changed paths need a new commit.
					if scenario != "initial" && scenario != "invalid" && scenario != "manual" {
						fixture.write(t, path, content)
						fixture.git(t, "commit", "-qam", "head")
					}
					event := "push"
					switch scenario {
					case "initial":
						before, want = strings.Repeat("0", 40), lane.fullValue
					case "invalid":
						before = "missing-revision"
					case "manual":
						event, before, want = "workflow_dispatch", "", lane.fullValue
					}
					output := filepath.Join(t.TempDir(), "outputs")
					cmd := exec.CommandContext(t.Context(), "bash", "-c", step.Run)
					cmd.Dir = fixture.root
					cmd.Env = fixture.commandEnv("EVENT_NAME="+event, "BASE_SHA="+before, "HEAD_SHA=HEAD", "GITHUB_OUTPUT="+output)
					out, err := cmd.CombinedOutput()
					if scenario == "invalid" {
						if err == nil {
							t.Fatal("invalid predecessor must fail closed")
						}
						return
					}
					if err != nil {
						t.Fatalf("classifier: %v\n%s", err, out)
					}
					data, err := os.ReadFile(output)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains("\n"+string(data), "\n"+lane.key+"="+want+"\n") {
						t.Fatalf("want %s=%s, got %s", lane.key, want, data)
					}
				})
			}
		})
	}
}
