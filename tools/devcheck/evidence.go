package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
	"github.com/aiproxer/aiproxer/tools/internal/sourcestate"
)

// evidenceRecorder writes the manifest requested with -evidence and tees each
// command's output into its own log file beside it.
//
// The tee is the point of the log records: a manifest that names a log nobody
// wrote is worse than no log, because it invites the reader to trust evidence
// that does not exist. Without -evidence the recorder is nil and command
// output is untouched.
type evidenceRecorder struct {
	manifest    *evidence.Manifest
	path        string
	dir         string
	sequence    int
	blockReason string
	quiet       bool
	report      io.Writer
	source      sourcestate.State
}

func newEvidenceRecorder(ctx context.Context, path, task string, scope evidence.Scope, workdir string) *evidenceRecorder {
	if path == "" {
		return nil
	}
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workdir, absolute)
	}
	manifest := evidence.New(ctx, evidence.Input{
		Task:    task,
		Scope:   scope,
		Env:     os.Environ(),
		Workdir: workdir,
	})
	recorder := &evidenceRecorder{
		manifest: manifest,
		path:     absolute,
		dir:      filepath.Dir(absolute),
	}
	// Non-Git directories retain their existing explicitly unknown revision.
	// Git-backed checks must bind evidence to exact source and index contents.
	if manifest.Revision.Head != "" && (task == "test" || task == "build" || task == "lint" || task == "contracts") {
		state, err := sourcestate.Snapshot(ctx, workdir)
		if err != nil {
			recorder.block(fmt.Sprintf("capture check source: %v", err))
		} else {
			recorder.source = state
		}
	}
	if task == "test" || task == "contracts" {
		if fields, err := splitGoFlags(manifest.Environment.GoFlags); err == nil {
			if pattern, found, err := lastGoFlagsSkip(fields); err == nil && found && pattern != "" {
				recorder.skip(pattern, "inherited GOFLAGS test exclusion")
			}
		}
	}
	return recorder
}

// block records the first infrastructure condition that prevented the check
// from producing a code verdict. Later conditions add nothing: the run already
// cannot claim its scope passed.
func (r *evidenceRecorder) block(reason string) {
	if r == nil || r.blockReason != "" {
		return
	}
	r.blockReason = reason
}

func (r *evidenceRecorder) finish(runErr error) error {
	if r == nil {
		return nil
	}
	if r.source.Head != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		after, err := sourcestate.Snapshot(ctx, r.manifest.Revision.Worktree)
		cancel()
		if err != nil {
			r.block(fmt.Sprintf("verify check source: %v", err))
		} else if r.source != after {
			r.block("source changed during check; rerun against stable inputs")
		}
	}
	r.manifest.Finish(runErr, r.blockReason)
	writeErr := r.manifest.Write(r.path)
	if r.quiet && r.report != nil {
		_, writeSummaryErr := fmt.Fprintf(r.report, "verification=%s evidence=%s\n", r.manifest.Outcome, r.path)
		writeErr = errors.Join(writeErr, writeSummaryErr)
	}
	if runErr == nil && (r.manifest.Outcome == evidence.OutcomeBlocked || r.manifest.Outcome == evidence.OutcomeFailed) {
		writeErr = errors.Join(writeErr, errors.New(r.manifest.FailureReason))
	}
	return writeErr
}

func (r *evidenceRecorder) skip(target, reason string) {
	if r != nil {
		r.manifest.Skips = append(r.manifest.Skips, evidence.Skip{Target: target, Reason: reason})
	}
}

// commandRun is one in-flight command: its log file, the writer that streams
// into it, and the start time used for the recorded duration.
type commandRun struct {
	label     string
	command   []string
	dir       string
	startedAt time.Time
	logPath   string
	log       *os.File
	output    io.Writer
	live      io.Writer
	quiet     bool
	scope     evidence.Selection
}

// start opens this step's log and returns the writer command output must go to.
// A log that cannot be opened is not fatal: the check still runs, and the step
// simply records no log path.
func (r *evidenceRecorder) start(label string, command []string, dir string, stdout io.Writer) *commandRun {
	run := &commandRun{
		label:     label,
		command:   append([]string(nil), command...),
		dir:       dir,
		startedAt: time.Now(),
		output:    stdout,
	}
	if r == nil {
		return run
	}
	module, err := filepath.Rel(r.manifest.Revision.Worktree, dir)
	if err != nil {
		module = dir
	}
	run.scope.Module = filepath.ToSlash(module)
	for _, arg := range command {
		if arg == "." || strings.HasPrefix(arg, "./") {
			run.scope.Packages = append(run.scope.Packages, arg)
		}
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		r.block(fmt.Sprintf("create step log directory: %v", err))
		return run
	}
	if r.sequence == 0 {
		dir, err := os.MkdirTemp(r.dir, filepath.Base(r.path)+"-logs-*")
		if err != nil {
			r.block(fmt.Sprintf("create run log directory: %v", err))
			return run
		}
		r.dir = dir
	}
	r.sequence++
	name := fmt.Sprintf("step-%02d-%s.log", r.sequence, sanitizeLogLabel(label))
	path := filepath.Join(r.dir, name)
	log, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		r.block(fmt.Sprintf("open step log %s: %v", path, err))
		return run
	}
	run.log = log
	run.logPath = path
	run.quiet = r.quiet
	if run.quiet {
		stdout = io.Discard
	}
	run.live = stdout
	run.output = io.MultiWriter(log, stdout)
	return run
}

func (run *commandRun) stdout(test bool) io.Writer {
	if test && run.log != nil {
		return run.live
	}
	return run.output
}

func (run *commandRun) rawLog() io.Writer {
	if run.log == nil {
		return nil
	}
	return run.log
}

func (run *commandRun) stderr(live io.Writer) io.Writer {
	if run.log == nil {
		return live
	}
	if run.quiet {
		live = io.Discard
	}
	return io.MultiWriter(run.log, live)
}

// finish records the step result and closes its log. stats is nil for commands
// that do not emit the test event stream.
func (r *evidenceRecorder) finishRun(run *commandRun, stats *evidence.Stats, runErr error) {
	if run == nil {
		return
	}
	if run.log != nil {
		runErr = errors.Join(runErr, run.log.Close())
	}
	if r == nil {
		return
	}
	step := evidence.Step{
		Label:      run.label,
		Command:    run.command,
		WorkingDir: run.dir,
		StartedAt:  run.startedAt,
		DurationMS: time.Since(run.startedAt).Milliseconds(),
		Result:     evidence.OutcomePassed,
		ExitCode:   exitCode(runErr),
		LogPath:    run.logPath,
		Tests:      stats,
		Scope:      run.scope,
	}
	if runErr != nil {
		step.Result = evidence.OutcomeFailed
	}
	r.manifest.Steps = append(r.manifest.Steps, step)
	if r.quiet && r.report != nil {
		if _, err := fmt.Fprintf(r.report, "%s: %s elapsed=%.3fs scope=%s:%v log=%s\n", step.Label, step.Result, float64(step.DurationMS)/1000, step.Scope.Module, step.Scope.Packages, step.LogPath); err != nil {
			r.block(fmt.Sprintf("write summary: %v", err))
		} else if step.Result == evidence.OutcomeFailed && step.LogPath != "" {
			if err := printStepFailureExcerpt(r.report, step.LogPath); err != nil {
				r.block(fmt.Sprintf("write summary failure excerpt: %v", err))
			}
		}
	}
}

// summaryFailureExcerptMaxLines bounds the failure excerpt printed to the
// summary report after a failed step. Failing-test identity lines and output
// excerpt share the budget; a cut excerpt ends with the full log path.
const summaryFailureExcerptMaxLines = 60

// testEvent decodes one `go test -json` line from a step log.
type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// containsFailureKeyword reports whether s carries a failure marker: a go
// test FAIL line, a runtime panic, or a data race report.
func containsFailureKeyword(s string) bool {
	return strings.Contains(s, "FAIL") ||
		strings.Contains(s, "panic:") ||
		strings.Contains(s, "DATA RACE")
}

// newLogScanner reads a step log incrementally with a large buffer: a single
// `go test -json` Output line can carry a large failure dump.
func newLogScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 10<<20)
	return scanner
}

// decodeTestEvent parses one step-log line as a `go test -json` event.
// Non-JSON lines report false.
func decodeTestEvent(line string, event *testEvent) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	if err := json.Unmarshal([]byte(trimmed), event); err != nil {
		return false
	}
	return true
}

// excerptWriter counts excerpt lines against the shared budget and records
// the first report write error.
type excerptWriter struct {
	report io.Writer
	shown  int
	cut    bool
	err    error
}

func (w *excerptWriter) emit(line string) bool {
	if w.err != nil {
		return false
	}
	if w.shown >= summaryFailureExcerptMaxLines {
		w.cut = true
		return false
	}
	_, w.err = fmt.Fprintln(w.report, line)
	if w.err != nil {
		return false
	}
	w.shown++
	return true
}

// printStepFailureExcerpt prints the failing-test identity lines plus a
// bounded output excerpt from a failed step's log to the summary report, so a
// summary-mode failure stays actionable without opening the log files. The
// log is read incrementally, never all at once. A report write error is
// returned for the caller to record against the run.
func printStepFailureExcerpt(report io.Writer, logPath string) error {
	failed, identities, err := scanFailedTests(logPath)
	if err != nil {
		_, werr := fmt.Fprintf(report, "failure excerpt unavailable: %v\n", err)
		return werr
	}
	w := &excerptWriter{report: report}
	for _, identity := range identities {
		if !w.emit(identity) {
			break
		}
	}
	if err := emitFailureExcerpt(logPath, failed, w.emit); err != nil {
		_, werr := fmt.Fprintf(report, "failure excerpt unavailable: %v\n", err)
		return errors.Join(w.err, werr)
	}
	if w.err != nil {
		return w.err
	}
	if w.cut {
		if _, err := fmt.Fprintf(report, "... truncated; full log: %s\n", logPath); err != nil {
			return err
		}
	}
	if w.shown == 0 {
		if _, err := fmt.Fprintln(report, "(no failed test events found in step log; see full log for details)"); err != nil {
			return err
		}
	}
	return nil
}

// scanFailedTests reads the step log incrementally and returns the set of
// failed (package, test) pairs plus one identity line per Action=="fail"
// event, in log order.
func scanFailedTests(logPath string) (map[string]bool, []string, error) {
	file, err := os.Open(logPath)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	failed := make(map[string]bool)
	var identities []string
	scanner := newLogScanner(file)
	for scanner.Scan() {
		var event testEvent
		if !decodeTestEvent(scanner.Text(), &event) {
			continue
		}
		if event.Action != "fail" {
			continue
		}
		name := event.Test
		if name == "" {
			name = "(package)"
		} else {
			failed[event.Package+"\x00"+event.Test] = true
		}
		identities = append(identities, "FAIL package="+event.Package+" test="+name)
	}
	return failed, identities, scanner.Err()
}

// emitFailureExcerpt streams the failure-relevant output lines from the step
// log through emit: Output lines carrying a failure keyword, the failing
// tests' own output, and non-JSON lines carrying FAIL, which covers build
// failures.
func emitFailureExcerpt(logPath string, failed map[string]bool, emit func(string) bool) error {
	file, err := os.Open(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := newLogScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		var event testEvent
		if decodeTestEvent(line, &event) {
			if event.Action != "output" {
				continue
			}
			for _, excerpt := range excerptLines(event, failed) {
				if !emit(excerpt) {
					return nil
				}
			}
			continue
		}
		if strings.Contains(line, "FAIL") {
			if !emit(line) {
				return nil
			}
		}
	}
	return scanner.Err()
}

// excerptLines selects the failure-relevant lines from one Output event:
// lines carrying a failure keyword, plus every line of a failed test's own
// output.
func excerptLines(event testEvent, failed map[string]bool) []string {
	owned := event.Test != "" && failed[event.Package+"\x00"+event.Test]
	var out []string
	for line := range strings.SplitSeq(event.Output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if owned || containsFailureKeyword(line) {
			out = append(out, line)
		}
	}
	return out
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func sanitizeLogLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "command"
	}
	return trimmed
}
