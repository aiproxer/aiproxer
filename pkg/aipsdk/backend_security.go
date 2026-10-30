package aipsdk

// BackendCredentialMode describes how a registered backend plugin obtains upstream credentials
// (startup metadata only; not plugin-private configuration values). [CredentialNone] marks
// adapters that do not use upstream credentials.
type BackendCredentialMode string

const (
	// CredentialStatic uses operator-configured static credentials such as API keys.
	CredentialStatic BackendCredentialMode = "static"
	// CredentialWorkload uses workload identity from the local runtime environment.
	CredentialWorkload BackendCredentialMode = "workload"
	// CredentialOAuthUser uses user-scoped OAuth credentials; eligibility is validated against access mode.
	CredentialOAuthUser BackendCredentialMode = "oauth_user"
	// CredentialNone means the backend does not use upstream credentials (deterministic local adapters).
	CredentialNone BackendCredentialMode = "none"
	// CredentialUnknown means the factory did not declare a credential posture; validation may treat this conservatively.
	CredentialUnknown BackendCredentialMode = "unknown"
)

// BackendAccessScope describes whether a backend is safe for shared multi-user
// deployments. Local-only backends may spawn local processes, read personal
// OAuth material, or otherwise depend on user-local trust boundaries.
type BackendAccessScope string

const (
	// BackendAccessAny means the backend may be enabled in single-user or multi-user mode,
	// subject to credential posture validation.
	//
	// This is the default applied when a backend is registered without an explicit
	// AccessScope. The default is safe for cloud/upstream HTTP backends but is UNSAFE for
	// process-spawning backends, backends that read personal OAuth material from the local
	// user context, or any backend that depends on a user-local trust boundary: such
	// backends MUST declare [BackendAccessLocalOnly] explicitly rather than relying on
	// this default, otherwise they would be permitted in multi-user deployments and bypass
	// the local-trust boundary. [internal/pluginreg] startup access-scope validation
	// rejects local-only backends in multi-user mode.
	BackendAccessAny BackendAccessScope = "any"
	// BackendAccessLocalOnly means the backend is only valid in single-user deployments.
	// Backends that spawn local subprocesses, read personal OAuth tokens from the user's
	// environment, or otherwise assume a single trusted local user MUST declare this
	// scope explicitly at registration time; do not rely on the [BackendAccessAny]
	// default for such backends.
	BackendAccessLocalOnly BackendAccessScope = "local_only"
)

// BackendSecurityProfile is stable startup metadata for backend credential posture. It is
// part of the public plugin registration contract and must not hold secret values.
type BackendSecurityProfile struct {
	CredentialMode BackendCredentialMode
	AccessScope    BackendAccessScope
}
