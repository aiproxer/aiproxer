package main

import (
	"errors"
	"os"

	"fmt"
)

// Pattern for mapping errors to exit codes.
func mainWithExitCodes() {
	if err := Execute(); err != nil {
		// With SilenceErrors enabled, this boundary prints the diagnostic once.
		fmt.Fprintln(os.Stderr, err)
		var exitErr *ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
