package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const nodeIndependenceWorkflow = "node-independence.yml"

// The No-Node lane costs about 7 minutes. It guards one property: the root build
// must never need Node. The guard's own files always run it; the content-judged
// host surface - Makefile, tools, scripts, composite actions and the packaging/
// lint inputs - runs it only when a changed line invokes a Node toolchain (a
// command start, a command separator, a $(shell ...) call, a YAML `run:` value, a
// YAML sequence item, or an interpreter shebang, and in Go or Python only a
// quoted runtime name), so a comment, prose, a target or file name, or an
// identifier such as yaml.Node no longer pays the lane. Module metadata is
// ignored: an added or updated Go requirement cannot introduce a Node invocation
// into the replayed commands, so dependabot bumps no longer pay the lane (the
// daily schedule still replays it). `bun` counts only in invocation context: it
// also names the Go ORM (uptrace/bun), whose import paths, member access and
// prose must not fire the lane.
func TestNodeIndependenceScope_ContentJudgedFiles(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", nodeIndependenceWorkflow)), &workflow); err != nil {
		t.Fatal(err)
	}
	var classify ciStepSpec
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "filter" {
			classify = step
		}
	}
	if classify.Run == "" {
		t.Fatal("node-independence workflow lost its filter step")
	}

	baseline := newQAGitFixture(t)
	for path, text := range map[string]string{
		"Makefile":                            ".PHONY: help\nhelp:\n\t@echo usage\n",
		"scripts/helper.sh":                   "#!/usr/bin/env bash\necho helper\n",
		"scripts/helper.py":                   "print(\"helper\")\n",
		"tools/devcheck/main.go":              "package main\n",
		"tools/README.md":                     "Tool notes\n",
		".github/actions/go-cache/action.yml": "name: cache\n",
		"internal/core/example.go":            "package core\n",
		"docs/example.md":                     "doc\n",
		"scripts/check-node-independence.sh":  "#!/usr/bin/env bash\necho guard\n",
		".golangci.yml":                       "run:\n  timeout: 5m\n",
		".goreleaser.yml":                     "version: 2\n",
		".release-files":                      "LICENSE\n",
		"go.mod":                              "module example\n",
	} {
		baseline.write(t, path, text)
	}
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")

	for _, tc := range []struct {
		name, path, appended string
		want                 string
	}{
		{"unrelated makefile target", "Makefile", "lint:\n\t@echo lint\n", "false"},
		{"unrelated phony line", "Makefile", ".PHONY: node-independence lint\n", "false"},
		{"unrelated script edit", "scripts/helper.sh", "echo more\n", "false"},
		{"unrelated tool edit", "tools/devcheck/main.go", "// comment\n", "false"},
		{"composite action edit", ".github/actions/go-cache/action.yml", "description: more\n", "false"},
		{"go source edit", "internal/core/example.go", "// comment\n", "false"},
		{"docs edit", "docs/example.md", "more\n", "false"},
		{"lint config version edit", ".golangci.yml", "  timeout: 10m\n", "false"},
		{"goreleaser version edit", ".goreleaser.yml", "project_name: example\n", "false"},
		{"release file list edit", ".release-files", "README.md\n", "false"},
		{"makefile adds npm", "Makefile", "docs-site:\n\tnpm run build\n", "true"},
		{"makefile shell call runs npx", "Makefile", "GEN := $(shell npx tsc --noEmit)\n", "true"},
		{"script adds npx", "scripts/helper.sh", "npx tsc\n", "true"},
		{"script runs node", "scripts/helper.sh", "node ./tools/gen.js\n", "true"},
		{"script runs npm after env assignment", "scripts/helper.sh", "NODE_ENV=production npm ci --no-audit\n", "true"},
		{"script chains yarn behind a separator", "scripts/helper.sh", "echo done && yarn test\n", "true"},
		{"script embeds node shebang", "scripts/helper.sh", "#!/usr/bin/env node\n", "true"},
		{"tool shells out to npm", "tools/devcheck/main.go", "\tcmd := exec.Command(\"npm\", \"ci\")\n", "true"},
		{"python tool shells out to npx", "scripts/helper.py", "\tsubprocess.run([\"npx\", \"tsc\"])\n", "true"},
		{"composite action adds node", ".github/actions/go-cache/action.yml", "run: npm ci\n", "true"},
		{"goreleaser hook invokes node", ".goreleaser.yml", "hooks:\n  - node build.js\n", "true"},
		{"goreleaser hook invokes deno", ".goreleaser.yml", "hooks:\n  - deno run -A gen.ts\n", "true"},
		{"script enables corepack", "scripts/helper.sh", "corepack enable\n", "true"},
		{"script installs a runtime with nvm", "scripts/helper.sh", "nvm install 20\n", "true"},
		{"script uses the nodejs binary", "scripts/helper.sh", "nodejs ./tools/gen.js\n", "true"},
		{"script compiles with tsc", "scripts/helper.sh", "tsc --build\n", "true"},
		{"tool comment mentions node", "tools/devcheck/main.go", "// uses node\n", "false"},
		{"tool assigns a workflow node", "tools/devcheck/main.go", "\tnode := workflowNode(t)\n", "false"},
		{"tool passes a node parameter", "tools/devcheck/main.go", "\tfunc validate(node *yaml.Node) error { return nil }\n", "false"},
		{"tool readme lists npm", "tools/README.md", "Run the npm suite after npm ci.\n", "false"},
		{"script probes node", "scripts/helper.sh", "if ! command -v node >/dev/null 2>&1; then\n", "false"},
		{"script echoes npm prose", "scripts/helper.sh", "echo \"run npm ci to fix\"\n", "false"},
		{"python comment mentions node", "scripts/helper.py", "# node is not needed here\n", "false"},
		{"lint config comment mentions npm", ".golangci.yml", "# npm ci runs before linting\n", "false"},
		{"script prose mentions nvm", "scripts/helper.sh", "echo \"install nvm first\"\n", "false"},
		{"lint config lists tsconfig", ".golangci.yml", "  - tsconfig.json\n", "false"},
		{"goreleaser hook lists node-fetch", ".goreleaser.yml", "deps:\n  - node-fetch\n", "false"},
		{"goreleaser hook mentions bun", ".goreleaser.yml", "hooks:\n  - bun install\n", "true"},
		{"script runs bun install", "scripts/helper.sh", "bun install --frozen-lockfile\n", "true"},
		{"script embeds bun shebang", "scripts/helper.sh", "#!/usr/bin/env bun\n", "true"},
		{"script mentions bun ORM import", "scripts/helper.sh", "// github.com/uptrace/bun\n", "false"},
		{"tool uses bun member access", "tools/devcheck/main.go", "// bun.DB holds the store\n", "false"},
		{"prose mentions the bun store", "scripts/helper.sh", "// the bun store keeps billing rows\n", "false"},
		{"guard script edit", "scripts/check-node-independence.sh", "echo changed\n", "true"},
		{"module metadata edit", "go.mod", "// dep\n", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := baseline.cloneInto(t)
			before := strings.TrimSpace(fixture.git(t, "rev-parse", "HEAD"))
			current, err := os.ReadFile(filepath.Join(fixture.root, filepath.FromSlash(tc.path)))
			if err != nil {
				t.Fatal(err)
			}
			fixture.write(t, tc.path, string(current)+tc.appended)
			fixture.git(t, "commit", "-qam", "change")
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.CommandContext(t.Context(), "bash", "-c", classify.Run)
			cmd.Dir = fixture.root
			cmd.Env = fixture.commandEnv("EVENT_NAME=push", "BASE_SHA="+before, "GITHUB_OUTPUT="+output)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("classifier: %v\n%s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("\n"+string(data), "\nhost="+tc.want+"\n") {
				t.Fatalf("want host=%s, got %s", tc.want, data)
			}
		})
	}
}

// Module metadata diffs bypass the no-Node lane in every direction: adding,
// updating, or removing a Go requirement cannot introduce a Node invocation
// into the replayed host commands, so dependabot bumps and module-only edits
// used to pay the whole lane for nothing.
func TestNodeIndependenceScope_ModuleMetadataBypass(t *testing.T) {
	t.Parallel()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readRepositoryFile(t, ".github", "workflows", nodeIndependenceWorkflow)), &workflow); err != nil {
		t.Fatal(err)
	}
	var classify ciStepSpec
	for _, step := range workflow.Jobs["changes"].Steps {
		if step.ID == "filter" {
			classify = step
		}
	}
	if classify.Run == "" {
		t.Fatal("node-independence workflow lost its filter step")
	}

	baseline := newQAGitFixture(t)
	baseline.write(t, "go.mod", "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n)\n")
	baseline.write(t, "go.sum", "github.com/a/one v1.0.0 h1:aaa=\ngithub.com/b/two v2.0.0 h1:bbb=\n")
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")

	for _, tc := range []struct {
		name, path, text string
	}{
		{"drop requirement", "go.mod", "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n)\n"},
		{"add requirement", "go.mod", "module example\n\ngo 1.24\n\nrequire (\n\tgithub.com/a/one v1.0.0\n\tgithub.com/b/two v2.0.0\n\tgithub.com/c/three v3.0.0\n)\n"},
		{"checksum edit", "go.sum", "github.com/a/one v1.0.0 h1:aaa=\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := baseline.cloneInto(t)
			before := strings.TrimSpace(fixture.git(t, "rev-parse", "HEAD"))
			fixture.write(t, tc.path, tc.text)
			fixture.git(t, "commit", "-qam", "change")
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.CommandContext(t.Context(), "bash", "-c", classify.Run)
			cmd.Dir = fixture.root
			cmd.Env = fixture.commandEnv("EVENT_NAME=push", "BASE_SHA="+before, "GITHUB_OUTPUT="+output)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("classifier: %v\n%s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("\n"+string(data), "\nhost=false\n") {
				t.Fatalf("want host=false, got %s", data)
			}
		})
	}
}

// The guard's nodeToolNames is the authoritative list of Node entry points it
// masks on the host, and the classifier must fire on every one of them: a name
// added there without the classifier would silently bypass the lane. `bun` is
// covered by its invocation-shape rule instead, because it also names the Go
// ORM; extra classifier names (the TypeScript toolchains) are allowed.
func TestNodeIndependenceScope_EntryPointNamesMatchGuard(t *testing.T) {
	t.Parallel()
	guard := readRepositoryFile(t, "tools", "backendplugin", "node_independence", "probe.go")
	block := regexp.MustCompile(`(?s)var nodeToolNames = \[\]string\{(.*?)\n\}`).FindStringSubmatch(guard)
	if block == nil {
		t.Fatal("probe.go lost its nodeToolNames block")
	}
	var entryPoints []string
	for _, name := range regexp.MustCompile(`"([a-z]+)"`).FindAllStringSubmatch(block[1], -1) {
		entryPoints = append(entryPoints, name[1])
	}
	if len(entryPoints) == 0 {
		t.Fatal("probe.go nodeToolNames lists no entry point")
	}
	workflow := readRepositoryFile(t, ".github", "workflows", nodeIndependenceWorkflow)
	tools := regexp.MustCompile(`(?m)^\s*node_tool='\(([^']*)\)'\s*$`).FindStringSubmatch(workflow)
	if tools == nil {
		t.Fatal("node-independence workflow lost its node_tool pattern")
	}
	fires := map[string]bool{}
	for name := range strings.SplitSeq(tools[1], "|") {
		fires[name] = true
	}
	bun := regexp.MustCompile(`(?m)^\s*bun_ref='([^']*)'\s*$`).FindStringSubmatch(workflow)
	if bun == nil || !strings.Contains(bun[1], "bunx") {
		t.Error("bun must keep its invocation-shape rule in the classifier")
	}
	for _, name := range entryPoints {
		if name == "bun" {
			continue
		}
		if !fires[name] {
			t.Errorf("guard entry point %q is not a classifier trigger; add it to node_tool", name)
		}
	}
}
