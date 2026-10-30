package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/productsmoke"
	"github.com/aiproxer/aiproxer/tools/internal/sourcestate"
)

// Product-smoke budget and verdict constants. The Make target passes the
// command's absolute deadline, so the 600-second budget includes the `go run`
// compilation; the controller derives its work deadline from that value. A
// direct invocation without the absolute deadline falls back to the 570-second
// work budget and has no outer bound.
const (
	productSmokeTask              = "product-smoke"
	productSmokeCommandBudget     = 600 * time.Second
	productSmokeBudget            = 570 * time.Second
	productSmokeReportReserve     = 30 * time.Second
	productSmokeMinTestWindow     = 30 * time.Second
	productSmokeSourceVerifyLimit = 10 * time.Second
	productSmokeDeadlineEnv       = "AIP_PRODUCT_SMOKE_DEADLINE_UNIX"

	productSmokePassed = "pass"
	productSmokeFailed = "fail"

	productJourneyPassed      = "pass"
	productJourneyFailed      = "fail"
	productJourneyQuarantined = "quarantined"
	productJourneyUnfinished  = "unfinished"

	productSmokeBinaryEnv   = "AIP_PRODUCT_SMOKE_BIN"
	productSmokeEvidenceEnv = "AIP_PRODUCT_SMOKE_EVIDENCE"

	productSmokePackage     = "./internal/testkit/productsmoke"
	productSmokeBuildTag    = "productsmoke"
	productSmokeSummaryFile = "summary.json"
	productSmokeStreamFile  = "go-test.json.log"
	productSmokeStderrFile  = "go-test.stderr.log"
	productSmokeBuildLog    = "build.log"
	productSmokeJourneyDir  = "journeys"
	// productSmokeTestTimeoutPanic is the test binary's panic prefix when its
	// own -timeout fires; the run was cut even though no required test recorded
	// a terminal event.
	productSmokeTestTimeoutPanic = "panic: test timed out"
)

// productSmokeJourney is one required journey after platform filtering and
// quarantine mapping.
type productSmokeJourney struct {
	ID              string
	POSIXOnly       bool
	QuarantineIssue string
}

// observedProductTest is what the reconciler saw for one test name.
type observedProductTest struct {
	ran      bool
	terminal string
	elapsed  float64
	// invalid records a terminal event without a preceding run event, or a
	// repeated/contradictory transition. Normal go test output never produces
	// either sequence, so a PASS from it cannot be trusted.
	invalid bool
}

// productObservation collects the go test -json events reconciliation needs.
type productObservation struct {
	tests        map[string]*observedProductTest
	sawRun       bool
	cached       bool
	testTimedOut bool
}

// observe folds one go test event into the observation. The observer runs on
// the parser goroutine; executeObserved returns only after it has finished.
func (o *productObservation) observe(event testEvent) {
	// Go attributes the test-binary timeout panic to the test that was running,
	// so recognise the marker on any output event, not only package-level ones.
	if strings.Contains(event.Output, productSmokeTestTimeoutPanic) {
		o.testTimedOut = true
	}
	if event.Test == "" {
		if strings.Contains(event.Output, "(cached)") {
			o.cached = true
		}
		return
	}
	if o.tests == nil {
		o.tests = map[string]*observedProductTest{}
	}
	test := o.tests[event.Test]
	if test == nil {
		test = &observedProductTest{}
		o.tests[event.Test] = test
	}
	switch event.Action {
	case "run":
		if test.ran || test.terminal != "" {
			test.invalid = true
		}
		o.sawRun = true
		test.ran = true
	case "pass", "fail", "skip":
		if !test.ran || test.terminal != "" {
			test.invalid = true
		}
		test.terminal = event.Action
		test.elapsed = event.Elapsed
	}
}

// productSmokeJourneyResult is one journey's verdict.
type productSmokeJourneyResult struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	Detail          string  `json:"detail,omitempty"`
	ElapsedSeconds  float64 `json:"elapsed_seconds"`
	Evidence        string  `json:"evidence,omitempty"`
	QuarantineIssue string  `json:"quarantine_issue,omitempty"`
}

// productSmokeReport is the observed verdict for every required journey.
type productSmokeReport struct {
	Journeys []productSmokeJourneyResult
	Problems []string
}

// pass reports whether every non-quarantined required journey passed and the
// run itself produced no problem. A quarantined journey is never a pass, but it
// does not block the run.
func (r productSmokeReport) pass() bool {
	if len(r.Problems) != 0 {
		return false
	}
	for _, journey := range r.Journeys {
		if journey.Status != productJourneyPassed && journey.Status != productJourneyQuarantined {
			return false
		}
	}
	return true
}

// productSmokeSummary is the machine-readable record written beside the run.
type productSmokeSummary struct {
	SchemaVersion     int                         `json:"schema_version"`
	Task              string                      `json:"task"`
	Verdict           string                      `json:"verdict"`
	Reason            string                      `json:"reason,omitempty"`
	StartedAt         time.Time                   `json:"started_at"`
	FinishedAt        time.Time                   `json:"finished_at"`
	WallSeconds       float64                     `json:"wall_seconds"`
	Head              string                      `json:"head"`
	SourceFingerprint string                      `json:"source_fingerprint"`
	Dirty             bool                        `json:"dirty"`
	OS                string                      `json:"os"`
	EvidenceDir       string                      `json:"evidence_dir"`
	Binary            string                      `json:"binary"`
	BinarySHA256      string                      `json:"binary_sha256"`
	GoTestTimeout     string                      `json:"go_test_timeout"`
	Command           string                      `json:"command,omitempty"`
	CommandError      string                      `json:"command_error,omitempty"`
	Journeys          []productSmokeJourneyResult `json:"journeys"`
	Problems          []string                    `json:"problems,omitempty"`
}

// runProductSmoke builds aipstd once and runs the product-smoke package under
// one bounded deadline, then reconciles the result against the required
// journeys. It always reports what it observed and never turns an unobserved
// journey into a pass.
func runProductSmoke(ctx context.Context, root, evidencePath string, stdout, stderr io.Writer) error {
	started := time.Now()
	workDeadline, wallStarted, absoluteBudget, deadlineErr := productSmokeDeadlines(started)
	ctx, cancel := context.WithDeadline(ctx, workDeadline)
	defer cancel()

	dir, err := productSmokeEvidenceDir(root, evidencePath)
	if err != nil {
		return err
	}
	summaryPath := filepath.Join(dir, productSmokeSummaryFile)
	// Clear the previous verdict before any other startup work. A run killed
	// before it publishes its own summary must not leave an older pass behind.
	if err := clearProductSmokeSummary(summaryPath); err != nil {
		return err
	}
	summary := productSmokeSummary{
		SchemaVersion: 1,
		Task:          productSmokeTask,
		StartedAt:     wallStarted.UTC(),
		OS:            runtime.GOOS,
		EvidenceDir:   dir,
	}
	var required []productSmokeJourney
	// abort centralizes every infrastructure failure after the evidence
	// directory exists, so even a failure before the build leaves a failed
	// summary with a per-journey accounting instead of only an exit code.
	abort := func(cause error) error {
		status := productJourneyFailed
		if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
			status = productJourneyUnfinished
		}
		report := productSmokeNotRunReport(required, status, "not started: "+cause.Error())
		finishErr := finishProductSmoke(summaryPath, summary, report, []string{cause.Error()}, "", cause, wallStarted, stdout)
		return errors.Join(cause, finishErr)
	}
	if deadlineErr != nil {
		required = requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
		quarantine, loadErr := loadQuarantine(root)
		if loadErr != nil {
			return abort(errors.Join(deadlineErr, fmt.Errorf("load required journeys: %w", loadErr)))
		}
		required = requiredProductSmokeJourneys(productsmoke.Required, quarantine, runtime.GOOS)
		return abort(deadlineErr)
	}

	if absoluteBudget && time.Until(workDeadline) <= 0 {
		// The controller itself started after the work budget. Report every
		// required journey as unfinished: a startup compile that consumed the
		// command budget is a failure of the whole invocation, not a missing
		// verdict.
		required = requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
		quarantine, loadErr := loadQuarantine(root)
		if loadErr != nil {
			return abort(fmt.Errorf("the command deadline elapsed before the product smoke started, and the required journeys could not be loaded: %w", loadErr))
		}
		required = requiredProductSmokeJourneys(productsmoke.Required, quarantine, runtime.GOOS)
		cause := fmt.Errorf("the %s command deadline elapsed before the product smoke started (compilation and setup took too long)", productSmokeCommandBudget)
		report := productSmokeNotRunReport(required, productJourneyUnfinished, "not started: "+cause.Error())
		return finishProductSmoke(summaryPath, summary, report, []string{cause.Error()}, "", cause, wallStarted, stdout)
	}

	required = requiredProductSmokeJourneys(productsmoke.Required, nil, runtime.GOOS)
	if len(required) == 0 {
		return abort(fmt.Errorf("product smoke: no required journeys for %s", runtime.GOOS))
	}
	quarantine, err := loadQuarantine(root)
	if err != nil {
		return abort(err)
	}
	required = requiredProductSmokeJourneys(productsmoke.Required, quarantine, runtime.GOOS)
	source, dirty, err := productSmokeSource(ctx, root)
	if err != nil {
		return abort(err)
	}
	summary.Head = source.Head
	summary.SourceFingerprint = source.Fingerprint
	summary.Dirty = dirty
	journeyRoot := filepath.Join(dir, productSmokeJourneyDir)
	if err := resetProductSmokeJourneyEvidence(journeyRoot, required); err != nil {
		return abort(err)
	}

	binary := filepath.Join(dir, "aipstd"+productSmokeExecutableSuffix())
	buildLogPath := filepath.Join(dir, productSmokeBuildLog)
	buildLog, err := os.Create(buildLogPath)
	if err != nil {
		return abort(fmt.Errorf("product smoke: create build log: %w", err))
	}
	buildCmd := exec.Command("go", "build", "-mod=readonly", "-buildvcs=false", "-o", binary, "./cmd/aipstd")
	buildCmd.Dir = root
	buildCmd.Env = append(os.Environ(), "GOWORK=off")
	buildCmd.Stderr = io.MultiWriter(stderr, buildLog)
	_, buildErr := execute(ctx, buildCmd, false, io.MultiWriter(stdout, buildLog), nil)
	buildCloseErr := buildLog.Close()
	if buildErr != nil {
		status := productJourneyFailed
		detail := "not run: aipstd did not build"
		problems := []string{"build aipstd failed"}
		if ctxErr := ctx.Err(); ctxErr != nil {
			status = productJourneyUnfinished
			detail = "not started: the command deadline ended the build"
			problems = append(problems, "the command deadline ended the build: "+ctxErr.Error())
		}
		report := productSmokeNotRunReport(required, status, detail)
		return finishProductSmoke(summaryPath, summary, report, problems, "build aipstd", errors.Join(buildErr, buildCloseErr), wallStarted, stdout)
	}
	if buildCloseErr != nil {
		return abort(fmt.Errorf("product smoke: close the build log: %w", buildCloseErr))
	}
	hash, err := fileSHA256(binary)
	if err != nil {
		return abort(err)
	}
	summary.Binary = binary
	summary.BinarySHA256 = hash

	deadline, ok := ctx.Deadline()
	if !ok {
		return abort(errors.New("product smoke: the run context has no deadline"))
	}
	testTimeout, timeoutErr := productSmokeTestTimeout(time.Until(deadline))
	if timeoutErr != nil {
		report := productSmokeNotRunReport(required, productJourneyUnfinished, "not started: "+timeoutErr.Error())
		return finishProductSmoke(summaryPath, summary, report, []string{timeoutErr.Error()}, "", nil, wallStarted, stdout)
	}
	summary.GoTestTimeout = testTimeout.String()

	if err := os.MkdirAll(journeyRoot, 0o755); err != nil {
		return abort(fmt.Errorf("product smoke: create journey evidence: %w", err))
	}
	streamLog, err := os.Create(filepath.Join(dir, productSmokeStreamFile))
	if err != nil {
		return abort(fmt.Errorf("product smoke: create test stream log: %w", err))
	}
	stderrLog, err := os.Create(filepath.Join(dir, productSmokeStderrFile))
	if err != nil {
		return abort(errors.Join(fmt.Errorf("product smoke: create test stderr log: %w", err), streamLog.Close()))
	}

	argv := withQuarantine(productSmokeTestArgv(testTimeout), quarantine)
	testCmd := exec.Command(argv[0], argv[1:]...)
	testCmd.Dir = root
	testCmd.Env = append(os.Environ(),
		"GOWORK=off",
		productSmokeBinaryEnv+"="+binary,
		productSmokeEvidenceEnv+"="+journeyRoot,
	)
	testCmd.Stderr = io.MultiWriter(stderr, stderrLog)
	var observation productObservation
	_, testErr := executeObserved(ctx, testCmd, true, stdout, streamLog, executionOptions{
		observe:         observation.observe,
		killGroupOnExit: true,
	})
	testErr = errors.Join(testErr, streamLog.Close(), stderrLog.Close())

	cut := ctx.Err() != nil || observation.testTimedOut
	report := reconcileProductSmoke(observation, required, cut)
	for i := range report.Journeys {
		report.Journeys[i].Evidence = filepath.Join(journeyRoot, productJourneySlug(report.Journeys[i].ID))
	}
	var extraProblems []string
	if ctxErr := ctx.Err(); ctxErr != nil {
		extraProblems = append(extraProblems, "the command deadline ended the run: "+ctxErr.Error())
	}
	if sourceErr := verifyProductSmokeSource(root, source); sourceErr != nil {
		extraProblems = append(extraProblems, sourceErr.Error())
	}
	return finishProductSmoke(summaryPath, summary, report, extraProblems, "go test", testErr, wallStarted, stdout)
}

// finishProductSmoke writes the summary and prints the per-journey verdict. The
// returned error is the command verdict, not just a reporting failure.
func finishProductSmoke(
	summaryPath string,
	summary productSmokeSummary,
	report productSmokeReport,
	extraProblems []string,
	command string,
	commandErr error,
	started time.Time,
	stdout io.Writer,
) error {
	summary.FinishedAt = time.Now().UTC()
	summary.WallSeconds = time.Since(started).Seconds()
	summary.Journeys = report.Journeys
	summary.Problems = append(append([]string{}, report.Problems...), extraProblems...)
	summary.Command = command
	if commandErr != nil {
		summary.CommandError = commandErr.Error()
	}
	summary.Verdict = productSmokePassed
	if !report.pass() || commandErr != nil || len(extraProblems) != 0 {
		summary.Verdict = productSmokeFailed
		summary.Reason = productSmokeReason(report, extraProblems, command, commandErr)
	}
	reportErr := errors.Join(
		writeProductSmokeSummary(summaryPath, summary),
		printProductSmokeReport(stdout, summary),
	)
	if reportErr != nil {
		return fmt.Errorf("product smoke: report the %s verdict: %w", summary.Verdict, reportErr)
	}
	if summary.Verdict == productSmokeFailed {
		return fmt.Errorf("product smoke failed: %s", summary.Reason)
	}
	return nil
}

// productSmokeReason condenses every block into one line. It names each
// non-passing journey so a failure cannot be reduced to an opaque exit code.
func productSmokeReason(report productSmokeReport, extraProblems []string, command string, commandErr error) string {
	reasons := append([]string{}, extraProblems...)
	reasons = append(reasons, report.Problems...)
	for _, journey := range report.Journeys {
		switch journey.Status {
		case productJourneyFailed:
			reasons = append(reasons, journey.ID+" "+strings.TrimSpace(journey.Detail))
		case productJourneyUnfinished:
			reasons = append(reasons, journey.ID+" unfinished")
		}
	}
	if commandErr != nil {
		first := strings.TrimSpace(strings.SplitN(commandErr.Error(), "\n", 2)[0])
		label := strings.TrimSpace(command + ": " + first)
		reasons = append(reasons, label)
	}
	return strings.Join(uniqueProductSmokeStrings(reasons), "; ")
}

func uniqueProductSmokeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// productSmokeNotRunReport records journeys that never started. A quarantined
// journey keeps its quarantine verdict; nothing else is ever credited.
func productSmokeNotRunReport(required []productSmokeJourney, status, detail string) productSmokeReport {
	report := productSmokeReport{Journeys: make([]productSmokeJourneyResult, 0, len(required))}
	for _, requirement := range required {
		result := productSmokeJourneyResult{ID: requirement.ID, Status: status, Detail: detail}
		if requirement.QuarantineIssue != "" {
			result.Status = productJourneyQuarantined
			result.Detail = "quarantined with tracking issue " + requirement.QuarantineIssue
			result.QuarantineIssue = requirement.QuarantineIssue
		}
		report.Journeys = append(report.Journeys, result)
	}
	return report
}

// reconcileProductSmoke maps observed go test events onto required journeys.
// An unobserved journey is never a pass; cut marks a run whose deadline ended
// before a journey recorded a terminal event. A test-binary timeout panic is a
// cut even when the caller's deadline has not expired.
func reconcileProductSmoke(observation productObservation, required []productSmokeJourney, cut bool) productSmokeReport {
	report := productSmokeReport{Journeys: make([]productSmokeJourneyResult, 0, len(required))}
	if !observation.sawRun {
		report.Problems = append(report.Problems, "no test events observed; the product-smoke package ran no tests")
	}
	if observation.cached {
		report.Problems = append(report.Problems, "the Go test cache served the result; only observed execution counts")
	}
	if observation.testTimedOut {
		cut = true
		report.Problems = append(report.Problems, "the go test -timeout fired before the product-smoke deadline")
	}
	for _, requirement := range required {
		result := productSmokeJourneyResult{ID: requirement.ID}
		test := observation.tests[requirement.ID]
		switch {
		case requirement.QuarantineIssue != "":
			result.Status = productJourneyQuarantined
			result.Detail = "quarantined with tracking issue " + requirement.QuarantineIssue
			result.QuarantineIssue = requirement.QuarantineIssue
		case test != nil && test.invalid:
			result.Status = productJourneyFailed
			result.Detail = "inconsistent go test event stream"
		case test != nil && test.terminal == "fail":
			result.Status = productJourneyFailed
			result.Detail = "test failed"
		case test != nil && test.terminal == "skip":
			result.Status = productJourneyFailed
			result.Detail = "test skipped"
		case test != nil && test.ran && test.terminal == "pass":
			if observation.cached {
				result.Status = productJourneyFailed
				result.Detail = "cached result; not observed execution"
			} else {
				result.Status = productJourneyPassed
			}
		case cut:
			result.Status = productJourneyUnfinished
			result.Detail = productSmokeCutDetail(observation)
		case test != nil && test.ran:
			result.Status = productJourneyFailed
			result.Detail = "started but produced no terminal event" + productSmokeParentFailure(observation, requirement.ID)
		default:
			result.Status = productJourneyFailed
			result.Detail = "not observed" + productSmokeParentFailure(observation, requirement.ID)
		}
		if test != nil {
			result.ElapsedSeconds = test.elapsed
		}
		report.Journeys = append(report.Journeys, result)
	}
	report.Problems = append(report.Problems, productSmokeInconsistentEvents(observation)...)
	report.Problems = append(report.Problems, productSmokeUnexpectedFailures(observation, report.Journeys)...)
	return report
}

// productSmokeInconsistentEvents names every test whose event stream cannot be
// trusted, including non-required helpers. The run fails even if no required
// journey happened to depend on that test.
func productSmokeInconsistentEvents(observation productObservation) []string {
	var problems []string
	for name, test := range observation.tests {
		if test.invalid {
			problems = append(problems, "inconsistent go test event stream for "+name)
		}
	}
	sort.Strings(problems)
	return problems
}

// productSmokeCutDetail names what ended a run that left a journey without a
// terminal event.
func productSmokeCutDetail(observation productObservation) string {
	if observation.testTimedOut {
		return "no terminal event before the go test -timeout fired"
	}
	return "no terminal event before the run was cut short"
}

// productSmokeParentFailure names the nearest failed ancestor, so a journey
// that never reported its own terminal event explains where the run stopped.
func productSmokeParentFailure(observation productObservation, id string) string {
	parent := id
	for {
		index := strings.LastIndexByte(parent, '/')
		if index < 0 {
			return ""
		}
		parent = parent[:index]
		if test := observation.tests[parent]; test != nil && test.terminal == "fail" {
			return "; parent " + parent + " failed without a terminal event for this journey"
		}
	}
}

// productSmokeUnexpectedFailures reports failures outside the required set. A
// failing ancestor of a failed journey is already explained by that journey.
func productSmokeUnexpectedFailures(observation productObservation, journeys []productSmokeJourneyResult) []string {
	required := make(map[string]bool, len(journeys))
	var unfinished []string
	for _, journey := range journeys {
		required[journey.ID] = true
		if journey.Status == productJourneyFailed || journey.Status == productJourneyUnfinished {
			unfinished = append(unfinished, journey.ID)
		}
	}
	var problems []string
	for name, test := range observation.tests {
		if test.terminal != "fail" || required[name] {
			continue
		}
		explained := false
		for _, id := range unfinished {
			if strings.HasPrefix(id, name+"/") {
				explained = true
				break
			}
		}
		if !explained {
			problems = append(problems, "non-required test "+name+" failed")
		}
	}
	sort.Strings(problems)
	return problems
}

// requiredProductSmokeJourneys filters the portable list to the host OS and
// maps quarantine entries onto required subtests. The existing quarantine
// grammar only accepts top-level test names, so quarantining a parent covers
// every required subtest beneath it.
func requiredProductSmokeJourneys(all []productsmoke.Journey, entries []quarantineEntry, goos string) []productSmokeJourney {
	issues := make(map[string]string, len(entries))
	for _, entry := range entries {
		issues[entry.Test] = entry.Issue
	}
	required := make([]productSmokeJourney, 0, len(all))
	for _, journey := range all {
		if journey.POSIXOnly && goos == "windows" {
			continue
		}
		top := journey.ID
		if index := strings.IndexByte(top, '/'); index >= 0 {
			top = top[:index]
		}
		required = append(required, productSmokeJourney{
			ID:              journey.ID,
			POSIXOnly:       journey.POSIXOnly,
			QuarantineIssue: issues[top],
		})
	}
	return required
}

// productSmokeTestTimeout derives the go test -timeout from the remaining
// budget, keeping productSmokeReportReserve for reconciliation and reporting.
func productSmokeTestTimeout(remaining time.Duration) (time.Duration, error) {
	window := remaining - productSmokeReportReserve
	if window < productSmokeMinTestWindow {
		return 0, fmt.Errorf(
			"remaining product-smoke budget %s leaves %s after the %s reconciliation reserve, below the %s minimum test window",
			remaining.Truncate(time.Millisecond), window.Truncate(time.Millisecond),
			productSmokeReportReserve, productSmokeMinTestWindow,
		)
	}
	timeout := window.Truncate(time.Second)
	if timeout < productSmokeMinTestWindow {
		return 0, fmt.Errorf(
			"remaining product-smoke budget %s leaves %s after truncation, below the %s minimum test window",
			remaining.Truncate(time.Millisecond), timeout, productSmokeMinTestWindow,
		)
	}
	return timeout, nil
}

func productSmokeTestArgv(timeout time.Duration) []string {
	return []string{
		"go", "test",
		"-mod=readonly",
		"-json",
		"-count=1",
		"-timeout=" + timeout.String(),
		"-tags=" + productSmokeBuildTag,
		productSmokePackage,
	}
}

// productSmokeSource records HEAD, the source fingerprint and whether the
// worktree was dirty. The fingerprint binds a PASS to the exact index and
// working bytes, not only to the commit id.
func productSmokeSource(ctx context.Context, root string) (sourcestate.State, bool, error) {
	state, err := sourcestate.Snapshot(ctx, root)
	if err != nil {
		return sourcestate.State{}, false, fmt.Errorf("product smoke: identify source: %w", err)
	}
	status, err := productSmokeGit(ctx, root, "status", "--porcelain")
	if err != nil {
		return sourcestate.State{}, false, fmt.Errorf("product smoke: identify source: %w", err)
	}
	return state, status != "", nil
}

// verifyProductSmokeSource proves the tree did not change between the snapshot
// and the published verdict. It runs under its own short bound so an expired
// run still gets a source-stability verdict and a summary.
func verifyProductSmokeSource(root string, before sourcestate.State) error {
	ctx, cancel := context.WithTimeout(context.Background(), productSmokeSourceVerifyLimit)
	defer cancel()
	after, err := sourcestate.Snapshot(ctx, root)
	if err != nil {
		return fmt.Errorf("verify source after the product smoke: %w", err)
	}
	if after != before {
		return errors.New("source changed during the product smoke; rerun against stable inputs")
	}
	return nil
}

// productSmokeDeadlines returns the controller's work deadline, the reported
// command start and whether the Make target supplied an absolute command
// deadline. With no absolute deadline, the direct-invocation fallback starts
// the work budget now.
func productSmokeDeadlines(started time.Time) (workDeadline, wallStarted time.Time, absolute bool, err error) {
	raw := strings.TrimSpace(os.Getenv(productSmokeDeadlineEnv))
	if raw == "" {
		return started.Add(productSmokeBudget), started, false, nil
	}
	unix, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || unix <= 0 {
		return started.Add(productSmokeBudget), started, false, fmt.Errorf(
			"product smoke: %s=%q is not a positive Unix timestamp", productSmokeDeadlineEnv, raw,
		)
	}
	commandDeadline := time.Unix(unix, 0)
	return commandDeadline.Add(-productSmokeReportReserve), commandDeadline.Add(-productSmokeCommandBudget), true, nil
}

func productSmokeGit(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := commandOutput(ctx, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// productSmokeEvidenceDir returns an existing directory outside the repository.
// The default is a fresh run directory under the dev temp root; an explicit
// path is used as given so the caller controls retention. Evidence inside the
// source tree is rejected: the run writes build.log, aipstd and journey files,
// and the post-run source fingerprint would otherwise flag the verification
// machinery's own artifacts as a source change.
func productSmokeEvidenceDir(root, requested string) (string, error) {
	if requested != "" {
		absolute, err := productSmokeAbsoluteEvidencePath(requested)
		if err != nil {
			return "", fmt.Errorf("product smoke: resolve evidence directory: %w", err)
		}
		if err := rejectProductSmokeEvidenceInsideSource(root, absolute); err != nil {
			return "", err
		}
		if err := os.MkdirAll(absolute, 0o755); err != nil {
			return "", fmt.Errorf("product smoke: create evidence directory: %w", err)
		}
		return absolute, nil
	}
	tempRoot := os.Getenv("AIP_DEV_TMPDIR")
	if tempRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("product smoke: locate the dev temp root: %w", err)
		}
		tempRoot = filepath.Join(home, ".cache", "aip-tmp")
	}
	absoluteRoot, err := productSmokeAbsoluteEvidencePath(tempRoot)
	if err != nil {
		return "", fmt.Errorf("product smoke: resolve the dev temp root: %w", err)
	}
	if err := rejectProductSmokeEvidenceInsideSource(root, absoluteRoot); err != nil {
		return "", err
	}
	if err := os.MkdirAll(absoluteRoot, 0o755); err != nil {
		return "", fmt.Errorf("product smoke: create the dev temp root: %w", err)
	}
	dir, err := os.MkdirTemp(absoluteRoot, "product-smoke-")
	if err != nil {
		return "", fmt.Errorf("product smoke: create the evidence directory: %w", err)
	}
	if err := rejectProductSmokeEvidenceInsideSource(root, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// productSmokeAbsoluteEvidencePath resolves a path for comparison. It walks to
// the deepest existing ancestor, resolves that ancestor's symlinks, and appends
// the remaining components. A nonexistent directory below a symlinked ancestor
// must not be compared by its unresolved spelling.
func productSmokeAbsoluteEvidencePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var suffix []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for _, s := range slices.Backward(suffix) {
				resolved = filepath.Join(resolved, s)
			}
			return resolved, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	return absolute, nil
}

// rejectProductSmokeEvidenceInsideSource fails when evidence would be written
// under root. A different Windows volume is outside by definition.
func rejectProductSmokeEvidenceInsideSource(root, evidence string) error {
	rootAbsolute, err := productSmokeAbsoluteEvidencePath(root)
	if err != nil {
		return fmt.Errorf("product smoke: resolve the source tree: %w", err)
	}
	relative, err := filepath.Rel(rootAbsolute, evidence)
	if err != nil {
		return nil
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf(
			"product smoke: evidence directory %q is inside the source tree %q; choose a directory outside the repository (or set AIP_DEV_TMPDIR outside it)",
			evidence, rootAbsolute,
		)
	}
	return nil
}

func productSmokeExecutableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// clearProductSmokeSummary removes a previous verdict before this run can
// produce evidence, so an interrupted run cannot leave an older pass behind.
func clearProductSmokeSummary(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("product smoke: clear the previous summary: %w", err)
	}
	return nil
}

// resetProductSmokeJourneyEvidence clears the required journeys' evidence
// directories. Only the required journeys are touched, so an operator-chosen
// evidence directory keeps unrelated files.
func resetProductSmokeJourneyEvidence(journeyRoot string, required []productSmokeJourney) error {
	for _, requirement := range required {
		if err := os.RemoveAll(filepath.Join(journeyRoot, productJourneySlug(requirement.ID))); err != nil {
			return fmt.Errorf("product smoke: reset journey evidence: %w", err)
		}
	}
	return nil
}

// productJourneySlug mirrors the harness's per-journey evidence directory name
// (evidence_test.go journeySlug). Keep the two replacements in step.
func productJourneySlug(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	return replacer.Replace(name)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("product smoke: hash binary: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", fmt.Errorf("product smoke: hash binary: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// writeProductSmokeSummary replaces the summary atomically so a reader never
// sees a half-written verdict.
func writeProductSmokeSummary(path string, summary productSmokeSummary) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("product smoke: encode summary: %w", err)
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".summary-*.json")
	if err != nil {
		return fmt.Errorf("product smoke: create summary: %w", err)
	}
	tempPath := temp.Name()
	_, writeErr := temp.Write(data)
	closeErr := temp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("product smoke: write summary: %w", err), os.Remove(tempPath))
	}
	if err := os.Rename(tempPath, path); err != nil {
		return errors.Join(fmt.Errorf("product smoke: publish summary: %w", err), os.Remove(tempPath))
	}
	return nil
}

// printProductSmokeReport prints one line per journey plus the run identity.
func printProductSmokeReport(w io.Writer, summary productSmokeSummary) error {
	if _, err := fmt.Fprintf(w, "product-smoke: required=%d os=%s\n", len(summary.Journeys), summary.OS); err != nil {
		return err
	}
	for _, journey := range summary.Journeys {
		detail := journey.Detail
		if journey.Status != productJourneyPassed && journey.Evidence != "" {
			detail = strings.TrimSpace(detail + " evidence=" + productSmokeEvidenceLabel(journey.Evidence))
		}
		if _, err := fmt.Fprintf(w, "%-11s %-58s %8.2fs  %s\n", strings.ToUpper(journey.Status), journey.ID, journey.ElapsedSeconds, detail); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "wall=%.1fs head=%s dirty=%t\n", summary.WallSeconds, summary.Head, summary.Dirty); err != nil {
		return err
	}
	line := fmt.Sprintf("product smoke %s", strings.ToUpper(summary.Verdict))
	if summary.Reason != "" {
		line += ": " + summary.Reason
	}
	line += " summary=" + filepath.Join(summary.EvidenceDir, productSmokeSummaryFile)
	_, err := fmt.Fprintln(w, line)
	return err
}

// productSmokeEvidenceLabel marks an evidence path the harness never wrote, so
// a failed journey does not point at a directory that does not exist.
func productSmokeEvidenceLabel(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return path + " (not written)"
	}
	return path
}
