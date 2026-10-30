package observers

// ignoreBestEffortRecorderErr is the explicit fail-open sink for best-effort
// control-plane recorder errors. Best-effort recording must never change
// observer/decorator behavior or surface errors to callers; the
// [controlplane.RecorderService] owns status degradation internally.
//
// This intentionally swallows every recorder error, including
// [controlplane.ErrDisabled] (capability off -> no recording, no degradation)
// and [controlplane.ErrUnsafeEvidence] (normalizer produced unsafe evidence ->
// the recorder does not degrade and the observer must stay fail-open). The
// helper centralizes the intentional swallow so call sites do not duplicate
// `_ = err` no-op branches. It does not log.
func ignoreBestEffortRecorderErr(err error) {
	_ = err
}
