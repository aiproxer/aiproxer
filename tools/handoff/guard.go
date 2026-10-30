package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// guardCommand binds the gate to both its staged index and working contents.
// An explicit alternate index supports partial commits without inheriting other
// Git repository selectors into fixture subprocesses.
func guardCommand(ctx context.Context, repo, index string, argv []string, output io.Writer) error {
	if len(argv) == 0 {
		return errors.New("guard requires command argv")
	}
	if index != "" {
		var err error
		index, err = filepath.Abs(index)
		if err != nil {
			return err
		}
	}
	before, err := snapshotWithIndex(ctx, repo, index)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	cmd.Stdout = output
	cmd.Stderr = output
	runErr := cmd.Run()
	after, stateErr := snapshotWithIndex(ctx, repo, index)
	if stateErr != nil {
		return errors.Join(runErr, stateErr)
	}
	if before != after {
		return errors.Join(runErr, errors.New("staged/working source changed during gate; restage and reverify before committing"))
	}
	return runErr
}
