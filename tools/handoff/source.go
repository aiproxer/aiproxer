package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/internal/sourcestate"
)

type SourceState = sourcestate.State

func snapshot(ctx context.Context, repo string) (SourceState, error) {
	return sourcestate.Snapshot(ctx, repo)
}

func snapshotWithIndex(ctx context.Context, repo, index string) (SourceState, error) {
	return sourcestate.SnapshotWithIndex(ctx, repo, index)
}

// Fixture and manifest probes use the same repository environment isolation.
func sourceGit(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return sourceGitWithIndex(ctx, repo, "", args...)
}

func sourceGitWithIndex(ctx context.Context, repo, index string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	data, err := cmd.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("git %v: %w", args, err)
	}
	return data, nil
}
