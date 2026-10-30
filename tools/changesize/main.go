package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func main() {
	os.Exit(runWithOutput(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stderr io.Writer, getenv func(string) string) int {
	return runWithOutput(args, io.Discard, stderr, getenv)
}

func runWithOutput(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("changesize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "git repository root")
	staged := fs.Bool("staged", false, "count staged index paths (pre-commit)")
	base := fs.String("base", "", "diff range base revision")
	head := fs.String("head", "", "diff range head revision")
	limit := fs.Int("limit", DefaultLimit, "maximum modified Go files")
	report := fs.Bool("report", false, "report delivery metrics as JSON without enforcing advisory budgets")
	consumer := fs.String("consumer", "", "declared first consumer or dependency; declaration is not certification")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "usage: changesize [--staged | --base <rev> --head <rev>] [--limit %d] [--repo <dir>]\n", DefaultLimit)
		return 2
	}
	if *limit < 1 {
		_, _ = fmt.Fprintln(stderr, "check-change-size: --limit must be >= 1")
		return 2
	}
	if *report {
		if *staged || *limit != DefaultLimit {
			_, _ = fmt.Fprintln(stderr, "delivery report uses --base and optional --head; no --staged or custom --limit")
			return 2
		}
		if err := writeDeliveryReport(*repo, *base, *head, *consumer, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		return 0
	}
	if *consumer != "" {
		_, _ = fmt.Fprintln(stderr, "--consumer requires --report")
		return 2
	}

	useStaged := *staged
	if !useStaged && *base == "" && *head == "" {
		useStaged = true
	}
	if useStaged && (*base != "" || *head != "") {
		_, _ = fmt.Fprintln(stderr, "check-change-size: use --staged or --base/--head, not both")
		return 2
	}
	if !useStaged && (*base == "" || *head == "") {
		_, _ = fmt.Fprintln(stderr, "check-change-size: --base and --head are required together")
		return 2
	}

	var gitArgs []string
	if useStaged {
		gitArgs = []string{"diff", "--cached", "--name-only", "--diff-filter=ACDMRT", "-z"}
	} else {
		gitArgs = []string{"diff", "--name-only", "--diff-filter=ACDMRT", "-z", *base + "..." + *head}
	}
	out, err := gitOutput(*repo, gitArgs...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "check-change-size: git diff: %v\n", err)
		return 2
	}
	count := uniqueGoPathCount(splitGitNames(out))

	envOverride := truthy(getenv(OverrideEnv))
	gitOverride := gitConfigBool(*repo, OverrideGitConfig)
	override := envOverride || gitOverride

	if allowed(count, *limit, override) {
		if count > *limit && override {
			reason := OverrideGitConfig
			if envOverride {
				reason = OverrideEnv
			}
			_, _ = fmt.Fprintf(stderr, "check-change-size: %d modified Go files exceed the %d-file limit; proceeding because %s is set.\n", count, *limit, reason)
		}
		return 0
	}

	_, _ = fmt.Fprintf(stderr, "check-change-size: %d modified Go files exceed the %d-file limit.\n", count, *limit)
	_, _ = fmt.Fprintf(stderr, "Split the change into smaller reviewable PRs/commits, or set %s=1 or `git config %s true` (admin override).\n", OverrideEnv, OverrideGitConfig)
	return 1
}

func gitOutput(repo string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, err
	}
	return out, nil
}

func gitConfigBool(repo, key string) bool {
	out, err := gitOutput(repo, "config", "--bool", "--get", key)
	if err != nil {
		return false
	}
	return truthy(string(out))
}
