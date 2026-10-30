package processhost

// Reason is a bounded stable process-host outcome code (no paths/secrets).
//
// These constants are outcome codes, not sentinel errors: ReasonOK is a success
// code, so the Err prefix convention for error sentinels does not apply. The
// names follow the enum convention (type name prefix + value). Error() is
// implemented so reasons can be wrapped and matched with errors.Is.
//
//nolint:errname // outcome codes (incl. success), not error sentinels
type Reason string

func (r Reason) Error() string { return string(r) }

//nolint:errname // outcome codes (incl. success), not error sentinels
const (
	ReasonOK                    Reason = "ok"
	ReasonUnsupportedBinding    Reason = "unsupported_binding"
	ReasonUnsupportedChannel    Reason = "unsupported_channel"
	ReasonPeerRejected          Reason = "peer_rejected"
	ReasonStaleGeneration       Reason = "stale_generation"
	ReasonPIDReuse              Reason = "pid_reuse"
	ReasonLaunchFailed          Reason = "launch_failed"
	ReasonHandshakeFailed       Reason = "handshake_failed"
	ReasonConfigureBeforePeer   Reason = "configure_before_peer"
	ReasonEnvBootstrapRejected  Reason = "env_bootstrap_rejected"
	ReasonLoopbackRejected      Reason = "loopback_rejected"
	ReasonCookieAuthRejected    Reason = "cookie_auth_rejected"
	ReasonShuttingDown          Reason = "shutting_down"
	ReasonGenerationInvalidated Reason = "generation_invalidated"
	// ReasonConnectorOutputLimitExceeded reports a connector that outran the
	// host's diagnostic budget. The generation is terminated, not served.
	ReasonConnectorOutputLimitExceeded Reason = "connector_output_limit_exceeded"
	ReasonProcessModelViolation        Reason = "process_model_violation"
	ReasonMissingRuntime               Reason = "missing_runtime"
	ReasonArtifactRequired             Reason = "artifact_required"
	ReasonSubstitution                 Reason = "substitution"
)
