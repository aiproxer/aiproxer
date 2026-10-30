package catalog

// State is a bounded inspect-safe artifact/export state.
type State string

const (
	StateBuiltin        State = "builtin"
	StateDiscovered     State = "discovered"
	StateIncompatible   State = "incompatible"
	StateInvalid        State = "invalid"
	StateUntrusted      State = "untrusted"
	StateDigestMismatch State = "digest_mismatch"
	StateConflict       State = "conflict"
	StateConfigured     State = "configured"
	StateActive         State = "active"
	StateFailed         State = "failed"
	StateStopped        State = "stopped"
)

// Reason is a bounded diagnostic code without full paths or secrets.
type Reason string

const (
	ReasonOK                   Reason = "ok"
	ReasonDuplicatePluginID    Reason = "duplicate_plugin_id"
	ReasonDuplicateExportKind  Reason = "duplicate_export_kind"
	ReasonBuiltinCollision     Reason = "builtin_collision"
	ReasonProtocolIncompatible Reason = "protocol_incompatible"
	ReasonInvalidUnused        Reason = "invalid_unused"
	ReasonEnabledMissing       Reason = "enabled_missing"
	ReasonEnabledInvalid       Reason = "enabled_invalid"
	ReasonUntrusted            Reason = "untrusted"
	ReasonDigestMismatch       Reason = "digest_mismatch"
)
