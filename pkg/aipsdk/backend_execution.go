package aipsdk

import "errors"

// ErrInvalidBackendExecutionClass indicates an unrecognized non-empty execution class.
var ErrInvalidBackendExecutionClass = errors.New("aipsdk: invalid backend execution class")

// BackendExecutionClass represents the declared execution semantics of a backend factory.
type BackendExecutionClass string

const (
	// BackendExecutionUnknown indicates omitted or unclassified legacy metadata.
	BackendExecutionUnknown BackendExecutionClass = ""
	// BackendExecutionInference indicates an ordinary model-like inference service.
	BackendExecutionInference BackendExecutionClass = "inference"
	// BackendExecutionAgentRuntime indicates an agent or orchestration runtime with its own harness/state.
	BackendExecutionAgentRuntime BackendExecutionClass = "agent_runtime"
)

// BackendExecutionProfile holds stable startup execution semantics for backend registration.
type BackendExecutionProfile struct {
	Class BackendExecutionClass
}

// EffectiveClass returns the normalized execution class, treating empty as unknown.
func (p BackendExecutionProfile) EffectiveClass() BackendExecutionClass {
	switch p.Class {
	case BackendExecutionInference:
		return BackendExecutionInference
	case BackendExecutionAgentRuntime:
		return BackendExecutionAgentRuntime
	default:
		return BackendExecutionUnknown
	}
}

// Validate checks that the execution class is empty (unknown) or one of the recognized classes.
func (p BackendExecutionProfile) Validate() error {
	switch p.Class {
	case BackendExecutionUnknown, BackendExecutionInference, BackendExecutionAgentRuntime:
		return nil
	default:
		return ErrInvalidBackendExecutionClass
	}
}
