package kirospec

import (
	"fmt"
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
			ceiling := grandfathered["high-concurrency-performance-hardening"]
			size := ceiling.Design + 1
			writeSpec(t, specs, "high-concurrency-performance-hardening", `{}`, map[string]string{
				"design.md": strings.Repeat("line\n", size),
			})
			want := fmt.Sprintf("%d design.md lines exceeds budget %d", size, ceiling.Design)
			if findings := strings.Join(ValidateBudgets(specs), "\n"); !strings.Contains(findings, want) {
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

// exportGitEnvPins are Git environment variables that bind a subprocess to a
// repository, index, or object store. Fixture commands must not inherit them:
// a stray GIT_DIR or GIT_INDEX_FILE would make the fixture write to the
// caller's repository instead.
var exportGitEnvPins = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_COMMON_DIR":                   true,
	"GIT_DIR":                          true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_WORK_TREE":                    true,
}

// exportFixtureEnv isolates fixture Git commands from caller configuration:
// repository bindings, GIT_CONFIG* overrides, caller identity, system
// attributes, and user templates are removed or replaced. A fixture commit is
// then a function of the files the test writes, not of the machine.
func exportFixtureEnv(t *testing.T) []string {
	t.Helper()
	absentConfig := filepath.Join(t.TempDir(), "absent-gitconfig")
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if exportGitEnvPins[name] ||
			strings.HasPrefix(name, "GIT_CONFIG") ||
			strings.HasPrefix(name, "GIT_AUTHOR_") ||
			strings.HasPrefix(name, "GIT_COMMITTER_") ||
			name == "GIT_ATTR_NOSYSTEM" ||
			name == "GIT_TEMPLATE_DIR" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+absentConfig,
		"GIT_ATTR_NOSYSTEM=1",
	)
}

func exportFixtureGit(t *testing.T, root string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initExportFixture(t *testing.T, root, attrs string) {
	t.Helper()
	env := exportFixtureEnv(t)
	hooks := t.TempDir()
	templates := t.TempDir()
	exportFixtureGit(t, root, env, "init", "-q", "--template="+templates)
	for _, args := range [][]string{
		{"config", "user.name", "Export Fixture"},
		{"config", "user.email", "export-fixture@example.invalid"},
		{"config", "commit.gpgsign", "false"},
		{"config", "core.hooksPath", filepath.ToSlash(hooks)},
		{"config", "core.autocrlf", "false"},
		{"config", "core.safecrlf", "false"},
		{"config", "core.attributesFile", filepath.ToSlash(filepath.Join(root, ".git", "fixture-absent-attributes"))},
		{"config", "maintenance.auto", "false"},
		{"config", "gc.auto", "0"},
		{"config", "gc.autoDetach", "false"},
	} {
		exportFixtureGit(t, root, env, args...)
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

	if err := localAttributesError(root); err != nil {
		t.Fatal(err)
	}

	// An archived spec has no active directory, so only the active-path rule
	// in .gitattributes still marks it as Enterprise billing prose. Moving the
	// directory under archive/ must not publish the files that rule excludes.
	// The rules come from HEAD and the lookups use the exporter's pinned
	// attributes, so a CRLF checkout, an uncommitted edit, or an ambient
	// attributes file cannot stand in for a missing committed rule.
	findings, err := archivedExportRuleFindings(root, filepath.Join(t.TempDir(), "no-global-attributes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
}

// archivedExportRuleFindings reports archived specs that HEAD would publish
// even though their active paths are export-ignored.
func archivedExportRuleFindings(root, absentAttributes string) ([]string, error) {
	attributes, err := gitBlob(root, ".gitattributes")
	if err != nil {
		return nil, err
	}
	active := regexp.MustCompile(`(?m)^\.kiro/specs/([^/\s]+)/\*\* export-ignore[ \t]*\r?$`)
	matches := active.FindAllStringSubmatch(string(attributes), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("HEAD:.gitattributes has no active .kiro/specs/*/** export-ignore rule; the archived-rule check would be vacuous")
	}
	var findings []string
	for _, match := range matches {
		relative := filepath.Join(".kiro", "specs", "archive", match[1])
		exists, err := commitPathExists(root, relative)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		ignored, err := exportIgnored(root, absentAttributes, relative)
		if err != nil {
			return nil, err
		}
		if !ignored {
			findings = append(findings, filepath.ToSlash(relative)+" lost the export-ignore rule of active spec "+match[1])
		}
	}
	return findings, nil
}

// gitBlob returns the bytes of a file in HEAD.
func gitBlob(root, name string) ([]byte, error) {
	cmd := exec.Command("git", "-C", root, "cat-file", "blob", "HEAD:"+name)
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob HEAD:%s: %w", name, err)
	}
	return out, nil
}

// commitPathExists reports whether HEAD tracks a directory at relative.
func commitPathExists(root, relative string) (bool, error) {
	cmd := exec.Command("git", "-C", root, "ls-tree", "-d", "--name-only", "HEAD", "--", filepath.ToSlash(relative))
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git ls-tree HEAD -- %s: %w", relative, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// exportIgnored reports whether the commit excludes a path from the export.
// It mirrors the pinned configuration of tools/ossexport: the archive is built
// from HEAD, the system and global attributes files are disabled, and CRLF
// conversion is off, so the answer is a function of the commit alone.
func exportIgnored(root, absentAttributes, relative string) (bool, error) {
	cmd := exec.Command("git", "-C", root,
		"-c", "core.autocrlf=false",
		"-c", "core.attributesFile="+absentAttributes,
		"check-attr", "-z", "--source=HEAD", "export-ignore", "--", filepath.ToSlash(relative))
	cmd.Env = attributesEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git check-attr %s: %w", relative, err)
	}
	fields := strings.Split(string(out), "\x00")
	return len(fields) == 4 && fields[2] == "set", nil
}

// attributesEnvironment disables the system attributes file for the same
// reason the exporter does: it lives outside the repository and would change
// the result of one commit.
func attributesEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range gitscope.Environ() {
		if strings.HasPrefix(entry, "GIT_ATTR_NOSYSTEM=") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_ATTR_NOSYSTEM=1")
}

// localAttributesError reports the local attributes file the exporter refuses:
// check-attr would otherwise honour rules outside the commit.
func localAttributesError(root string) error {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--git-common-dir")
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	directory := strings.TrimSpace(string(out))
	if directory == "" {
		return fmt.Errorf("the Git directory is unknown")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	path := filepath.Join(directory, "info", "attributes")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() > 0 {
		return fmt.Errorf("the local attributes file %s adds rules to the exported tree; the export refuses it", path)
	}
	return nil
}

// TestExportOmissions_MissingCommittedArchiveRuleFailsDespiteAmbientAttributesAndCRLF
// proves the repository check reads the committed rule. The ambient attributes
// file would otherwise satisfy git check-attr, and the CRLF attribute lines
// would otherwise hide the active rule from the scan.
func TestExportOmissions_MissingCommittedArchiveRuleFailsDespiteAmbientAttributesAndCRLF(t *testing.T) {
	cases := []struct {
		name                     string
		attributes               string
		ambient                  bool
		hostileGlobal            bool
		worktreeArchiveRule      bool
		worktreeRemoveActiveRule bool
		want                     int
	}{
		{
			name:       "committed archive rule",
			attributes: ".kiro/specs/commercial/** export-ignore\r\n.kiro/specs/archive/commercial export-ignore\r\n",
		},
		{
			name:       "missing committed archive rule with ambient attributes",
			attributes: ".kiro/specs/commercial/** export-ignore\r\n",
			ambient:    true,
			want:       1,
		},
		{
			name:          "missing committed archive rule under hostile global configuration",
			attributes:    ".kiro/specs/commercial/** export-ignore\r\n",
			hostileGlobal: true,
			want:          1,
		},
		{
			name:                "missing committed archive rule with worktree edit",
			attributes:          ".kiro/specs/commercial/** export-ignore\r\n",
			worktreeArchiveRule: true,
			want:                1,
		},
		{
			name:                     "missing committed archive rule with worktree active rule removed",
			attributes:               ".kiro/specs/commercial/** export-ignore\r\n",
			worktreeRemoveActiveRule: true,
			want:                     1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hostileMarker string
			if tc.hostileGlobal {
				ambient := filepath.Join(t.TempDir(), "attributes")
				if err := os.WriteFile(ambient, []byte(".kiro/specs/archive/commercial export-ignore\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				var globalConfig string
				globalConfig, hostileMarker = hostileGlobalGitConfig(t, ambient)
				t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
			}
			root := t.TempDir()
			initExportFixture(t, root, "")
			specs := filepath.Join(root, ".kiro", "specs")
			writeSpec(t, specs, "commercial", `{"phase":"tasks-generated"}`, map[string]string{"tasks.md": "- [ ] work\n"})
			writeSpec(t, specs, "archive/commercial", `{"phase":"completed","completed":true}`, map[string]string{"tasks.md": "- [x] done\n"})
			if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(tc.attributes), 0o600); err != nil {
				t.Fatal(err)
			}
			commitFixture(t, root)
			if hostileMarker != "" {
				if _, err := os.Stat(hostileMarker); !os.IsNotExist(err) {
					t.Fatalf("hostile global hook ran during fixture commit (stat error %v)", err)
				}
				if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
					t.Fatalf("hostile template hook was installed in the fixture (stat error %v)", err)
				}
			}
			// The fixture pinned its own attributes file for an isolated
			// setup. Drop that pin before the check so an ambient attributes
			// file would be visible if exportIgnored did not override it.
			exportFixtureGit(t, root, exportFixtureEnv(t), "config", "--unset", "core.attributesFile")
			switch {
			case tc.worktreeArchiveRule:
				attributes := tc.attributes + ".kiro/specs/archive/commercial export-ignore\r\n"
				if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(attributes), 0o600); err != nil {
					t.Fatal(err)
				}
			case tc.worktreeRemoveActiveRule:
				if err := os.WriteFile(filepath.Join(root, ".gitattributes"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ambient {
				ambient := filepath.Join(t.TempDir(), "attributes")
				if err := os.WriteFile(ambient, []byte(".kiro/specs/archive/commercial export-ignore\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				globalConfig := filepath.Join(t.TempDir(), "gitconfig")
				cmd := exec.Command("git", "config", "--file", globalConfig, "core.attributesFile", ambient)
				cmd.Env = gitscope.Environ()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("write ambient Git config: %v\n%s", err, out)
				}
				t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
			}
			findings, err := archivedExportRuleFindings(root, filepath.Join(t.TempDir(), "no-global-attributes"))
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != tc.want {
				t.Fatalf("findings = %v, want %d finding(s)", findings, tc.want)
			}
		})
	}

	t.Run("local attributes file is refused", func(t *testing.T) {
		root := t.TempDir()
		initExportFixture(t, root, "")
		specs := filepath.Join(root, ".kiro", "specs")
		writeSpec(t, specs, "commercial", `{"phase":"tasks-generated"}`, map[string]string{"tasks.md": "- [ ] work\n"})
		writeSpec(t, specs, "archive/commercial", `{"phase":"completed","completed":true}`, map[string]string{"tasks.md": "- [x] done\n"})
		if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(".kiro/specs/commercial/** export-ignore\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		commitFixture(t, root)
		infoDir := filepath.Join(root, ".git", "info")
		if err := os.MkdirAll(infoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(infoDir, "attributes"), []byte(".kiro/specs/archive/commercial export-ignore\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := localAttributesError(root)
		if err == nil || !strings.Contains(err.Error(), "local attributes file") {
			t.Fatalf("localAttributesError = %v, want the local attributes file refusal", err)
		}
	})
}

// hostileGlobalGitConfig writes a caller configuration that would break an
// unpinned fixture: signing is on, a failing hook is configured directly and
// through the template, CRLF conversion is aggressive, and background
// maintenance is enabled. It also supplies the archive rule the helper must not
// accept from the ambient configuration.
func hostileGlobalGitConfig(t *testing.T, ambientAttributes string) (config, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "hostile-hook-ran")
	hook := "#!/bin/sh\ntouch \"" + filepath.ToSlash(marker) + "\"\nexit 1\n"
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o700); err != nil { //nolint:gosec // the fixture hook must be executable.
		t.Fatal(err)
	}
	templates := t.TempDir()
	templateHooks := filepath.Join(templates, "hooks")
	if err := os.MkdirAll(templateHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateHooks, "pre-commit"), []byte(hook), 0o700); err != nil { //nolint:gosec // the fixture hook must be executable.
		t.Fatal(err)
	}
	config = filepath.Join(t.TempDir(), "hostile-gitconfig")
	content := strings.Join([]string{
		"[commit]",
		"\tgpgsign = true",
		"[core]",
		"\thooksPath = " + filepath.ToSlash(hooks),
		"\tautocrlf = true",
		"\tsafecrlf = true",
		"\tattributesFile = " + filepath.ToSlash(ambientAttributes),
		"[init]",
		"\ttemplateDir = " + filepath.ToSlash(templates),
		"[maintenance]",
		"\tauto = true",
		"[gc]",
		"\tauto = 1",
		"",
	}, "\n")
	if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, marker
}

func commitFixture(t *testing.T, root string) {
	t.Helper()
	env := exportFixtureEnv(t)
	exportFixtureGit(t, root, env, "add", "-A")
	exportFixtureGit(t, root, env,
		"-c", "user.name=Export Fixture",
		"-c", "user.email=export-fixture@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "-qm", "fixture")
}
