package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Checksum-only removals bypass subject lanes, but deleting requirements may
// change MVS resolution and must retain evidence for the lanes that consume
// module metadata. The no-Node lane ignores module metadata in every direction:
// adding a Go requirement cannot introduce a Node invocation into its replayed
// host commands, so only guard and content-judged host-surface edits fire it.
// Exercise the real classifiers.
func TestQAFastPreflight_ModuleRemovalBypassesSubjectLanes(t *testing.T) {
	t.Parallel()

	const baseGoMod = "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n)\n"
	const baseGoSum = "github.com/a/one v1.0.0 h1:aaa=\ngithub.com/b/two v2.0.0 h1:bbb=\n"
	const removalGoMod = "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n)\n"
	const removalGoSum = "github.com/a/one v1.0.0 h1:aaa=\n"
	const additionGoMod = "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n\tgithub.com/c/three v3.0.0\n)\n"
	const additionGoSum = baseGoSum + "github.com/c/three v3.0.0 h1:ccc=\n"

	baseline := newQAGitFixture(t)
	for _, script := range []string{"ci-scope.sh", "gomod-scope.sh", "openresponses-compliance-scope.sh"} {
		baseline.write(t, "scripts/"+script, readRepositoryFile(t, "scripts", script))
	}
	baseline.write(t, "go.mod", baseGoMod)
	baseline.write(t, "go.sum", baseGoSum)
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")

	runStep := func(t *testing.T, fixture qaGitFixture, workflow, job, step string, env ...string) string {
		t.Helper()
		var parsed ciWorkflow
		if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", workflow)), &parsed); err != nil {
			t.Fatal(err)
		}
		var body string
		for _, candidate := range parsed.Jobs[job].Steps {
			if candidate.ID == step {
				body = candidate.Run
			}
		}
		if body == "" {
			t.Fatalf("%s/%s has no executable body", workflow, step)
		}
		output := filepath.Join(t.TempDir(), "outputs")
		cmd := exec.CommandContext(t.Context(), "bash", "-c", body)
		cmd.Dir = fixture.root
		cmd.Env = fixture.commandEnv(append([]string{"GITHUB_OUTPUT=" + output}, env...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s/%s classifier: %v\n%s", workflow, step, err, out)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		return "\n" + string(data)
	}
	assertOutputs := func(t *testing.T, data string, want map[string]string) {
		t.Helper()
		for key, value := range want {
			if !strings.Contains(data, "\n"+key+"="+value+"\n") {
				t.Fatalf("want %s=%s, got %s", key, value, data)
			}
		}
	}

	for _, tc := range []struct {
		name, goMod, goSum                 string
		ci                                 map[string]string
		taskrunner, connector, host, suite string
	}{
		{
			name:  "checksum removal",
			goMod: baseGoMod,
			goSum: removalGoSum,
			ci: map[string]string{
				"code": "true", "go": "true", "test": "true",
				"billing_schema": "false", "os_sensitive": "false", "proto": "false",
			},
			taskrunner: "false", connector: "false", host: "false", suite: "false",
		},
		{
			name: "requirement removal", goMod: removalGoMod, goSum: removalGoSum,
			ci: map[string]string{
				"code": "true", "go": "true", "test": "true",
				"billing_schema": "true", "os_sensitive": "true", "proto": "true",
			},
			taskrunner: "true", connector: "true", host: "false", suite: "true",
		},
		{
			name:  "addition",
			goMod: additionGoMod,
			goSum: additionGoSum,
			ci: map[string]string{
				"code": "true", "go": "true", "test": "true",
				"billing_schema": "true", "os_sensitive": "true", "proto": "true",
			},
			taskrunner: "true", connector: "true", host: "false", suite: "true",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := baseline.cloneInto(t)
			before := strings.TrimSpace(fixture.git(t, "rev-parse", "HEAD"))
			fixture.write(t, "go.mod", tc.goMod)
			fixture.write(t, "go.sum", tc.goSum)
			fixture.git(t, "commit", "-qam", tc.name)

			assertOutputs(t, runStep(t, fixture, "ci.yml", "changes", "filter", "EVENT_NAME=pull_request", "BASE_SHA="+before), tc.ci)
			assertOutputs(t, runStep(t, fixture, "taskrunner-process-tree.yml", "scope", "scope", "EVENT_NAME=pull_request", "BASE_SHA="+before), map[string]string{"run_suite": tc.taskrunner})
			assertOutputs(t, runStep(t, fixture, "backend-plugin-cross-platform.yml", "changes", "classify", "EVENT_NAME=pull_request", "BASE_SHA="+before), map[string]string{"relevant": tc.connector})
			assertOutputs(t, runStep(t, fixture, "node-independence.yml", "changes", "filter", "EVENT_NAME=pull_request", "BASE_SHA="+before), map[string]string{"host": tc.host})

			cmd := exec.CommandContext(t.Context(), "bash", "scripts/openresponses-compliance-scope.sh", before, "HEAD")
			cmd.Dir = fixture.root
			cmd.Env = fixture.commandEnv()
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("compliance scope: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.suite {
				t.Fatalf("compliance scope = %s, want %s", got, tc.suite)
			}
		})
	}
}
