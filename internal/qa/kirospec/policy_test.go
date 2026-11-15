package kirospec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureDelivery = "| Spec | at most 5 requirement areas and 25 acceptance criteria; `design.md` at most 300 lines; at most 12 leaf tasks |\n" +
	"| PR | at most ~1,500 lines of non-test Go and ~40 changed files |\n" +
	"| Tests in a feature PR | at most ~5x the production lines they cover |\n"

func writePolicyFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	base := map[string]string{
		"Makefile":                               ".PHONY: build\nbuild: deps\n\tgo build ./...\nVAR := x\n",
		".kiro/steering/delivery.md":             fixtureDelivery,
		".agents/skills/golang-testing/SKILL.md": "---\nname: golang-testing\n---\n",
		"pkg/foo/foo.go":                         "package foo\n\nfunc Bar() {}\n",
	}
	for _, rel := range policyDocuments {
		base[rel] = ""
	}
	for rel, content := range files {
		base[rel] = content
	}
	for rel, content := range base {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestValidatePolicyDocs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, doc, content, want string
	}{
		{"clean", ".kiro/steering/case.md", "# Clean\n\nRun `make build`, see `pkg/foo`, `pkg/foo/foo.go`, `pkg/foo.Bar()`, `golang-testing`,\n" +
			"[foo](../../pkg/foo/foo.go), [anchor](#clean), [web](https://example.com). Never copy into `.kiro/skills`.\n" +
			"Tests stay within ~5x; 12 is the enforced ceiling; at most 100 `*.go` files; ~40 files, ~1500 added non-test Go lines.\n" +
			"Unchecked: `origin/main`, `<container>/worktrees/`, `kiro-*`.\n```sh\nmake build\n```\n", ""},
		{"unknown first component path", ".kiro/steering/case.md", "Run `scirpts/policy.sh` first.\n", `path "scirpts/policy.sh" does not exist`},
		{"unknown first component prose", ".kiro/steering/case.md", "Read `some/deep/words` there.\n", ""},
		{"broken same-document anchor", ".kiro/steering/case.md", "# Clean\n\nSee [top](#missing).\n", `anchor "#missing" is not a heading in this document`},
		{"make target", "AGENTS.md", "Run `make missing-target PR=<n>`.\n", `make target "missing-target"`},
		{"fenced make target", "AGENTS.md", "```sh\nmake gone\n```\n", `make target "gone"`},
		{"path", ".kiro/steering/case.md", "See `pkg/bar/`.\n", `path "pkg/bar/" does not exist`},
		{"symbol", ".kiro/steering/case.md", "See `pkg/foo.Baz()`.\n", "pkg/foo does not declare Baz"},
		{"skill", "docs/agent-workflow.md", "Load `golang-gone`.\n", `skill "golang-gone"`},
		{"link", "docs/agent-workflow.md", "See [x](missing.md).\n", `link target "missing.md"`},
		{"footer", ".kiro/steering/case.md", "_Updated: 2026-01-01_\n", "history/changelog footer"},
		{"changelog heading", "AGENTS.md", "## Changelog\n", "history/changelog footer"},
		{"go version", ".kiro/steering/case.md", "The toolchain is Go 1.26.\n", "copied Go version"},
		{"component count", ".kiro/steering/case.md", "dbparity has 12 registered components.\n", "copied component count"},
		{"unrelated test ratio", "docs/development-iteration.md", "The test retries 2x before reporting failure.\n", ""},
		{"unrelated file count", "AGENTS.md", "Inspect ~50 changed files for local debugging.\n", ""},
		{"pr lines", ".kiro/steering/delivery.md", strings.Replace(fixtureDelivery, "1,500", "2,000", 1), `"PR" row states 2,000`},
		{"pr files", ".kiro/steering/delivery.md", strings.Replace(fixtureDelivery, "~40", "~50", 1), `"PR" row states 50`},
		{"task ceiling", ".kiro/steering/delivery.md", strings.Replace(fixtureDelivery, "12 leaf tasks", "13 leaf tasks", 1), `"Spec" row states 13`},
		{"go file gate", "AGENTS.md", "**Change-size gate.** At most **90 `*.go` files**.\n", "change-size gate 90"},
		{"delivery row", ".kiro/steering/delivery.md", strings.Replace(fixtureDelivery, "~5x", "~1.5x", 1), `"Tests in a feature PR" row states 1.5`},
		{"delivery row missing", ".kiro/steering/delivery.md", "| PR | budget |\n", `"PR" row not found`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := writePolicyFixture(t, map[string]string{tc.doc: tc.content})
			joined := strings.Join(ValidatePolicyDocs(root), "\n")
			if tc.want == "" {
				if joined != "" {
					t.Fatalf("unexpected findings:\n%s", joined)
				}
				return
			}
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("findings %q do not contain %q", joined, tc.want)
			}
		})
	}
}

func TestPolicyMarkdownAnchors(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		".kiro/steering/case.md": "# Case\n\n" +
			"See [plain](../theory.md#design-notes), [code](../theory.md#run-make-build),\n" +
			"[duplicate](../theory.md#design-notes-1), [explicit](../theory.md#manual),\n" +
			"[local](#case), and [line](../../pkg/foo/foo.go#L1).\n",
		".kiro/theory.md": "# Theory\n\n## Design notes\n\n## Design notes\n\n## Run `make build`\n\n" +
			"<a name=\"manual\"></a>\n\n```sh\n## Fenced heading\n```\n\n~~~\n## Another fenced heading\n~~~\n",
	})
	if findings := strings.Join(ValidatePolicyDocs(root), "\n"); findings != "" {
		t.Fatalf("valid anchors were rejected: %s", findings)
	}
}

func TestPolicyMarkdownAnchorsRejectUnknown(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		".kiro/steering/case.md": "See [theory](../theory.md#design-notes) and [fenced](../theory.md#fenced-heading).\n",
		".kiro/theory.md":        "# Theory\n\n## Design notes\n\n```sh\n## Fenced heading\n```\n",
	})
	findings := strings.Join(ValidatePolicyDocs(root), "\n")
	if !strings.Contains(findings, `anchor "#fenced-heading" is not a heading in "../theory.md"`) {
		t.Fatalf("fenced heading was accepted as an anchor: %s", findings)
	}
	if strings.Contains(findings, "#design-notes") {
		t.Fatalf("valid anchor was rejected: %s", findings)
	}
}

func TestPolicyAnchorToleratesExportOmittedTarget(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		"AGENTS.md": "See [requirements](.kiro/specs/hidden/requirements.md#goal).\n",
	})
	initExportFixture(t, root, ".kiro/specs/hidden/requirements.md export-ignore\n")
	if findings := strings.Join(ValidatePolicyDocs(root), "\n"); findings != "" {
		t.Fatalf("export-omitted anchor target was rejected: %s", findings)
	}
}

func TestSpecOnlyValidationDoesNotInspectUnstagedPolicy(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{"AGENTS.md": "Run `make missing-target`.\n"})
	writeSpec(t, filepath.Join(root, ".kiro", "specs"), "completed-active", `{"phase":"completed"}`, nil)
	if err := os.MkdirAll(filepath.Join(root, ".kiro", "specs", "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	specs := strings.Join(ValidateSpecs(root), "\n")
	if !strings.Contains(specs, "active/completed-active: completed") || strings.Contains(specs, "missing-target") {
		t.Fatalf("spec-only findings: %s", specs)
	}
	if findings := strings.Join(Validate(root), "\n"); !strings.Contains(findings, "missing-target") {
		t.Fatalf("full validation missed invalid policy: %s", findings)
	}
}

func TestPolicyPathChecksBothRoots(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		"scripts/other.sh":        "",
		"docs/scripts/example.sh": "",
		"docs/agent-workflow.md":  "See `scripts/example.sh`.\n",
	})
	if findings := strings.Join(ValidatePolicyDocs(root), "\n"); findings != "" {
		t.Fatalf("document-relative path was rejected: %s", findings)
	}
}

func TestPolicySymbolChecksBothRoots(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		"pkg/foo/foo.go":         "package foo\nfunc Different() {}\n",
		"docs/pkg/foo/foo.go":    "package foo\nfunc Bar() {}\n",
		"docs/agent-workflow.md": "See `pkg/foo.Bar()`.\n",
	})
	if findings := strings.Join(ValidatePolicyDocs(root), "\n"); findings != "" {
		t.Fatalf("document-relative symbol was rejected: %s", findings)
	}
}

func TestPolicySymbolMustBePackageLevel(t *testing.T) {
	t.Parallel()
	root := writePolicyFixture(t, map[string]string{
		".kiro/steering/case.md": "See `pkg/foo.Missing()`.\n",
		"pkg/foo/foo.go":         "package foo\nfunc f() {\n\tMissing := 1\n\t_ = Missing\n}\n",
	})
	if findings := strings.Join(ValidatePolicyDocs(root), "\n"); !strings.Contains(findings, "pkg/foo does not declare Missing") {
		t.Fatalf("local variable mistaken for package symbol: %s", findings)
	}
}

func TestPolicyPathOmissionOnlyAllowedForPublicExport(t *testing.T) {
	t.Parallel()
	for _, private := range []bool{false, true} {
		for _, ignored := range []bool{false, true} {
			name := fmtTreeName(private) + "/not-ignored"
			attrs := ""
			if ignored {
				name = fmtTreeName(private) + "/export-ignored"
				attrs = ".github/dependabot.yml export-ignore\n"
			}
			t.Run(name, func(t *testing.T) {
				root := writePolicyFixture(t, map[string]string{
					"AGENTS.md": "See `.github/dependabot.yml`.\n",
				})
				initExportFixture(t, root, attrs)
				if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
					t.Fatal(err)
				}
				if private {
					if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				tolerated := !private && ignored
				if findings := strings.Join(ValidatePolicyDocs(root), "\n"); strings.Contains(findings, `path ".github/dependabot.yml" does not exist`) == tolerated {
					t.Fatalf("policy findings %q, tolerated=%v", findings, tolerated)
				}
			})
		}
	}
}

func TestGitHubSlug(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Design notes":                  "design-notes",
		"Run `make build`":              "run-make-build",
		"1. Public contracts — `pkg/`":  "1-public-contracts--pkg",
		"snake_case stays":              "snake_case-stays",
		"**Bold** and *thin* emphasis":  "bold-and-thin-emphasis",
		"[Linked heading](../other.md)": "linked-heading",
	}
	for heading, want := range cases {
		if got := githubSlug(headingText(heading)); got != want {
			t.Errorf("slug(%q) = %q, want %q", heading, got, want)
		}
	}
}
