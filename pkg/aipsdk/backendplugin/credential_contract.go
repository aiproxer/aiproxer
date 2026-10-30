package backendplugin

import (
	"fmt"
	"regexp"
	"strings"
)

// CredentialContractVersion is the only accepted static-key contract version.
// The contract is versioned independently of the plugin transport protocol:
// ordered credentials travel over the existing authenticated
// ConfigureRequest.Secrets bundle, so no protobuf/Configure RPC change is
// needed to introduce or validate it.
const CredentialContractVersion = 2

// CredentialClass is the closed static-credential class for one export.
// api_key and api_token are bearer credentials eligible for ordered,
// quota-aware selection; static_nonrotatable marks authenticating key material
// (subscription tokens, service-key exchanges, signing key pairs) that must
// never be pooled or rotated across identities.
type CredentialClass string

const (
	CredentialClassAPIKey             CredentialClass = "api_key"
	CredentialClassAPIToken           CredentialClass = "api_token"
	CredentialClassStaticNonrotatable CredentialClass = "static_nonrotatable"
)

// CredentialSource is the closed approved origin of static credential values.
// host_env is the sole approved source: the host captures approved host
// process environment values and delivers them over the authenticated
// Configure channel. File, YAML, dotenv, child-environment and launcher
// fallbacks are never valid sources.
type CredentialSource string

const (
	CredentialSourceHostEnv CredentialSource = "host_env"
)

// CredentialSlot is the closed typed secret-slot name used on
// ConfigureRequest.Secrets. Connector-specific aliases (token, pat, api-key,
// access_token, service_key, ...) are normalized to one of these canonical
// names before plugin delivery; map iteration order is never meaningful.
type CredentialSlot string

const (
	CredentialSlotAPIKey   CredentialSlot = "api_key"
	CredentialSlotAPIToken CredentialSlot = "api_token"
)

// CredentialSelection is the closed key-selection mode for one export,
// constrained by inference/agent and side-effect safety. quota_pre_output is
// ordered quota-aware selection before any visible output or effect;
// session_admission selects once on safe agent-session admission and never
// replays after side effects; no_rotation forbids automatic rotation.
type CredentialSelection string

const (
	CredentialSelectionQuotaPreOutput   CredentialSelection = "quota_pre_output"
	CredentialSelectionSessionAdmission CredentialSelection = "session_admission"
	CredentialSelectionNoRotation       CredentialSelection = "no_rotation"
)

// CredentialContract is the versioned nonsecret static-key registration for
// one factory export. It carries namespace identity only (provider
// environment root, slot shape, selection mode, quota domain) and never
// secret values, so it is safe to log, embed in manifests and compare.
type CredentialContract struct {
	Version     uint32              `json:"version"`
	Class       CredentialClass     `json:"class"`
	Source      CredentialSource    `json:"source"`
	EnvRoot     string              `json:"env_root"`
	Slot        CredentialSlot      `json:"slot"`
	Selection   CredentialSelection `json:"selection"`
	QuotaDomain string              `json:"quota_domain"`
}

var (
	credentialEnvRoot = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	credentialDomain  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	numberedSuffix    = regexp.MustCompile(`_[0-9]+$`)
)

// Validate reports whether the contract is a well-formed v2 registration.
// Errors name only nonsecret identifiers (never secret values: the contract
// cannot hold any by construction).
func (c CredentialContract) Validate() error {
	if c.Version != CredentialContractVersion {
		return fmt.Errorf("%w: unsupported contract version %d", ErrInvalidCredentialContract, c.Version)
	}
	switch c.Class {
	case CredentialClassAPIKey, CredentialClassAPIToken, CredentialClassStaticNonrotatable:
	default:
		return fmt.Errorf("%w: unknown class %q", ErrInvalidCredentialContract, c.Class)
	}
	if c.Source != CredentialSourceHostEnv {
		return fmt.Errorf("%w: unknown source %q", ErrInvalidCredentialContract, c.Source)
	}
	if !credentialEnvRoot.MatchString(c.EnvRoot) || numberedSuffix.MatchString(c.EnvRoot) {
		return fmt.Errorf("%w: invalid env_root %q", ErrInvalidCredentialContract, c.EnvRoot)
	}
	switch c.Slot {
	case CredentialSlotAPIKey, CredentialSlotAPIToken:
	default:
		return fmt.Errorf("%w: unknown slot %q", ErrInvalidCredentialContract, c.Slot)
	}
	switch c.Selection {
	case CredentialSelectionQuotaPreOutput, CredentialSelectionSessionAdmission, CredentialSelectionNoRotation:
	default:
		return fmt.Errorf("%w: unknown selection %q", ErrInvalidCredentialContract, c.Selection)
	}
	if !credentialDomain.MatchString(strings.TrimSpace(c.QuotaDomain)) {
		return fmt.Errorf("%w: invalid quota_domain %q", ErrInvalidCredentialContract, c.QuotaDomain)
	}
	return nil
}
