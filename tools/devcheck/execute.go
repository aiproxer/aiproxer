package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

const commandTimeout = 30 * time.Minute

func withAnalyzerBudget(root string, command []string) []string {
	if runtime.GOOS == "windows" || len(command) == 0 || command[0] != "golangci-lint" {
		return command
	}
	return append([]string{"bash", filepath.Join(root, "scripts", "go-dev-guard.sh"), "--aip-resource-run"}, command...)
}

// execute delegates process-tree ownership to taskrunner. The parser is joined
// before returning, including malformed telemetry and interruption paths.
func execute(ctx context.Context, cmd *exec.Cmd, test bool, output, rawLog io.Writer) (testStats, error) {
	return executeObserved(ctx, cmd, test, output, rawLog, executionOptions{})
}

// executionOptions carries opt-in ownership behavior for one command. The zero
// value preserves the existing executor contract.
type executionOptions struct {
	// observe folds each go test event on the parser goroutine.
	observe func(testEvent)
	// killGroupOnExit kills the command's process group after its leader exits,
	// so descendants cannot outlive a leader that failed on its own.
	killGroupOnExit bool
}

// executeObserved is execute with optional per-event observation and process
// ownership. The observer runs on the parser goroutine and has returned before
// executeObserved does, so callers may read what they collected once it returns.
func executeObserved(ctx context.Context, cmd *exec.Cmd, test bool, output, rawLog io.Writer, options executionOptions) (testStats, error) {
	if err := ctx.Err(); err != nil {
		return testStats{}, err
	}
	timeout := commandTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return testStats{}, context.DeadlineExceeded
		}
	}
	request := taskrunner.Request{
		Argv: cmd.Args, Dir: cmd.Dir, Env: cmd.Env, ClearEnv: cmd.Env != nil,
		Timeout: timeout, Output: taskrunner.Stream, StreamOut: output, StreamErr: cmd.Stderr,
		KillGroupOnExit: options.killGroupOnExit,
	}
	var stats testStats
	var parseErr error
	var reader *io.PipeReader
	var writer *io.PipeWriter
	var parsed chan struct{}
	if test {
		reader, writer = io.Pipe()
		parsed = make(chan struct{})
		request.StreamOut = writer
		if rawLog != nil {
			request.StreamOut = io.MultiWriter(rawLog, writer)
		}
		go func() {
			defer close(parsed)
			defer func() { _ = reader.Close() }()
			stats, parseErr = consumeTestEvents(reader, output, options.observe)
			if parseErr != nil {
				_, _ = io.Copy(io.Discard, reader)
			}
		}()
	}
	result := taskrunner.Run(ctx, request)
	if test {
		_ = writer.Close()
		<-parsed
	}
	return stats, errors.Join(parseErr, result.Err, result.Cleanup.Err, result.AccountingErr)
}

func commandOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	_, err := execute(ctx, cmd, false, &output, nil)
	return output.Bytes(), err
}
