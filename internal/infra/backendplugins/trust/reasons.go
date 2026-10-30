package trust

// Reason is a bounded stable trust failure/success code (no paths/secrets).
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
	ReasonOK                  Reason = "ok"
	ReasonPathEscape          Reason = "path_escape"
	ReasonNotRegular          Reason = "not_regular_file"
	ReasonNotExecutableType   Reason = "not_native_executable"
	ReasonUnsupportedPlatform Reason = "unsupported_platform"
	ReasonDigestMismatch      Reason = "digest_mismatch"
	ReasonSubstitution        Reason = "substitution_detected"
	ReasonStagingUnsupported  Reason = "staging_unsupported"
	ReasonStagingFailed       Reason = "staging_failed"
	ReasonRootRequired        Reason = "trusted_root_required"
	ReasonSymlinkEscape       Reason = "symlink_escape"
	ReasonOpenFailed          Reason = "open_failed"
	ReasonACLUnverified       Reason = "acl_unverified"
)
