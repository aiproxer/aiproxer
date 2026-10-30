// Command devcheck provides explicit, measured development checks. It delegates
// correctness and caching to the toolchain; it never memoizes successful checks.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
	"github.com/aiproxer/aiproxer/tools/devcheck/internal/testscope"
	"github.com/aiproxer/aiproxer/tools/internal/scopeplan"
)

type testStats struct {
	Passed  int `json:"passed"`
	Cached  int `json:"cached"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run returns the check verdict. Its named result is read by the deferred
// manifest writer, so every return after the recorder exists lands in the
// verification manifest, including the failure paths.
func run(ctx context.Context) (err error) {
	task := flag.String("task", "doctor", "test, build, lint, contracts, delivery, doctor, product-smoke, or quarantine")
	module := flag.String("module", ".", "repository-relative Go module directory")
	packages := flag.String("packages", "", "explicit space-separated package patterns, e.g. ./pkg/aipapi")
	var skipTest explicitTestSkipFlag
	flag.Var(&skipTest, "skip-test", "skip one exact top-level Test in an explicit task=test run")
	jobs := flag.Int("jobs", min(4, runtime.GOMAXPROCS(0)), "Go package and analyzer concurrency")
	fresh := flag.Bool("fresh", false, "disable test result reuse for a deliberate fresh execution")
	repeat := flag.Int("repeat", 1, "repeat identical checks to distinguish warm reuse from execution cost")
	scope := flag.String("scope", "explicit", "explicit or changed local test scope")
	base := flag.String("base", "", "changed-scope comparison reference (default origin/main)")
	head := flag.String("head", "", "delivery report commit; empty includes working changes")
	consumer := flag.String("consumer", "", "delivery report's declared immediate consumer")
	planOnly := flag.Bool("plan", false, "print changed-scope plan without running tests")
	full := flag.Bool("full", false, "run all maintained modules' default tests instead of selecting")
	evidencePath := flag.String("evidence", "", "write a verification manifest (revision, scope, results, logs) to this path; for task=product-smoke, the evidence directory")
	outputMode := flag.String("output", "stream", "stream or summary (summary requires -evidence)")
	timeout := flag.Duration("timeout", commandTimeout, "total development-check time budget")
	automationOnly := flag.Bool("automation-only", false, "contracts: run only known automation self-tests and consumer contracts")
	flag.Parse()
	if *automationOnly && *task != "contracts" {
		return errors.New("automation-only requires task=contracts")
	}
	if (*outputMode != "stream" && *outputMode != "summary") || (*outputMode == "summary" && *evidencePath == "") {
		return errors.New("output must be stream or summary; summary requires -evidence so detailed logs are retained")
	}
	if flag.NArg() != 0 || *jobs < 1 || *repeat < 1 || *timeout <= 0 {
		return errors.New("jobs/repeat/timeout must be positive; use named flags for scope")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := validateExplicitTestSkip(
		skipTest.set, skipTest.name, *task, *scope, *full, *base, *planOnly,
	); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if *task == "delivery" && *evidencePath != "" {
		return errors.New("delivery produces a planning report, not a verification manifest; evidence is unsupported")
	}
	recorderEvidence := *evidencePath
	if *task == "product-smoke" {
		// product-smoke writes its own summary into the evidence directory
		// instead of a verification manifest.
		recorderEvidence = ""
	}
	recorder := newEvidenceRecorder(ctx, recorderEvidence, *task, evidence.Scope{
		Kind:     *scope,
		Module:   *module,
		Packages: strings.Fields(*packages),
		Base:     *base,
		Jobs:     *jobs,
		Repeat:   *repeat,
		Fresh:    *fresh,
		PlanOnly: *planOnly,
		Full:     *full,
	}, root)
	if recorder != nil {
		recorder.quiet, recorder.report = *outputMode == "summary", os.Stderr
		defer func() {
			if writeErr := recorder.finish(err); writeErr != nil {
				err = errors.Join(err, writeErr)
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *task == "delivery" {
		if *module != "." || *packages != "" || *scope != "explicit" || *full || *planOnly || *fresh || *repeat != 1 {
			return errors.New("delivery accepts base/head/consumer, not test scope or execution options")
		}
		cmd := exec.Command("go", "run", "-buildvcs=false", "./tools/changesize", "--report", "--base", *base, "--head", *head, "--consumer", *consumer)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_, err := execute(ctx, cmd, false, os.Stdout, nil)
		return err
	}
	if *head != "" || *consumer != "" {
		return errors.New("head/consumer require task=delivery")
	}
	if *task == "product-smoke" {
		if *module != "." || *packages != "" || *scope != "explicit" || *base != "" || *planOnly || *full || *fresh || *repeat != 1 || skipTest.set {
			return errors.New("product-smoke accepts only -evidence and -timeout; it always runs the whole required journey list")
		}
		return runProductSmoke(ctx, root, *evidencePath, os.Stdout, os.Stderr)
	}
	if *task == "contracts" {
		if *module != "." || *packages != "" || *scope != "changed" || *full {
			return errors.New("contracts requires scope=changed with no explicit MODULE/PKGS/full override")
		}
		return runContractCheck(ctx, root, *base, testPlanOptions{jobs: *jobs, repeat: *repeat, fresh: *fresh, dry: *planOnly, recorder: recorder, automationOnly: *automationOnly}, os.Stdout, os.Stderr)
	}
	quarantine, err := loadQuarantine(root)
	if err != nil {
		return err
	}
	if *task == "test" {
		for _, exclusion := range quarantine {
			recorder.skip(exclusion.Test, "quarantined: "+exclusion.Issue)
		}
	}
	if skipTest.set {
		recorder.skip(skipTest.name, "explicit requested test exclusion")
	}
	if *planOnly {
		recorder.skip("execution", "plan-only request")
	}
	if *task == "quarantine" {
		if err := checkQuarantine(ctx, root, quarantine); err != nil {
			return err
		}
		fmt.Println(skipPattern(quarantine))
		return nil
	}
	if *scope == "changed" {
		if err := validateChangedScope(*task, *module, *packages); err != nil {
			return err
		}
		start := time.Now()
		planning, stop := context.WithTimeout(ctx, 2*time.Minute)
		plan, planErr := testscope.Build(planning, root, testscope.Options{Base: *base, Full: *full})
		stop()
		if planErr != nil {
			recorder.block(fmt.Sprintf("select changed scope: %v", planErr))
			return planErr
		}
		fmt.Fprintf(os.Stderr, "planning_elapsed=%.3fs\n", time.Since(start).Seconds())
		return runTestPlan(ctx, root, plan, testPlanOptions{jobs: *jobs, repeat: *repeat, fresh: *fresh, dry: *planOnly, quarantine: quarantine, recorder: recorder}, os.Stdout, os.Stderr)
	}
	if *scope != "explicit" || *base != "" || *planOnly || *full {
		return errors.New("base/plan/full require -scope=changed; scope must be explicit or changed")
	}
	dir, err := moduleDirectory(root, *module)
	if err != nil {
		recorder.block(fmt.Sprintf("resolve module %s: %v", *module, err))
		return err
	}
	if *task == "doctor" {
		return doctor(ctx, dir)
	}
	commands, err := commandGroupsFor(
		*task,
		*module,
		*packages,
		*jobs,
		*fresh,
		os.Getenv("AIP_LOCAL_ARCH_TRIMPATH") == "1",
	)
	if err != nil {
		return err
	}
	if skipTest.set {
		goFlags, err := effectiveGOFlags(ctx, dir, append(os.Environ(), "GOWORK=off"))
		if err != nil {
			return err
		}
		pattern, err := combinedTestSkipPattern(goFlags, quarantine, skipTest.name)
		if err != nil {
			return fmt.Errorf("combine test skip patterns: %w", err)
		}
		for i := range commands {
			commands[i] = withExplicitTestSkip(commands[i], pattern)
		}
		fmt.Fprintf(
			os.Stderr,
			"Excluding top-level test %s from this development run.\n",
			skipTest.name,
		)
	} else {
		for i := range commands {
			commands[i] = withQuarantine(commands[i], quarantine)
		}
	}
	// Do not substitute staticcheck for the mandatory multi-linter gate, or
	// report success when the requested analyzer is absent.
	if _, err := exec.LookPath(commands[0][0]); err != nil {
		recorder.block(fmt.Sprintf("required tool %s is unavailable: %v", commands[0][0], err))
		return fmt.Errorf("required tool %s is unavailable: %w", commands[0][0], err)
	}
	fmt.Fprintln(os.Stderr, "Development feedback only; delivery still requires the applicable comprehensive gates.")
	for n := 1; n <= *repeat; n++ {
		for index, command := range commands {
			if err := ctx.Err(); err != nil {
				return err
			}
			command = withAnalyzerBudget(root, command)
			fmt.Fprintf(os.Stderr, "[%d/%d] module=%s command=%s\n", n, *repeat, *module, strings.Join(command, " "))
			start := time.Now()
			cmd := exec.Command(command[0], command[1:]...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off")
			cmd.Stderr = os.Stderr
			step := recorder.start(fmt.Sprintf("repeat-%d-command-%d-of-%d", n, index+1, len(commands)), command, dir, os.Stdout)
			cmd.Stderr = step.stderr(os.Stderr)
			stats, executeErr := execute(ctx, cmd, *task == "test", step.stdout(*task == "test"), step.rawLog())
			recorded := stepStats(*task == "test", stats)
			recorder.finishRun(step, recorded, executeErr)
			fmt.Fprintf(os.Stderr, "elapsed=%.3fs passed=%d cached=%d failed=%d skipped=%d\n", time.Since(start).Seconds(), stats.Passed, stats.Cached, stats.Failed, stats.Skipped)
			if executeErr != nil {
				return fmt.Errorf("%s failed: %w", *task, executeErr)
			}
		}
	}
	return nil
}

// stepStats maps the parsed test counters onto the manifest shape. Build and
// lint commands report no test events, so they carry no stats block.
func stepStats(test bool, stats testStats) *evidence.Stats {
	if !test {
		return nil
	}
	return &evidence.Stats{Passed: stats.Passed, Cached: stats.Cached, Failed: stats.Failed, Skipped: stats.Skipped}
}

func commandFor(task, scope string, jobs int, fresh bool) ([]string, error) {
	packages := strings.Fields(scope)
	if len(packages) == 0 {
		return nil, errors.New("explicit scope required: set PKGS='./path/to/package/...' (use ./... deliberately for the full module)")
	}
	if err := scopeplan.ValidatePackages(packages); err != nil {
		return nil, err
	}
	var command []string
	switch task {
	case "test":
		command = []string{"go", "test", "-mod=readonly", "-p=" + strconv.Itoa(jobs), "-parallel=" + strconv.Itoa(jobs), "-timeout=10m", "-json"}
		if fresh {
			command = append(command, "-count=1")
		}
	case "build":
		// Local development binaries do not need a new link solely because
		// another commit changed the embedded VCS revision. Release gates keep
		// their existing stamping and trimpath flags.
		command = []string{"go", "build", "-mod=readonly", "-p=" + strconv.Itoa(jobs), "-buildvcs=false"}
	case "lint":
		command = []string{"golangci-lint", "run", "--allow-parallel-runners", "--concurrency=" + strconv.Itoa(jobs), "--disable=modernize,paralleltest,thelper,gocognit"}
	default:
		return nil, fmt.Errorf("unknown task %q", task)
	}
	return append(command, packages...), nil
}

func commandGroupsFor(task, module, scope string, jobs int, fresh, localArchTrimpath bool) ([][]string, error) {
	if !localArchTrimpath || filepath.Clean(module) != "." || (task != "test" && task != "build") {
		command, err := commandFor(task, scope, jobs, fresh)
		if err != nil {
			return nil, err
		}
		return [][]string{command}, nil
	}

	packages := strings.Fields(scope)
	if len(packages) == 0 {
		command, err := commandFor(task, scope, jobs, fresh)
		if err != nil {
			return nil, err
		}
		return [][]string{command}, nil
	}
	// Keep broad scopes intact: splitting them could check archtest twice or
	// make the broad command use a different compiler cache variant.
	for _, packagePattern := range packages {
		if packagePattern == "./..." || packagePattern == "./internal/..." {
			command, err := commandFor(task, scope, jobs, fresh)
			if err != nil {
				return nil, err
			}
			return [][]string{command}, nil
		}
	}
	var archPackages, otherPackages []string
	for _, packagePattern := range packages {
		if isArchTestPattern(packagePattern) {
			archPackages = append(archPackages, packagePattern)
		} else {
			otherPackages = append(otherPackages, packagePattern)
		}
	}
	if len(archPackages) == 0 {
		command, err := commandFor(task, scope, jobs, fresh)
		if err != nil {
			return nil, err
		}
		return [][]string{command}, nil
	}

	type commandGroup struct {
		packages []string
		trimPath bool
	}
	groups := []commandGroup{{packages: otherPackages}, {packages: archPackages, trimPath: true}}
	if isArchTestPattern(packages[0]) {
		groups = []commandGroup{{packages: archPackages, trimPath: true}, {packages: otherPackages}}
	}
	commands := make([][]string, 0, 2)
	for _, group := range groups {
		if len(group.packages) == 0 {
			continue
		}
		command, err := commandFor(task, strings.Join(group.packages, " "), jobs, fresh)
		if err != nil {
			return nil, err
		}
		if group.trimPath {
			command = slices.Insert(command, len(command)-len(group.packages), "-trimpath")
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func isArchTestPattern(packagePattern string) bool {
	return packagePattern == "./internal/archtest" || strings.HasPrefix(packagePattern, "./internal/archtest/")
}

func moduleDirectory(root, module string) (string, error) {
	return scopeplan.Directory(root, module)
}

func consumeTests(input io.Reader, output io.Writer) (testStats, error) {
	return consumeTestEvents(input, output, nil)
}

func consumeTestEvents(input io.Reader, output io.Writer, observe func(testEvent)) (testStats, error) {
	var stats testStats
	decoder := json.NewDecoder(input)
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return stats, nil
			}
			return stats, fmt.Errorf("read test telemetry: %w", err)
		}
		if observe != nil {
			observe(event)
		}
		if _, err := io.WriteString(output, event.Output); err != nil {
			return stats, err
		}
		if event.Test != "" || event.Package == "" {
			continue
		}
		switch event.Action {
		case "output":
			if strings.HasPrefix(event.Output, "ok\t") || strings.HasPrefix(event.Output, "ok ") {
				if strings.Contains(event.Output, "(cached)") {
					stats.Cached++
				}
			}
		case "pass":
			stats.Passed++
		case "fail":
			stats.Failed++
		case "skip":
			stats.Skipped++
		}
	}
}

func doctor(ctx context.Context, dir string) error {
	fmt.Printf("module=%s logical_cpus=%d effective_gomaxprocs=%d\n", dir, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	cmd := exec.Command("go", "env", "-json", "GOVERSION", "GOTOOLCHAIN", "GOOS", "GOARCH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOWORK", "CGO_ENABLED", "GOPROXY")
	cmd.Dir = dir
	data, err := commandOutput(ctx, cmd)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	var env map[string]string
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		path := env[key]
		if path == "" || path == "off" {
			return fmt.Errorf("%s is disabled; restore a stable writable cache", key)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		probe, err := os.CreateTemp(path, "devcheck-probe-*")
		if err != nil {
			return fmt.Errorf("%s is not writable: %w", key, err)
		}
		if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
			return err
		}
	}
	for _, key := range []string{"GOMAXPROCS", "GODEBUG", "GOLANGCI_LINT_CACHE", "AIP_LINT_JOBS", "AIP_LINT_CONCURRENCY"} {
		fmt.Printf("%s=%s\n", key, os.Getenv(key))
	}
	if linter, err := exec.LookPath("golangci-lint"); err == nil {
		for _, args := range [][]string{{"version"}, {"cache", "status"}} {
			lint := exec.Command(linter, args...)
			lint.Dir = dir
			var output strings.Builder
			lint.Stderr = &output
			_, err := execute(ctx, lint, false, &output, nil)
			fmt.Print(output.String())
			if err != nil {
				if ctx.Err() != nil {
					return err
				}
				fmt.Printf("lint diagnostic unavailable: %v\n", err)
			}
		}
	} else {
		fmt.Println("golangci-lint is unavailable; dev-lint will fail until installed.")
	}
	fmt.Println("Keep cache paths stable across sessions/worktrees. Compare identical -repeat=2 checks before cleaning anything.")
	fmt.Println("-count=1 reruns tests; -a forces compilation. Race, coverage, tags, trimpath, and toolchain changes create distinct build variants.")
	fmt.Println("For unexplained misses: GODEBUG=gocachetest=1 go test ./path/to/package. For recompilation: go build -x ./path/to/package.")
	return nil
}
