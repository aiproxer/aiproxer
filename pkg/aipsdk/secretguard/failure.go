package secretguard

// FailureMode selects how the secret-guard runner treats a non-nil error from Evaluate.
type FailureMode int

const (
	FailureModeUnspecified FailureMode = iota
	// FailOpen continues after recording a safe failure outcome.
	FailOpen
	// FailClosed stops the request and returns a policy failure.
	FailClosed
)
