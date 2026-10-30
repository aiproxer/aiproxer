package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// captureCommand runs explicit argv, saves real output/status, and refuses to
// attribute a check to source that changed during execution.
func captureCommand(ctx context.Context, repo, log, purpose string, argv []string, output, diagnostics io.Writer) error {
	if !slices.Contains([]string{"red", "verification", "baseline"}, purpose) {
		return errors.New("unknown evidence purpose")
	}
	log, err := filepath.Abs(log)
	if err != nil {
		return err
	}
	// Invalidate a previous receipt before overwriting its log, including when
	// the command fails or changes source before a replacement can be written.
	if err := os.Remove(log + ".record.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	before, err := snapshot(ctx, repo)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(log, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = repo
	cmd.Env = gitscope.Environ()
	cmd.Stdout = io.MultiWriter(file, diagnostics)
	cmd.Stderr = cmd.Stdout
	runErr := cmd.Run()
	closeErr := file.Close()
	after, stateErr := snapshot(ctx, repo)
	if err := errors.Join(closeErr, stateErr); err != nil {
		return err
	}
	if before != after {
		return errors.New("source changed during execution; retained log is not current-source evidence")
	}
	code := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			return runErr
		}
		code = exit.ExitCode()
	}
	digest, err := logDigest(log)
	if err != nil {
		return err
	}
	record := commandRecord{Source: after, Command: Command{Purpose: purpose, Argv: argv, ExitCode: &code, Evidence: log, Record: log + ".record.json"}, LogSHA256: digest}
	if err := writeJSONAtomic(log+".record.json", record); err != nil {
		return err
	}
	return errors.Join(json.NewEncoder(output).Encode(record), runErr)
}
