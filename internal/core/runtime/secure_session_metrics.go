package runtime

// SecureSessionMetrics receives secure-session observability signals (optional; nil skips all).
type SecureSessionMetrics interface {
	ObserveBeginTurnNew()
	ObserveBeginTurnResume()
	// ObserveBeginTurnDenied increments when BeginTurn fails (code is aipapi SessionDenialCode string or "unknown").
	ObserveBeginTurnDenied(code string)
	ObserveStorageUnavailable()
	ObserveActivityTouch(seconds float64)
	ObserveRecorderClientTurnFailed(mandatory bool)
	ObserveRecorderStreamEventFailed(committed bool, mandatory bool)
}
