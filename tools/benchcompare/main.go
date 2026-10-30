// Command benchcompare alternates matching benchmark runs and retains evidence.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

const benchstatVersion = "v0.0.0-20261009192801-be2c69fb417e"

type options struct {
	Baseline, Candidate, Module, Packages, Bench, Fixtures, Out, Benchtime, CPU string
	Samples                                                                     int
	Timeout                                                                     time.Duration
}

type sample struct {
	Revision  string   `json:"revision"`
	Round     int      `json:"round"`
	Command   []string `json:"command"`
	Log       string   `json:"log"`
	ElapsedMS int64    `json:"elapsed_ms"`
	Result    string   `json:"result"`
}

type report struct {
	Heads            map[string]string `json:"heads"`
	Environment      map[string]string `json:"environment"`
	FixtureDigest    string            `json:"fixture_digest"`
	BenchstatVersion string            `json:"benchstat_version"`
	TimingEvidence   bool              `json:"sufficient_samples"`
	Samples          []sample          `json:"samples"`
	Outcome          string            `json:"outcome"`
	Error            string            `json:"error,omitempty"`
}

func main() {
	var opts options
	flag.StringVar(&opts.Baseline, "baseline", "", "existing clean baseline worktree")
	flag.StringVar(&opts.Candidate, "candidate", "", "existing clean candidate worktree")
	flag.StringVar(&opts.Module, "module", ".", "relative module directory in both revisions")
	flag.StringVar(&opts.Packages, "packages", "", "identical space-separated relative packages")
	flag.StringVar(&opts.Bench, "bench", "", "benchmark regexp")
	flag.StringVar(&opts.Fixtures, "fixtures", "", "comma-separated identical fixture files/directories")
	flag.StringVar(&opts.Out, "out", "", "new external evidence directory")
	flag.StringVar(&opts.Benchtime, "benchtime", "1s", "identical benchmark duration")
	flag.StringVar(&opts.CPU, "cpu", "1", "identical Go benchmark CPU list")
	flag.IntVar(&opts.Samples, "samples", 10, "repetitions per revision")
	flag.DurationVar(&opts.Timeout, "timeout", 15*time.Minute, "per-command budget")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := compare(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func compare(ctx context.Context, opts options) (runErr error) {
	if opts.Baseline == "" || opts.Candidate == "" || opts.Packages == "" || opts.Bench == "" || opts.Fixtures == "" || opts.Out == "" || opts.Samples < 1 || opts.Timeout <= 0 {
		return errors.New("baseline/candidate/packages/bench/fixtures/out and positive samples/timeout are required")
	}
	for pkg := range strings.FieldsSeq(opts.Packages) {
		if (pkg != "." && !strings.HasPrefix(pkg, "./")) || slices.Contains(strings.Split(filepath.ToSlash(pkg), "/"), "..") {
			return fmt.Errorf("invalid package %q", pkg)
		}
	}
	if filepath.IsAbs(opts.Module) || slices.Contains(strings.Split(filepath.ToSlash(opts.Module), "/"), "..") {
		return errors.New("module must stay within both revision roots")
	}
	roots := map[string]string{"baseline": opts.Baseline, "candidate": opts.Candidate}
	record := report{Heads: map[string]string{}, BenchstatVersion: benchstatVersion, TimingEvidence: sufficientSamples(opts.Samples), Outcome: "failed"}
	for label, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		roots[label] = root
		head, err := probe(ctx, root, opts.Timeout, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		status, err := probe(ctx, root, opts.Timeout, "git", "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(status)) != 0 {
			return fmt.Errorf("%s worktree is dirty; refusing ambiguous benchmark identity", label)
		}
		record.Heads[label] = strings.TrimSpace(string(head))
		dir, err := contained(root, opts.Module)
		if err != nil {
			return err
		}
		data, err := probe(ctx, dir, opts.Timeout, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS")
		if err != nil {
			return err
		}
		var environment map[string]string
		if err := json.Unmarshal(data, &environment); err != nil {
			return err
		}
		if record.Environment == nil {
			record.Environment = environment
		} else if !reflect.DeepEqual(record.Environment, environment) {
			return errors.New("toolchains/build flags differ between revisions")
		}
	}
	digest, err := fixtureIdentity(roots["baseline"], roots["candidate"], strings.Split(opts.Fixtures, ","))
	if err != nil {
		return err
	}
	record.FixtureDigest = digest
	out, err := filepath.Abs(opts.Out)
	if err != nil {
		return err
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root, out)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("evidence directory must be outside revision roots")
		}
	}
	if err := os.Mkdir(out, 0o755); err != nil {
		return fmt.Errorf("create new evidence directory: %w", err)
	}
	defer func() {
		record.Outcome = "passed"
		if runErr != nil {
			record.Outcome = "failed"
			record.Error = runErr.Error()
		}
		data, err := json.MarshalIndent(record, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(out, "report.json"), data, 0o600)
		}
		runErr = errors.Join(runErr, err)
	}()
	if !record.TimingEvidence {
		fmt.Fprintln(os.Stderr, "WARNING: insufficient samples for timing claims; collect at least six repetitions per revision and inspect benchstat intervals.")
	}
	var expectedNames []string
	for round := 0; round < opts.Samples; round++ {
		for _, label := range roundOrder(round) {
			dir, err := contained(roots[label], opts.Module)
			if err != nil {
				return err
			}
			argv := []string{"go", "test", "-mod=readonly", "-run=^$", "-bench=" + opts.Bench, "-benchmem", "-count=1", "-benchtime=" + opts.Benchtime, "-cpu=" + opts.CPU, "-p=1"}
			argv = append(argv, strings.Fields(opts.Packages)...)
			path := filepath.Join(out, fmt.Sprintf("%s-%02d.txt", label, round+1))
			log, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			result := taskrunner.Run(ctx, taskrunner.Request{Argv: argv, Dir: dir, Env: []string{"GOWORK=off"}, Timeout: opts.Timeout, Output: taskrunner.Stream, StreamOut: log, StreamErr: log})
			err = errors.Join(result.Err, result.Cleanup.Err, result.AccountingErr, log.Close())
			record.Samples = append(record.Samples, sample{Revision: label, Round: round + 1, Command: argv, Log: path, ElapsedMS: result.Elapsed.Milliseconds(), Result: string(result.Kind)})
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := validateSample(data); err != nil {
				return err
			}
			names := measurementNames(data)
			if expectedNames == nil {
				expectedNames = names
			} else if !slices.Equal(expectedNames, names) {
				return errors.New("benchmark selections differ between samples/revisions")
			}
			aggregate, err := os.OpenFile(filepath.Join(out, label+".txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, writeErr := aggregate.Write(data)
			if err := errors.Join(writeErr, aggregate.Close()); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s sample %d/%d retained\n", label, round+1, opts.Samples)
		}
	}
	for label, root := range roots {
		head, err := probe(ctx, root, opts.Timeout, "git", "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		status, err := probe(ctx, root, opts.Timeout, "git", "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(head)) != record.Heads[label] || len(bytes.TrimSpace(status)) != 0 {
			return errors.New("source changed during comparison; retained samples are not certified")
		}
	}
	finalDigest, err := fixtureIdentity(roots["baseline"], roots["candidate"], strings.Split(opts.Fixtures, ","))
	if err != nil {
		return err
	}
	if finalDigest != digest {
		return errors.New("fixtures changed during comparison")
	}
	stats, err := os.OpenFile(filepath.Join(out, "benchstat.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	result := taskrunner.Run(ctx, taskrunner.Request{Argv: []string{"go", "run", "golang.org/x/perf/cmd/benchstat@" + benchstatVersion, filepath.Join(out, "baseline.txt"), filepath.Join(out, "candidate.txt")}, Dir: roots["candidate"], Env: []string{"GOWORK=off"}, Timeout: opts.Timeout, Output: taskrunner.Stream, StreamOut: stats, StreamErr: stats})
	if err := errors.Join(result.Err, result.Cleanup.Err, result.AccountingErr, stats.Close()); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "comparison retained in %s (no automatic speedup verdict)\n", out)
	return nil
}

func probe(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
	return taskrunner.Output(ctx, taskrunner.Request{Argv: args, Dir: dir, Env: append(gitscope.Environ(), "GOWORK=off"), ClearEnv: true, Timeout: timeout})
}

func roundOrder(round int) []string {
	if round%2 == 1 {
		return []string{"candidate", "baseline"}
	}
	return []string{"baseline", "candidate"}
}
func sufficientSamples(samples int) bool { return samples >= 6 }

func validateSample(data []byte) error {
	if len(measurementNames(data)) > 0 {
		return nil
	}
	return errors.New("no benchmark measurements produced; refusing empty comparison")
}

func measurementNames(data []byte) []string {
	var names []string
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && strings.HasPrefix(fields[0], "Benchmark") && strings.HasSuffix(fields[3], "/op") {
			names = append(names, fields[0])
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func contained(root, name string) (string, error) {
	if filepath.IsAbs(name) || slices.Contains(strings.Split(filepath.ToSlash(name), "/"), "..") {
		return "", fmt.Errorf("path escapes root: %q", name)
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", name)
	}
	return path, nil
}

func fixtureIdentity(a, b string, names []string) (string, error) {
	hashes := make([]string, 0, 2)
	for _, root := range []string{a, b} {
		hash := sha256.New()
		var files []string
		for _, name := range names {
			if name == "" {
				return "", errors.New("fixture paths must be explicit")
			}
			path, err := contained(root, name)
			if err != nil {
				return "", err
			}
			if err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() && !entry.Type().IsRegular() {
					return errors.New("fixtures must be regular files, not symlinks/devices")
				}
				if !entry.IsDir() {
					files = append(files, path)
				}
				return nil
			}); err != nil {
				return "", err
			}
		}
		slices.Sort(files)
		for _, path := range slices.Compact(files) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			_, _ = hash.Write([]byte(filepath.ToSlash(rel) + "\x00"))
			_, _ = hash.Write(data)
			_, _ = hash.Write([]byte("\x00"))
		}
		hashes = append(hashes, hex.EncodeToString(hash.Sum(nil)))
	}
	if hashes[0] != hashes[1] {
		return "", errors.New("fixture contents differ between revisions")
	}
	return hashes[0], nil
}
