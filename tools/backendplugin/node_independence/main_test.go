package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeExecutable plants a fake Node entry point so the probe can be tested
// against a known answer instead of against whatever the host happens to have.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestProbeDetectsEntryPointsByNameAndInsideTheRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	repoRoot := filepath.Join(root, "repo")
	writeExecutable(t, filepath.Join(binDir, "node"))
	writeExecutable(t, filepath.Join(binDir, "npm"))
	// A vendored runtime inside the repository is the realistic absolute-path
	// case: the repository is walked explicitly for it.
	writeExecutable(t, filepath.Join(repoRoot, "third_party", "node"))

	result, err := probeNodeToolchain(binDir, filepath.Join(root, "skip"), repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Empty() {
		t.Fatal("probe reported an empty toolchain although node/npm on PATH and an in-repo node were planted")
	}
	paths := result.Paths()
	for _, want := range []string{filepath.Join(binDir, "node"), filepath.Join(binDir, "npm"), filepath.Join(repoRoot, "third_party", "node")} {
		if !slices.Contains(paths, want) {
			t.Errorf("probe missed %s; got %v", want, paths)
		}
	}
	if !slices.IsSorted(paths) {
		t.Errorf("probe paths must be sorted for stable evidence: %v", paths)
	}
	if result.String() != strings.Join(paths, ", ") {
		t.Errorf("String() = %q, want %q", result.String(), strings.Join(paths, ", "))
	}
	if len(result.Scope.Incomplete) != 0 {
		t.Errorf("a small fixture must not report an incomplete sweep: %v", result.Scope.Incomplete)
	}
}

// TestProbeIgnoresNonExecutableLookalikes is the regression for matching on name
// alone: a shell completion, a lint override or a vendored JavaScript file must
// not be reported, let alone masked, as a Node entry point.
func TestProbeIgnoresNonExecutableLookalikes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		// Only the lane's own platform has an execute bit to discriminate on; the
		// portable fallback treats existence as startable, which is why this
		// discrimination is asserted where it exists.
		t.Skip("executability discrimination is Linux-specific")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	for _, name := range []string{"npm", "nodejs"} {
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("# not executable\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := probeNodeToolchain(binDir, filepath.Join(root, "skip"), "")
	if err != nil {
		t.Fatal(err)
	}
	// The host may legitimately carry its own Node; only the planted
	// non-executables are under test here.
	for _, name := range []string{"npm", "nodejs"} {
		if planted := filepath.Join(binDir, name); slices.Contains(result.Paths(), planted) {
			t.Errorf("non-executable lookalike %s reported as an entry point", planted)
		}
	}
}

func TestProbeIgnoresTheLanesOwnTripwireDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	tripwireDir := filepath.Join(root, "tripwire")
	writeExecutable(t, filepath.Join(binDir, "go"))
	if err := installTripwires(tripwireDir); err != nil {
		t.Fatal(err)
	}

	result, err := probeNodeToolchain(binDir, tripwireDir, "")
	if err != nil {
		t.Fatal(err)
	}
	// The lane must not count its own tripwires as a reachable toolchain; that
	// would make every run report itself as a violation.
	for _, path := range result.Paths() {
		if strings.HasPrefix(path, tripwireDir+string(filepath.Separator)) {
			t.Errorf("probe reported the lane's own tripwire %s", path)
		}
	}
}

func TestProbeIgnoresDirectoriesNamedLikeNodeTools(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A version-manager toolcache directory shares the name with a command but
	// is not an entry point; only the file beneath it counts.
	if err := os.MkdirAll(filepath.Join(root, "node", "20.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := probeNodeToolchain(root, filepath.Join(root, "skip"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range result.Paths() {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			t.Errorf("a directory named like a Node tool must not be reported: %s", path)
		}
	}
}

func TestNegativeControlFailsAWorkingCommandAndPassesAnAbsentOne(t *testing.T) {
	t.Parallel()
	// A real, working executable stands in for a reachable Node toolchain: the
	// control logic must reject it, which is exactly what it must also do for a
	// reachable node. Using the Go toolchain keeps the fixture honest on every
	// platform instead of relying on shell scripts that are not runnable on all
	// of them.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain unavailable")
	}
	if control := runControl("negative", t.TempDir(), os.Environ(), goBin, "version"); control.Pass {
		t.Error("negative control passed against a working executable")
	}
	// An absent command must satisfy the negative control.
	if control := runControl("negative", t.TempDir(), os.Environ(), filepath.Join(t.TempDir(), "not-a-tool"), "--version"); !control.Pass {
		t.Errorf("negative control failed against a missing binary: %s", control.Note)
	}
	// A positive control that fails must not be reported as satisfied.
	if control := runControl("positive", t.TempDir(), os.Environ(), filepath.Join(t.TempDir(), "not-a-tool"), "--version"); control.Pass {
		t.Error("positive control passed for a command that cannot run")
	}
}

func TestReportControlOutcomeRejectsAnyUnsatisfiedControl(t *testing.T) {
	t.Parallel()
	good := controlResult{Kind: "negative", Pass: true}
	if err := reportControlOutcome([]controlResult{good}, []controlResult{{Kind: "positive", Pass: true}}); err != nil {
		t.Fatalf("satisfied controls must not fail the lane: %v", err)
	}
	broken := controlResult{Kind: "negative", Argv: []string{"node", "--version"}, Note: "Node entry point executed successfully; it is not unavailable"}
	err := reportControlOutcome([]controlResult{good, broken}, nil)
	if err == nil {
		t.Fatal("an executed Node entry point must fail the lane")
	}
	if !strings.Contains(err.Error(), "node") {
		t.Errorf("failure must name the offending control, got %q", err)
	}
}

func TestInstallTripwiresCoversEveryProbeName(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "tripwire")
	if err := installTripwires(dir); err != nil {
		t.Fatal(err)
	}
	for _, tool := range nodeToolNames {
		if _, err := os.Stat(filepath.Join(dir, tool)); err != nil {
			t.Errorf("tripwire missing for probed tool %q: %v", tool, err)
		}
	}
}

func TestParseArgsRequiresAnExplicitRoot(t *testing.T) {
	t.Parallel()
	if _, err := parseArgs([]string{"--set", "fast"}); err == nil {
		t.Error("--root must be required; the lane must never guess the repository root")
	}
	if _, err := parseArgs([]string{"--root", ".", "--set", "nope"}); err == nil {
		t.Error("an unknown --set must be rejected")
	}
	if _, err := parseArgs([]string{"--root", ".", "--isolate", "nope"}); err == nil {
		t.Error("an unknown --isolate must be rejected")
	}
	opts, err := parseArgs([]string{"--root", ".", "--set", "fast", "--isolate", "require", "--report", "r.json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.set != "fast" || opts.isolate != "require" || opts.reportPath != "r.json" || opts.root != "." {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestFullCommandSetCoversTheDocumentedSurface(t *testing.T) {
	t.Parallel()
	fastNames := stepNames(commandSets["fast"])
	for _, name := range fastNames {
		if !slices.Contains(stepNames(commandSets["full"]), name) {
			t.Errorf("fast set step %q is not part of the full set", name)
		}
	}
	full := stepNames(commandSets["full"])
	want := []string{
		"root-build", "unit", "quality", "comprehensive", "package-minimal",
		"cli-version", "cli-help", "non-cursor-smoke",
	}
	for _, name := range want {
		if !slices.Contains(full, name) {
			t.Errorf("full set missing documented step %q: %v", name, full)
		}
	}
	// The CLI steps must run the built binary, not a shell or a launcher name.
	for _, step := range commandSets["full"] {
		if step.Name != "cli-version" && step.Name != "cli-help" {
			continue
		}
		if step.Argv[0] != aipstdPlaceholder || len(step.Argv) != 2 {
			t.Errorf("%s must invoke the built binary directly, got %v", step.Name, step.Argv)
		}
	}
}

func TestPathDirsDeduplicatesAndDropsEmptyEntries(t *testing.T) {
	t.Parallel()
	joined := strings.Join([]string{"/usr/bin", "", "/usr/bin", "/bin"}, string(os.PathListSeparator))
	if got := pathDirs(joined); !slices.Equal(got, []string{"/usr/bin", "/bin"}) {
		t.Errorf("pathDirs = %v", got)
	}
}

// TestMaskedPathsFromEnvNeverInventsAPath pins the one value the lane treats as
// a list of executable paths. An unset handoff means the image exposed nothing
// to mask, and a placeholder substituted for it used to reach both the JSON
// report and the negative controls, which then tried to launch the sentence.
func TestMaskedPathsFromEnvNeverInventsAPath(t *testing.T) {
	t.Setenv(maskedEnv, "")
	if got := maskedPathsFromEnv(); len(got) != 0 {
		t.Errorf("unset %s = %v, want no entries", maskedEnv, got)
	}
	t.Setenv(maskedEnv, "/opt/node/bin/node,/usr/bin/node")
	if got := maskedPathsFromEnv(); !slices.Equal(got, []string{"/opt/node/bin/node", "/usr/bin/node"}) {
		t.Errorf("maskedPathsFromEnv = %v", got)
	}
}

func stepNames(steps []stepSpec) []string {
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.Name)
	}
	return names
}
