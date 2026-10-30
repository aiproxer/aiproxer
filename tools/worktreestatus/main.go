// Command worktreestatus prints a read-only overview of every registered
// worktree: branch, HEAD, dirtiness, ahead/behind against a base ref, and
// HEAD date. It performs no mutation and never touches branches or remotes.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

type worktree struct {
	path   string
	branch string
	head   string
	locked bool
}

func main() {
	base := flag.String("base", "origin/main", "ref for ahead/behind counts")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := status(ctx, *base, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	data, err := taskrunner.Output(ctx, taskrunner.Request{Argv: append([]string{"git"}, args...), Dir: dir, Env: gitscope.Environ(), ClearEnv: true, Timeout: time.Minute})
	return string(data), err
}

// parsePorcelain reads `git worktree list --porcelain` output. Records are
// blank-line separated; branch lines are absent for bare entries and replaced
// by a bare `detached` marker for detached checkouts.
func parsePorcelain(out string) []worktree {
	var all []worktree
	cur := worktree{}
	locked := false
	flush := func() {
		if cur.path != "" {
			if locked {
				cur.branch += " [locked]"
			}
			all = append(all, cur)
		}
		cur = worktree{}
		locked = false
	}
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "HEAD "):
			cur.head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.branch = "(detached)"
		case line == "bare":
			cur.branch = "(bare)"
		case strings.HasPrefix(line, "locked"):
			locked = true
		}
	}
	flush()
	return all
}

func cell(s string, err error) string {
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(s)
}

// row renders one worktree. Unreadable cells degrade to "?" so one broken
// worktree cannot hide the rest of the overview.
func row(ctx context.Context, wt worktree, base string, haveBase bool) []string {
	head, err := git(ctx, wt.path, "rev-parse", "--short", "HEAD")
	changes, statusErr := git(ctx, wt.path, "status", "--porcelain=v1", "--untracked-files=all")
	n := 0
	if statusErr == nil {
		for line := range strings.SplitSeq(changes, "\n") {
			if line != "" {
				n++
			}
		}
	}
	state := "clean"
	if statusErr != nil {
		state = "?"
	} else if n > 0 {
		state = fmt.Sprintf("dirty(%d)", n)
	}
	position := "-"
	if haveBase {
		counts, err := git(ctx, wt.path, "rev-list", "--left-right", "--count", "HEAD..."+base)
		if err == nil {
			if fields := strings.Fields(counts); len(fields) == 2 {
				position = fields[0] + "/" + fields[1]
			} else {
				position = "?"
			}
		} else {
			position = "?"
		}
	}
	date, dateErr := git(ctx, wt.path, "log", "-1", "--format=%cs")
	return []string{wt.path, wt.branch, cell(head, err), state, position, cell(date, dateErr)}
}

func status(ctx context.Context, base string, out io.Writer) error {
	return statusIn(ctx, ".", base, out)
}

func statusIn(ctx context.Context, dir, base string, out io.Writer) error {
	repo, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repo = strings.TrimSpace(repo)
	listed, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	entries := parsePorcelain(listed)
	_, err = git(ctx, repo, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	haveBase := err == nil

	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "PATH\tBRANCH\tHEAD\tSTATUS\tAHEAD/BEHIND\tHEAD DATE"); err != nil {
		return err
	}
	for _, wt := range entries {
		if _, err := fmt.Fprintln(writer, strings.Join(row(ctx, wt, base, haveBase), "\t")); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if haveBase {
		_, err = fmt.Fprintf(out, "\n%d worktree(s); ahead/behind against %s\n", len(entries), base)
	} else {
		_, err = fmt.Fprintf(out, "\n%d worktree(s); base ref %s not found locally\n", len(entries), base)
	}
	return err
}
