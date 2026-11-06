package kirospec

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestValidateBudgets_MissingGrandfatheredSpecsOnlyAllowedInPublicTree(t *testing.T) {
	t.Parallel()
	for _, private := range []bool{false, true} {
		t.Run(fmtTreeName(private), func(t *testing.T) {
			root := t.TempDir()
			initExportFixture(t, root, ".kiro/specs/billing-financial-safety-contracts/** export-ignore\n.kiro/specs/high-concurrency-performance-hardening/** export-ignore\n.kiro/specs/openai-chatgpt-plan-go-connector-migration/** export-ignore\n")
			specs := filepath.Join(root, ".kiro", "specs")
			if err := os.MkdirAll(specs, 0o755); err != nil {
				t.Fatal(err)
			}
			if private {
				if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			findings := strings.Join(ValidateBudgets(specs), "\n")
			if private != strings.Contains(findings, "no longer active") {
				t.Fatalf("private=%v missing-spec findings: %s", private, findings)
			}
			writeSpec(t, specs, "new-spec", `{}`, map[string]string{
				"requirements.md": strings.Repeat("## Requirement\n", 6) + "## Deferred\n",
			})
			if findings := strings.Join(ValidateBudgets(specs), "\n"); !strings.Contains(findings, "exceeds budget") {
				t.Fatalf("private=%v accepted oversized exported spec: %s", private, findings)
			}
			writeSpec(t, specs, "billing-financial-safety-contracts", `{}`, map[string]string{
				"design.md": strings.Repeat("line\n", 688),
			})
			if findings := strings.Join(ValidateBudgets(specs), "\n"); !strings.Contains(findings, "688 design.md lines exceeds budget 687") {
				t.Fatalf("private=%v accepted growth in a present grandfathered spec: %s", private, findings)
			}
		})
	}
}

func fmtTreeName(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

// R5: only policy-excluded absences are tolerated, never the surviving checks.
func TestPartialSpecs_SkippedOnlyInPublicTree(t *testing.T) {
	t.Parallel()
	for _, private := range []bool{false, true} {
		for _, missing := range []string{"spec.json", "tasks.md", "requirements.md"} {
			for _, ignored := range []bool{false, true} {
				label := "not-ignored"
				if ignored {
					label = "export-ignored"
				}
				t.Run(fmtTreeName(private)+"/"+missing+"/"+label, func(t *testing.T) {
					root := t.TempDir()
					attrs := ".kiro/specs/unrelated/** export-ignore\n"
					if ignored {
						attrs += ".kiro/specs/partial/" + missing + " export-ignore\n.kiro/specs/archive/partial-archive/" + missing + " export-ignore\n"
					}
					initExportFixture(t, root, attrs)
					specs := filepath.Join(root, ".kiro", "specs")
					if private {
						if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					for _, name := range []string{"partial", "archive/partial-archive"} {
						metadata := `{"phase":"tasks-generated"}`
						if strings.HasPrefix(name, "archive/") {
							metadata = `{"phase":"completed","completed":true}`
						}
						writeSpec(t, specs, name, metadata, map[string]string{
							"requirements.md": "## Deferred\n",
							"tasks.md":        "",
							"design.md":       "design\n",
						})
						if err := os.Remove(filepath.Join(specs, filepath.FromSlash(name), missing)); err != nil {
							t.Fatal(err)
						}
					}
					lifecycleErrs, lifecycleOmissions := ValidateLifecycleWithOmissions(specs)
					lifecycle := strings.Join(lifecycleErrs, "\n")
					tolerated := !private && ignored
					for _, path := range lifecycleOmissions {
						t.Logf("export-ignored omission: %s", path)
					}
					if (len(lifecycleOmissions) == 2) != tolerated {
						t.Fatalf("omissions=%v, tolerated=%v", lifecycleOmissions, tolerated)
					}
					for _, name := range []string{"active/partial", "archive/partial-archive"} {
						// Filesystem findings use the exact path, not the lifecycle label.
						path := strings.TrimPrefix(name, "active/")
						if strings.Contains(lifecycle, filepath.FromSlash(path)+string(filepath.Separator)) == tolerated {
							t.Fatalf("private=%v lifecycle findings: %s", private, lifecycle)
						}
					}
					budgetErrs, budgetOmissions := ValidateBudgetsWithOmissions(specs)
					for _, path := range budgetOmissions {
						t.Logf("export-ignored omission: %s", path)
					}
					budget := strings.Join(budgetErrs, "\n")
					if strings.Contains(budget, filepath.Join("partial", missing)) == tolerated {
						t.Fatalf("private=%v budget findings: %s", private, budget)
					}
					if err := os.WriteFile(filepath.Join(specs, "partial", "design.md"), []byte(strings.Repeat("line\n", 301)), 0o600); err != nil {
						t.Fatal(err)
					}
					if got := strings.Join(ValidateBudgets(specs), "\n"); !strings.Contains(got, "301 design.md lines exceeds budget 300") {
						t.Fatalf("surviving design escaped validation: %s", got)
					}
					if missing == "spec.json" {
						if err := os.WriteFile(filepath.Join(specs, "partial", "tasks.md"), []byte("- [x] done\n"), 0o600); err != nil {
							t.Fatal(err)
						}
						if got := strings.Join(ValidateLifecycle(specs), "\n"); !strings.Contains(got, "all 1 tasks are checked") {
							t.Fatalf("surviving tasks escaped validation: %s", got)
						}
					}
				})
			}
		}
	}
}

func initExportFixture(t *testing.T, root, attrs string) {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "init", "-q")
	cmd.Env = gitscope.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(attrs), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExportOmissions_Repository(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	specs := filepath.Join(root, ".kiro", "specs")
	for _, validate := range []func(string) ([]string, []string){ValidateLifecycleWithOmissions, ValidateBudgetsWithOmissions} {
		errs, omissions := validate(specs)
		for _, path := range omissions {
			t.Logf("export-ignored omission: %s", path)
		}
		if len(errs) != 0 {
			t.Fatalf("validation findings: %v", errs)
		}
	}

	// An archived spec has no active directory, so only the active-path rule
	// in .gitattributes still marks it as Enterprise billing prose. Moving the
	// directory under archive/ must not publish the files that rule excludes.
	attributes, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	active := regexp.MustCompile(`(?m)^\.kiro/specs/([^/\s]+)/\*\* export-ignore$`)
	for _, match := range active.FindAllStringSubmatch(string(attributes), -1) {
		relative := filepath.Join(".kiro", "specs", "archive", match[1])
		if _, err := os.Stat(filepath.Join(root, relative)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if !exportIgnored(t, root, relative) {
			t.Errorf("%s lost the export-ignore rule of active spec %s", relative, match[1])
		}
	}
}

// exportIgnored reports whether the repository excludes a path from the export.
func exportIgnored(t *testing.T, root, relative string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "check-attr", "-z", "export-ignore", "--", filepath.ToSlash(relative))
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git check-attr %s: %v", relative, err)
	}
	fields := strings.Split(string(out), "\x00")
	return len(fields) == 4 && fields[2] == "set"
}
