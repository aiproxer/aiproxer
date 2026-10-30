// Command node_independence proves that the AIProxer host build, verification,
// packaging and CLI surface runs with the Node toolchain genuinely unavailable.
//
// Spec cursor-sdk-standalone, task 5.1.
//
// The lane is not a keyword scan and it is not a PATH-only proof:
//
//   - It first observes Node availability. If the toolchain is reachable it
//     enters an unprivileged private mount namespace and bind-mounts a
//     non-executable file over every discovered entry point, so Node stops
//     existing for this process tree at *any* path, absolute paths included.
//     No host file is ever moved, so namespace teardown is the restore.
//   - It then runs negative controls that must fail (every masked absolute path,
//     and bare-name resolution) plus positive controls that must succeed, so a
//     lane that "passes" because it broke its own toolchain cannot report
//     success.
//   - It finally replays the documented host verification command set with a
//     tripwire PATH layered on top, recording each command's exit status.
//
// On a Node-free image the isolation step is a no-op and the negative controls
// still have to hold.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Cross-platform names for the privileged half of the isolation flow. The root
// helper is entered through sudo, so its identity inputs are authentic; the mask
// list it receives is not, and is re-validated inside the helper.
const (
	// rootHelperFlag runs the privileged half: validate, mask, drop, exec.
	rootHelperFlag = "-root-helper"
	// rootHelperProbeFlag resumes with probe arguments so the privilege path can
	// be regression-tested end to end.
	rootHelperProbeFlag = "-root-helper-probe"
	// rootHelperEnv carries the untrusted mask list into the root helper.
	rootHelperEnv = "AIP_NODE_MASK_TARGETS"
	// maskedEnv tells the resumed lane which entry points the helper masked.
	maskedEnv = "AIP_NODE_MASKED"
	// rootHelperProbeDirEnv names the fixture directory for the probe.
	rootHelperProbeDirEnv = "AIP_NODE_MASK_PROBE_DIR"
)

// aipstdPlaceholder is substituted with the built CLI path for the CLI steps.
const aipstdPlaceholder = "{aipstd}"

// stepTimeout bounds one verification command. The outer budget is a
// supervisor limit, never an assertion about the command's own results.
const stepTimeout = 90 * time.Minute

// tripwireTools shadows every probed Node-family entry point, including the
// version managers, because a version manager can materialise a runtime on
// first use. Deriving the list from nodeToolNames keeps the two from drifting.
var tripwireTools = nodeToolNames

// tripwireExit is the tripwire's exit status. 127 is the conventional
// "command not found", which is also what a genuinely absent toolchain returns,
// so the recorded log - not the status - is the finding.
const tripwireExit = 127

// stepSpec is one documented verification command.
type stepSpec struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// commandSets are the documented host verification surfaces. `full` is the
// design-mandated set and is what every pull request runs; `fast` exists only
// for a local smoke run and never substitutes for it.
var commandSets = map[string][]stepSpec{
	"fast": {
		{Name: "root-build", Argv: []string{"go", "build", "./..."}},
		{Name: "unit", Argv: []string{"make", "test-unit"}},
		{Name: "non-cursor-smoke", Argv: []string{"make", "parity-sentinel"}},
	},
	"full": {
		{Name: "root-build", Argv: []string{"go", "build", "./..."}},
		{Name: "unit", Argv: []string{"make", "test-unit"}},
		{Name: "quality", Argv: []string{"make", "quality-checks"}},
		{Name: "comprehensive", Argv: []string{"make", "test"}},
		{Name: "package-minimal", Argv: []string{"make", "package-minimal"}},
		{Name: "cli-build", Argv: []string{"go", "build", "-o", aipstdPlaceholder, "./cmd/aipstd"}},
		{Name: "cli-version", Argv: []string{aipstdPlaceholder, "--version"}},
		{Name: "cli-help", Argv: []string{aipstdPlaceholder, "--help"}},
		{Name: "non-cursor-smoke", Argv: []string{"make", "parity-sentinel"}},
	},
}

// stepResult records one command's exit status without keeping its output.
type stepResult struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Exit int      `json:"exit"`
	Note string   `json:"note,omitempty"`
}

// controlResult records one negative or positive control.
//
// Pass means the control met its expectation: for a negative control the command
// must fail to run, for a positive control it must succeed. LaunchFailed
// separates "the kernel refused to start it" from "it ran and returned nonzero",
// which is what distinguishes a genuinely unavailable Node entry point from an
// inert fixture.
type controlResult struct {
	Kind         string   `json:"kind"` // "negative" or "positive"
	Argv         []string `json:"argv"`
	Pass         bool     `json:"pass"`
	LaunchFailed bool     `json:"launch_failed"`
	Note         string   `json:"note,omitempty"`
}

// report is the auditable JSON the CI lane uploads as an artifact.
type report struct {
	Root            string          `json:"root"`
	Set             string          `json:"set"`
	Host            string          `json:"host"`
	ProbeBefore     probeResult     `json:"probe_before"`
	Masked          []string        `json:"masked"`
	ProbeAfter      probeResult     `json:"probe_after"`
	NegativeControl []controlResult `json:"negative_controls"`
	PositiveControl []controlResult `json:"positive_controls"`
	Steps           []stepResult    `json:"steps"`
	TripwireCalls   []string        `json:"tripwire_invocations"`
	OK              bool            `json:"ok"`
}

// options are the parsed command-line options.
type options struct {
	root       string
	set        string
	reportPath string
	isolate    string
	probeOnly  bool
}

func main() {
	// The privileged half and the probe are dispatched before argument parsing:
	// they carry the staged mask list path, which the lane's own flags never
	// describe, and re-entering through parseArgs would reject them.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case rootHelperFlag:
			code, err := rootHelperExit()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(code)
		case rootHelperProbeFlag:
			code, err := rootHelperProbeExit()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			os.Exit(code)
		}
	}
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	code, err := run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "node-independence: %v\n", err)
	}
	os.Exit(code)
}

func parseArgs(args []string) (options, error) {
	opts := options{set: "full", isolate: "auto"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--root":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--root requires a value")
			}
			i++
			opts.root = args[i]
		case "--set":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--set requires a value")
			}
			i++
			opts.set = args[i]
		case "--report":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--report requires a value")
			}
			i++
			opts.reportPath = args[i]
		case "--isolate":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--isolate requires a value")
			}
			i++
			opts.isolate = args[i]
		case "--probe-only":
			opts.probeOnly = true
		default:
			return opts, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	if opts.root == "" {
		return opts, fmt.Errorf("--root is required; the lane never guesses the repository root")
	}
	if _, ok := commandSets[opts.set]; !ok {
		return opts, fmt.Errorf("unknown --set %q; want one of %v", opts.set, slices.Sorted(maps.Keys(commandSets)))
	}
	switch opts.isolate {
	case "auto", "require", "off":
	default:
		return opts, fmt.Errorf("unknown --isolate %q; want auto, require, or off", opts.isolate)
	}
	return opts, nil
}

func run(opts options) (int, error) {
	root, err := filepath.Abs(opts.root)
	if err != nil {
		return 2, err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return 2, fmt.Errorf("%s is not an AIProxer root (no go.mod)", root)
	}
	workdir, err := os.MkdirTemp("", "aip-node-independence-")
	if err != nil {
		return 2, err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	// The verification surface must run unprivileged: the host refuses to start
	// as an administrative user, so an EUID-0 run would prove nothing. This is
	// asserted here rather than assumed, and it is why the isolation step needs
	// a real elevation to build a mount namespace.
	if err := requireNonRootEUID(os.Geteuid()); err != nil {
		return 2, err
	}
	callerUID := os.Geteuid()
	//nolint:gocritic // a copy is the point: this is the pre-isolation snapshot
	callerEnv := append([]string(nil), os.Environ()...)

	result := report{Root: root, Set: opts.set, Host: hostDescription()}

	pathEnv := os.Getenv("PATH")
	before, err := probeNodeToolchain(pathEnv, workdir, root)
	if err != nil {
		return 2, err
	}
	result.ProbeBefore = before
	fmt.Printf("node-independence: host=%s set=%s root=%s\n", result.Host, result.Set, root)
	fmt.Printf("node-independence: probe before isolation: %s\n", before)
	if opts.probeOnly {
		// Diagnostic mode: report what this host exposes and change nothing.
		encodeReport(opts, result)
		if before.Empty() {
			return 0, nil
		}
		return 1, fmt.Errorf("node toolchain reachable: %s", before)
	}

	if err := ensureIsolated(opts, workdir, before); err != nil {
		return 2, err
	}
	// sudo resets the environment, so the resumed lane gets the caller's variables
	// handed back explicitly. Verify that, because a stripped environment silently
	// repoints the Go caches and the run then measures the wrong build.
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "HOME"} {
		if before, after := envValue(callerEnv, key), envValue(os.Environ(), key); before != "" && before != after {
			return 2, fmt.Errorf("environment variable %s changed across isolation: %q -> %q", key, before, after)
		}
	}
	// Re-assert the identity the verification surface will run under. The
	// elevation exists only long enough to build the namespace; if anything
	// survived it, the run would be measuring the wrong thing.
	if euid := os.Geteuid(); euid != callerUID {
		return 1, fmt.Errorf("effective uid changed during isolation: expected %d, got %d", callerUID, euid)
	}
	// Only real paths belong here. The helper leaves maskedEnv unset when the
	// image exposed nothing to mask, and substituting a sentence for that would
	// put a non-path into both the report and the negative controls, which then
	// try to execute it.
	maskedPaths := maskedPathsFromEnv()
	result.Masked = maskedPaths
	if len(maskedPaths) == 0 {
		fmt.Printf("node-independence: isolated as uid %d; no Node entry point needed masking\n", callerUID)
	} else {
		fmt.Printf("node-independence: isolated as uid %d; masked %d Node entry point(s): %s\n",
			callerUID, len(maskedPaths), strings.Join(maskedPaths, ", "))
	}

	after, err := probeNodeToolchain(os.Getenv("PATH"), workdir, root)
	if err != nil {
		return 2, err
	}
	result.ProbeAfter = after
	fmt.Printf("node-independence: probe after isolation: %s\n", after)
	if !after.Empty() {
		return 1, fmt.Errorf("node toolchain still reachable after isolation: %s", after)
	}

	// The isolated environment, before any tripwire is layered on. Negative
	// controls must fail against real absence, not against our own shims.
	isolatedEnv := os.Environ()
	negative, err := negativeControls(root, isolatedEnv, result.Masked)
	if err != nil {
		return 2, err
	}
	result.NegativeControl = negative
	positive, err := positiveControls(root, isolatedEnv)
	if err != nil {
		return 2, err
	}
	result.PositiveControl = positive
	if err := reportControlOutcome(negative, positive); err != nil {
		return 1, err
	}

	tripwireDir := filepath.Join(workdir, "tripwire")
	if err := installTripwires(tripwireDir); err != nil {
		return 2, err
	}
	logPath := filepath.Join(workdir, "tripwire.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		return 2, err
	}
	replayEnv := append(os.Environ(),
		"PATH="+tripwireDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AIP_NODE_TRIPWIRE_LOG="+logPath,
	)
	aipstd := filepath.Join(workdir, "aipstd")

	for _, step := range commandSets[opts.set] {
		exit, note, err := runStep(root, replayEnv, aipstd, step)
		if err != nil {
			return 2, err
		}
		result.Steps = append(result.Steps, stepResult{Name: step.Name, Argv: step.Argv, Exit: exit, Note: note})
		if exit != 0 {
			// Stop at the first failing command: later commands would only
			// measure a broken tree.
			break
		}
	}

	result.TripwireCalls = readLines(logPath)
	// A Node toolchain that reappeared during the replay would invalidate the
	// whole run, so re-observe rather than trusting the earlier probe.
	final, err := probeNodeToolchain(os.Getenv("PATH"), workdir, root)
	if err != nil {
		return 2, err
	}
	result.ProbeAfter = final
	result.OK = final.Empty() && len(result.TripwireCalls) == 0 && allStepsPassed(result.Steps)
	encodeReport(opts, result)

	var problems []string
	if !final.Empty() {
		problems = append(problems, "Node toolchain reappeared during verification: "+final.String())
	}
	if len(result.TripwireCalls) > 0 {
		problems = append(problems, fmt.Sprintf("%d Node invocation(s) recorded by the PATH tripwires", len(result.TripwireCalls)))
	}
	for _, step := range result.Steps {
		if step.Exit != 0 {
			problems = append(problems, fmt.Sprintf("step %s exited %d", step.Name, step.Exit))
		}
	}
	if !slices.Equal(result.ProbeBefore.Paths(), result.ProbeAfter.Paths()) && len(result.ProbeBefore.Paths()) == 0 {
		problems = append(problems, "Node toolchain became reachable during verification")
	}
	if len(problems) > 0 {
		return 1, fmt.Errorf("no-Node verification failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Printf("node-independence: %d/%d steps passed, no Node entry point reachable or invoked\n",
		len(result.Steps), len(commandSets[opts.set]))
	fmt.Println("OK node-independence")
	return 0, nil
}

// ensureIsolated makes Node unreachable for this process tree.
//
// The identity flow is the load-bearing part: an unprivileged caller enters a
// mount namespace through a root helper, the helper masks the entry points and
// immediately drops back to the sudo caller, and only then is the lane resumed.
// There is deliberately no user namespace, because mapping the caller to root
// would leave the verification surface running with EUID 0.
func ensureIsolated(opts options, workdir string, before probeResult) error {
	if before.Empty() {
		// A Node-free image is the strongest starting state: nothing to mask, and
		// the negative controls below still have to hold.
		return nil
	}
	if opts.isolate == "off" {
		return fmt.Errorf("node toolchain reachable and isolation disabled: %s", before)
	}
	if !isolationSupported() {
		return fmt.Errorf("%s", isolationUnavailableReason())
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}
	// Replaces this process; on success it never returns.
	return isolate(self, os.Args[1:], before.Paths())
}

// rootHelperExit runs the privileged half of the flow and never returns on
// success. It is deliberately tiny: validate, mask, drop privileges, exec.
func rootHelperExit() (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 2, fmt.Errorf("resolve own executable: %w", err)
	}
	args := os.Args[2:]
	maskDir, err := os.MkdirTemp("", "aip-node-mask-")
	if err != nil {
		return 2, err
	}
	// The helper appends the masked-path record itself; declaring it here as well
	// would produce a duplicate variable whose empty value wins on lookup.
	if err := runRootHelper(self, args, maskDir); err != nil {
		return 2, err
	}
	return 0, nil
}

// rootHelperProbeExit resumes after the helper with the probe arguments. It exists
// so the privilege path can be regression-tested end to end: the test drives the
// real helper, and this mode asserts the same properties the lane asserts.
func rootHelperProbeExit() (int, error) {
	// The probe directory arrives as argv, not the environment: sudo resets the
	// environment, which would silently drop an env-carried path.
	dir := ""
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	if dir == "" {
		return 2, fmt.Errorf("the probe requires its fixture directory as an argument")
	}
	masked := maskedPathsFromEnv()
	fmt.Printf("masked=%d\n", len(masked))
	env := []string{"PATH=" + dir}
	problems := 0
	for _, target := range masked {
		control := runControl("negative", dir, env, target, "--version")
		if !control.Pass || !control.LaunchFailed {
			problems++
		}
	}
	if control := runControl("negative", dir, env, "node", "--version"); !control.Pass || !control.LaunchFailed {
		problems++
	}
	if control := runControl("positive", dir, env, filepath.Join(dir, "keep-me"), "--version"); !control.Pass {
		problems++
	}
	fmt.Printf("euid=%d\n", os.Geteuid())
	if problems != 0 {
		return 1, fmt.Errorf("%d isolation controls failed", problems)
	}
	fmt.Println("control-ok")
	unmask(masked)
	return 0, nil
}

// negativeControls assert that the Node toolchain cannot be started at all.
// maskedPathsFromEnv reads the helper's handoff. An unset variable means the
// image exposed no entry point to mask, which is an empty list rather than a
// placeholder: callers treat these values as executable paths.
func maskedPathsFromEnv() []string {
	masked := os.Getenv(maskedEnv)
	if masked == "" {
		return nil
	}
	return strings.Split(masked, ",")
}

func negativeControls(dir string, env []string, masked []string) ([]controlResult, error) {
	var results []controlResult
	for _, target := range masked {
		results = append(results, runControl("negative", dir, env, target, "--version"))
	}
	// Bare-name resolution must also fail, whatever the absolute paths did.
	for _, name := range nodeToolNames {
		results = append(results, runControl("negative", dir, env, name, "--version"))
	}
	return results, nil
}

// positiveControls assert that the isolation did not simply break the lane's
// own documented prerequisites. Without these a hostile or broken environment
// would satisfy every negative control.
func positiveControls(dir string, env []string) ([]controlResult, error) {
	return []controlResult{
		runControl("positive", dir, env, "go", "version"),
		runControl("positive", dir, env, "make", "--version"),
	}, nil
}

// runControl executes one control and records whether it met its expectation.
//
// Resolution deliberately goes through the control's own PATH rather than
// exec.Command's ambient lookup. Otherwise a control would silently run the
// caller's real `node` instead of the PATH it was asked to prove absent, which
// is precisely the failure the negative controls exist to catch.
func runControl(kind, dir string, env []string, argv ...string) controlResult {
	result := controlResult{Kind: kind, Argv: argv}
	cmd := exec.Command(resolveInPath(envValue(env, "PATH"), argv[0], dir), argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	err := cmd.Run()
	if err == nil {
		// The command ran to completion. That satisfies a positive control and
		// violates a negative one, because a negative control asserts that the
		// toolchain cannot be started at all.
		result.Pass = kind == "positive"
		if !result.Pass {
			result.Note = "entry point executed successfully; it is not unavailable"
		}
		return result
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		result.LaunchFailed = false
		result.Pass = kind == "negative"
		if !result.Pass {
			result.Note = fmt.Sprintf("documented prerequisite exited %d", exitErr.ExitCode())
		}
		return result
	}
	result.LaunchFailed = true
	result.Pass = kind == "negative"
	result.Note = err.Error()
	return result
}

// resolveInPath resolves name against pathEnv. An unresolvable bare name is
// turned into an absolute path that cannot exist, so the attempt fails at launch
// instead of falling back to the caller's own PATH.
func resolveInPath(pathEnv, name, dir string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		return name
	}
	if resolved, ok := lookExecutableIn(name, pathDirs(pathEnv)); ok {
		return resolved
	}
	return filepath.Join(dir, name)
}

// envValue returns the value of key in a KEY=VALUE environment list.
func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if after, found := strings.CutPrefix(entry, prefix); found {
			return after
		}
	}
	return ""
}

func reportControlOutcome(negative, positive []controlResult) error {
	var problems []string
	for _, control := range slices.Concat(negative, positive) {
		if control.Pass {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s control %v: %s", control.Kind, control.Argv, control.Note))
	}
	if len(problems) > 0 {
		return fmt.Errorf("control checks failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// installTripwires shadows every Node-family entry point with a shim that logs
// the invocation and fails. This is defence in depth: isolation already removes
// the toolchain, and the tripwire additionally catches a runtime that appears
// while the lane runs.
func installTripwires(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tripwire directory: %w", err)
	}
	for _, tool := range tripwireTools {
		body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\t%%s\\n' %q \"$*\" >> \"${AIP_NODE_TRIPWIRE_LOG:?}\"\necho \"node-independence: %s invoked: $*\" >&2\nexit %d\n",
			tool, tool, tripwireExit)
		shim := filepath.Join(dir, tool)
		if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
			return fmt.Errorf("write tripwire %s: %w", tool, err)
		}
	}
	return nil
}

// runStep executes one verification command with its own bounded supervisor.
func runStep(dir string, env []string, aipstd string, step stepSpec) (int, string, error) {
	argv := make([]string, 0, len(step.Argv))
	for _, arg := range step.Argv {
		argv = append(argv, strings.ReplaceAll(arg, aipstdPlaceholder, aipstd))
	}
	fmt.Printf("\n== %s: %s\n", step.Name, strings.Join(argv, " "))
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	// Resolve against the replayed PATH, not the caller's, so the tripwire layer
	// is what the steps actually see.
	cmd := exec.CommandContext(ctx, resolveInPath(envValue(env, "PATH"), argv[0], dir), argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	note := ""
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			fmt.Printf("== %s: exit %d\n", step.Name, exitErr.ExitCode())
			return exitErr.ExitCode(), note, nil
		case errors.Is(err, exec.ErrNotFound):
			note = "command not found; documented prerequisite missing"
			fmt.Printf("== %s: %s\n", step.Name, note)
			return tripwireExit, note, nil
		case ctx.Err() != nil:
			note = fmt.Sprintf("exceeded the %s supervisor budget", stepTimeout)
			fmt.Printf("== %s: %s\n", step.Name, note)
			return 1, note, nil
		default:
			return 0, note, fmt.Errorf("step %s: %w", step.Name, err)
		}
	}
	fmt.Printf("== %s: exit 0\n", step.Name)
	return 0, note, nil
}

func allStepsPassed(steps []stepResult) bool {
	for _, step := range steps {
		if step.Exit != 0 {
			return false
		}
	}
	return true
}

func encodeReport(opts options, result report) {
	if opts.reportPath == "" {
		return
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "node-independence: encode report: %v\n", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(opts.reportPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "node-independence: create report directory: %v\n", err)
		return
	}
	if err := os.WriteFile(opts.reportPath, append(encoded, '\n'), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "node-independence: write report: %v\n", err)
	}
}

func readLines(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var lines []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func hostDescription() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "unknown"
	}
	return fmt.Sprintf("%s/%s uid=%d", name, runtime.GOOS, os.Geteuid())
}
