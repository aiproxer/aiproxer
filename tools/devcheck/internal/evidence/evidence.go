// Package evidence records the exact revision, scope and result of a
// development check.
//
// A developer gate answers "did this code pass here?". A verification manifest
// answers the harder question a reviewer asks: which revision, with which tree
// state, under which scope, produced which per-command results, and where are
// the raw logs. Everything recorded is observed from the run itself, so a
// manifest cannot claim more than the check proved.
package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

// SchemaVersion is the manifest format version. Readers must reject an unknown
// value rather than interpret fields they do not know.
const SchemaVersion = 1

// Tool identifies the producer. A future runner may reuse this schema with its
// own Tool value instead of inventing a second format.
const Tool = "devcheck"

// Run outcomes. `blocked` is deliberately distinct from `failed`: an
// infrastructure condition (missing tool, exhausted resource slot, unreadable
// repository) says nothing about the code under test, and reporting it as a
// failure invites the reader to chase a defect that does not exist.
const (
	OutcomePassed  = "passed"
	OutcomeFailed  = "failed"
	OutcomeBlocked = "blocked"
	OutcomeSkipped = "skipped"
)

// Manifest is the complete record of one development check.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	Tool          string      `json:"tool"`
	Task          string      `json:"task"`
	Outcome       string      `json:"outcome"`
	FailureReason string      `json:"failure_reason,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	FinishedAt    time.Time   `json:"finished_at"`
	Revision      Revision    `json:"revision"`
	Scope         Scope       `json:"scope"`
	Environment   Environment `json:"environment"`
	Steps         []Step      `json:"steps"`
	Totals        Totals      `json:"totals"`
	Skips         []Skip      `json:"skips,omitempty"`
}

// Skip describes an intentionally omitted check or requested test exclusion.
type Skip struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// Selection is the actual module/package scope of one recorded command.
type Selection struct {
	Module   string   `json:"module"`
	Packages []string `json:"packages,omitempty"`
}

// Revision identifies the tree the check ran against. A clean revision is the
// only state whose manifest describes reproducible work; `dirty` keeps the
// manifest honest when it does not.
type Revision struct {
	Head            string   `json:"head"`
	Branch          string   `json:"branch"`
	Detached        bool     `json:"detached"`
	Dirty           bool     `json:"dirty"`
	DirtyPathCount  int      `json:"dirty_path_count"`
	DirtyGoFiles    []string `json:"dirty_go_files,omitempty"`
	DirtyDigest     string   `json:"dirty_digest"`
	DirtyTruncated  bool     `json:"dirty_content_truncated,omitempty"`
	MergeInProgress bool     `json:"merge_in_progress"`
	Worktree        string   `json:"worktree"`
}

// Scope records what the check was asked to cover, so a narrow scope can never
// be read as a repository-wide result.
type Scope struct {
	Kind     string   `json:"kind"`
	Module   string   `json:"module"`
	Packages []string `json:"packages,omitempty"`
	Base     string   `json:"base,omitempty"`
	Jobs     int      `json:"jobs"`
	Repeat   int      `json:"repeat"`
	Fresh    bool     `json:"fresh"`
	PlanOnly bool     `json:"plan_only"`
	Full     bool     `json:"full"`
}

// Environment records the toolchain that produced the result. A cache, race or
// trimpath variant is a different build; recording the versions keeps results
// comparable across hosts.
type Environment struct {
	GoVersion         string `json:"go_version"`
	GoFlags           string `json:"go_flags,omitempty"`
	LintVersion       string `json:"lint_version,omitempty"`
	GOOS              string `json:"goos"`
	GOARCH            string `json:"goarch"`
	GOMAXPROCS        int    `json:"gomaxprocs"`
	LogicalCPUs       int    `json:"logical_cpus"`
	CGOEnabled        string `json:"cgo_enabled,omitempty"`
	ConcurrencyPolicy string `json:"concurrency_policy,omitempty"`
}

// Step is one executed command and its measured result.
type Step struct {
	Label      string    `json:"label"`
	Command    []string  `json:"command"`
	WorkingDir string    `json:"working_dir"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Result     string    `json:"result"`
	ExitCode   int       `json:"exit_code"`
	Tests      *Stats    `json:"tests,omitempty"`
	LogPath    string    `json:"log_path,omitempty"`
	Scope      Selection `json:"scope"`
}

// Stats counts test events observed on the command's JSON stream.
type Stats struct {
	Passed  int `json:"passed"`
	Cached  int `json:"cached"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// Totals aggregates the steps. `commands_completed` below `commands_recorded`
// means the run stopped before finishing every planned command.
type Totals struct {
	CommandsRecorded  int   `json:"commands_recorded"`
	CommandsCompleted int   `json:"commands_completed"`
	CommandsFailed    int   `json:"commands_failed"`
	DurationMS        int64 `json:"duration_ms"`
	TestsPassed       int   `json:"tests_passed"`
	TestsCached       int   `json:"tests_cached"`
	TestsFailed       int   `json:"tests_failed"`
	TestsSkipped      int   `json:"tests_skipped"`
}

// Input is everything the caller knows when a check starts.
type Input struct {
	Task    string
	Scope   Scope
	Env     []string
	Workdir string
	// BlockedReason, when non-empty, records an infrastructure condition that
	// prevented the check from running.
	BlockedReason string
}

// New stamps a manifest with the observed revision and environment. Git and
// toolchain probes are best-effort: an unreadable repository yields an
// explicitly unknown revision rather than a fabricated one.
func New(ctx context.Context, in Input) *Manifest {
	started := time.Now()
	return &Manifest{
		SchemaVersion: SchemaVersion,
		Tool:          Tool,
		Task:          in.Task,
		Outcome:       OutcomePassed,
		StartedAt:     started,
		Revision:      revision(ctx, in.Workdir),
		Scope:         in.Scope,
		Environment:   environment(ctx, in.Workdir, in.Env),
		Steps:         []Step{},
	}
}

// Finish stamps the terminal outcome and recomputes the totals.
func (m *Manifest) Finish(err error, blocked string) {
	m.FinishedAt = time.Now()
	m.Totals = totals(m.Steps, m.StartedAt, m.FinishedAt)
	switch {
	case blocked != "":
		m.Outcome = OutcomeBlocked
		m.FailureReason = blocked
	case err != nil:
		m.Outcome = OutcomeFailed
		m.FailureReason = err.Error()
	case m.Totals.CommandsFailed > 0:
		m.Outcome = OutcomeFailed
		m.FailureReason = "one or more recorded steps failed"
	case m.Scope.PlanOnly || (len(m.Steps) == 0 && len(m.Skips) > 0):
		m.Outcome = OutcomeSkipped
	default:
		m.Outcome = OutcomePassed
	}
}

// maxDirtyContentFiles bounds the content hashing of the dirty digest. The
// dirty set is normally a handful of files, but a failed build can leave
// thousands untracked; truncating is reported rather than hidden, because a
// digest over an arbitrary prefix would silently collide with a different tree.
const maxDirtyContentFiles = 256

func totals(steps []Step, started, finished time.Time) Totals {
	sum := Totals{CommandsRecorded: len(steps), DurationMS: finished.Sub(started).Milliseconds()}
	for _, step := range steps {
		if step.Result == OutcomePassed {
			sum.CommandsCompleted++
		} else {
			sum.CommandsFailed++
		}
		// A failed step's counters are the evidence a reader needs most: which
		// tests failed and how many passed before that.
		if step.Tests == nil {
			continue
		}
		sum.TestsPassed += step.Tests.Passed
		sum.TestsCached += step.Tests.Cached
		sum.TestsFailed += step.Tests.Failed
		sum.TestsSkipped += step.Tests.Skipped
	}
	return sum
}

// Write encodes the manifest to path, creating parent directories. The file is
// written atomically so a reader never observes a partial manifest.
func (m *Manifest) Write(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode verification manifest: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".verification-manifest-*.json")
	if err != nil {
		return fmt.Errorf("create verification manifest: %w", err)
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write verification manifest: %w", err), temp.Close(), os.Remove(name))
	}
	if err := temp.Close(); err != nil {
		return errors.Join(fmt.Errorf("close verification manifest: %w", err), os.Remove(name))
	}
	if err := os.Rename(name, path); err != nil {
		return errors.Join(fmt.Errorf("publish verification manifest: %w", err), os.Remove(name))
	}
	return nil
}

func environment(ctx context.Context, workdir string, env []string) Environment {
	out := Environment{
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		GOMAXPROCS:  runtime.GOMAXPROCS(0),
		LogicalCPUs: runtime.NumCPU(),
	}
	out.GoVersion = strings.TrimSpace(gitFreeOutput(ctx, workdir, env, "go", "version"))
	out.GoFlags = strings.TrimSpace(gitFreeOutput(ctx, workdir, env, "go", "env", "GOFLAGS"))
	if lint, lookErr := exec.LookPath("golangci-lint"); lookErr == nil {
		out.LintVersion = strings.TrimSpace(gitFreeOutput(ctx, workdir, env, lint, "version"))
	}
	out.CGOEnabled = value(env, "CGO_ENABLED")
	out.ConcurrencyPolicy = value(env, "AIP_GO_SLOTS")
	return out
}

func revision(ctx context.Context, workdir string) Revision {
	out := Revision{Worktree: workdir}
	head, err := gitOutput(ctx, workdir, "rev-parse", "HEAD")
	if err != nil {
		return out
	}
	out.Head = head
	if branch, err := gitOutput(ctx, workdir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		out.Branch = branch
		out.Detached = branch == "HEAD"
	}
	porcelain, err := gitRaw(ctx, workdir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return out
	}
	paths := porcelainPaths(porcelain)
	out.DirtyPathCount = len(paths)
	out.Dirty = len(paths) > 0
	out.DirtyGoFiles = goPaths(paths)
	digest, truncated := dirtyDigest(workdir, porcelain, paths)
	out.DirtyDigest = digest
	out.DirtyTruncated = truncated
	_, err = gitOutput(ctx, workdir, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	out.MergeInProgress = err == nil
	return out
}

// dirtyDigest identifies the tree content a manifest describes, not just the
// path list. `git status` reports "?? notes.txt" without the file's bytes, so
// hashing the porcelain payload alone would call two different dirty trees
// identical and let one manifest vouch for another.
func dirtyDigest(workdir string, porcelain []byte, paths []string) (string, bool) {
	hash := sha256.New()
	_, _ = hash.Write(porcelain)
	truncated := len(paths) > maxDirtyContentFiles
	for i, path := range paths {
		if i >= maxDirtyContentFiles {
			break
		}
		_, _ = hash.Write([]byte("\n"))
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte(" "))
		data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(path)))
		if err != nil {
			// A path git reports but cannot read (a submodule, a socket, a
			// file removed mid-scan) still contributes its name, so the digest
			// differs from a tree where the path is absent.
			_, _ = hash.Write([]byte("<unreadable>"))
			continue
		}
		_, _ = hash.Write([]byte(hex.EncodeToString(sha256Sum(data))))
	}
	return hex.EncodeToString(hash.Sum(nil)), truncated
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// porcelainPaths splits the NUL-delimited porcelain v1 payload. A rename or
// copy entry carries its source path in the following field, so both entries
// count toward the dirty set.
func porcelainPaths(raw []byte) []string {
	fields := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if len(field) < 4 || field[2] != ' ' {
			continue
		}
		x, y := field[0], field[1]
		paths = append(paths, filepath.ToSlash(field[3:]))
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
			if i < len(fields) {
				paths = append(paths, filepath.ToSlash(fields[i]))
			}
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

func goPaths(paths []string) []string {
	var out []string
	for _, path := range paths {
		if strings.HasSuffix(path, ".go") {
			out = append(out, path)
		}
	}
	return out
}

func value(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if found, ok := strings.CutPrefix(entry, prefix); ok {
			return found
		}
	}
	return ""
}

func gitOutput(ctx context.Context, workdir string, args ...string) (string, error) {
	data, err := gitRaw(ctx, workdir, args...)
	return strings.TrimSpace(string(data)), err
}

func gitRaw(ctx context.Context, workdir string, args ...string) ([]byte, error) {
	return taskrunner.Output(ctx, taskrunner.Request{
		Argv: append([]string{"git"}, args...), Dir: workdir,
		Env: gitscope.Environ(), ClearEnv: true, Timeout: 2 * time.Minute,
	})
}

// gitFreeOutput runs a non-git probe with the same environment hygiene, so a
// stray GIT_DIR inherited from a hook cannot redirect it.
func gitFreeOutput(ctx context.Context, workdir string, env []string, name string, args ...string) string {
	data, err := taskrunner.Output(ctx, taskrunner.Request{
		Argv: append([]string{name}, args...), Dir: workdir,
		Env: gitscope.WithoutRepoEnv(env), ClearEnv: env != nil, Timeout: 2 * time.Minute,
	})
	if err != nil {
		return ""
	}
	return string(data)
}
