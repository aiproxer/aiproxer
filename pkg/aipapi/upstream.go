package aipapi

import (
	"errors"
	"fmt"
)

// ErrRecoverablePreOutput is a stable sentinel for upstream failures that the core
// may swallow and retry on another route candidate before client-visible output begins.
var ErrRecoverablePreOutput = errors.New("aipapi: recoverable pre-output upstream failure")

// OutputPhase classifies whether visible output had started when the failure occurred.
type OutputPhase string

const (
	PhasePreOutput  OutputPhase = "pre_output"
	PhasePostOutput OutputPhase = "post_output"
)

// UpstreamFailureError is a structured upstream error for orchestration (executor, diagnostics).
type UpstreamFailureError struct {
	Phase        OutputPhase
	Recoverable  bool
	Reason       string
	CandidateKey string
}

func (e *UpstreamFailureError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	if e.Recoverable && e.Phase == PhasePreOutput {
		return "upstream failure (recoverable, pre-output)"
	}
	return "upstream failure"
}

// Unwrap returns ErrRecoverablePreOutput when a pre-output failure is recoverable.
func (e *UpstreamFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Recoverable && e.Phase == PhasePreOutput {
		return ErrRecoverablePreOutput
	}
	return nil
}

type recoverablePreOutputError struct {
	cause error
}

func (e *recoverablePreOutputError) Error() string {
	return fmt.Sprintf("%v: %v", ErrRecoverablePreOutput, e.cause)
}

func (e *recoverablePreOutputError) Unwrap() []error {
	return []error{ErrRecoverablePreOutput, e.cause}
}

func RecoverablePreOutputError(err error) error {
	if err == nil {
		return nil
	}
	return &recoverablePreOutputError{cause: err}
}

// IsRecoverablePreOutput reports whether err should allow another backend attempt
// before client-visible output has been committed for the active attempt.
func IsRecoverablePreOutput(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRecoverablePreOutput) {
		return true
	}
	var uf *UpstreamFailureError
	return errors.As(err, &uf) && uf.Recoverable && uf.Phase == PhasePreOutput
}
