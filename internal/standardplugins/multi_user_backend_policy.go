package standardplugins

import (
	"slices"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/alibabatokenplanintl"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

// This file is the host/distribution-owned multi-user backend approval policy.
//
// It is the fail-closed authority for one security boundary: which backend factory
// kinds the standard distribution may enable in an `access.mode: multi_user`
// deployment.
//
// # Why a host-owned registry exists
//
// Connector-owned metadata (manifest `access_scope` / `credential_mode`, or a
// runtime `Describe()` descriptor) is necessary but NOT sufficient to authorize
// shared use. A brand-new connector can be misclassified, can copy the wrong
// template, or can falsely declare `access_scope: any` + `credential_mode: static`
// + `execution_class: inference` while actually being an agent runtime bound to one
// developer's personal subscription. Effective eligibility therefore combines the
// most restrictive source:
//
//	effective_multi_user_eligibility(factory) =
//	    connector_posture_allows_multi_user
//	AND host_distribution_policy_approves(factory)
//	AND credential/security checks pass
//
// Absence from [MultiUserBackendPolicy] is denial. Ordinary connector work inside
// `connectors/<name>` therefore cannot grant shared-use privilege: promoting a
// factory requires an explicit change to this file, outside the connector subtree.
// Treat edits here as a privileged security-review event (this file is a
// CODEOWNERS / branch-protection candidate).
//
// The generic runtime decision itself lives in
// internal/infra/runtimebundle/security_policy.go, which consults this policy
// before any backend factory construction, connector Configure, or child-process
// launch. There is deliberately no config knob and no per-connector override.

// MultiUserCredentialClass records who owns the credential an approved factory uses
// in a shared deployment. It is the machine-readable half of an approval rationale.
type MultiUserCredentialClass string

const (
	// MultiUserCredentialOperatorStatic is an operator-configured static credential
	// such as a provider API key: it belongs to the deployment operator, not to any
	// individual downstream principal.
	MultiUserCredentialOperatorStatic MultiUserCredentialClass = "operator_static"
	// MultiUserCredentialWorkload is workload identity from the local runtime
	// environment (cloud IAM role, instance metadata, ...).
	MultiUserCredentialWorkload MultiUserCredentialClass = "workload"
	// MultiUserCredentialServiceAccount is a provider service-account credential.
	MultiUserCredentialServiceAccount MultiUserCredentialClass = "service_account"
	// MultiUserCredentialNone marks a deterministic backend that uses no upstream
	// credential at all.
	MultiUserCredentialNone MultiUserCredentialClass = "none"
)

// MultiUserExecutionLocation records where an approved factory runs its inference.
type MultiUserExecutionLocation string

const (
	// MultiUserExecutionRemoteProvider is a remote provider HTTP inference adapter.
	MultiUserExecutionRemoteProvider MultiUserExecutionLocation = "remote_provider"
	// MultiUserExecutionLocalProcess runs a user-controlled inference process on the
	// host, reading user-local model state or trust material. No approval may use this
	// location.
	MultiUserExecutionLocalProcess MultiUserExecutionLocation = "local_process"
	// MultiUserExecutionDeterministicLocalStub is the synthetic local-stub connector.
	// It runs as a local process but is deterministic: it uses no upstream credential,
	// reads no user-local model state, no personal OAuth material, and no local trust
	// boundary, so it crosses no credential or isolation boundary a shared deployment
	// must protect. It is approved so the shipped single-user AND multi-user example
	// configurations (for example config/examples/secrets-guard-block-multi-user.yaml)
	// remain valid shared deployments.
	MultiUserExecutionDeterministicLocalStub MultiUserExecutionLocation = "deterministic_local_stub"
)

// MultiUserBackendApproval is one typed entry of the host-owned approval policy.
// A factory without an entry is denied in multi_user; there is no boolean-only
// entry and no implicit approval path.
type MultiUserBackendApproval struct {
	// FactoryKind is the registered backend factory kind (plugin config `kind:`).
	FactoryKind string
	// CredentialClass records credential ownership for the shared deployment.
	CredentialClass MultiUserCredentialClass
	// ExecutionLocation records remote provider versus local process execution.
	ExecutionLocation MultiUserExecutionLocation
	// Reference is a short human-readable approval rationale / evidence pointer.
	Reference string
}

// MultiUserBackendPolicy is an immutable, enumerable multi-user approval policy.
// The zero value denies everything. Instances are produced only by
// [HostMultiUserBackendPolicy]; there is no mutation API, no public setter, and no
// connector-supplied variant.
type MultiUserBackendPolicy struct {
	approvals map[string]MultiUserBackendApproval
}

// HostMultiUserBackendPolicy returns the standard distribution's approval policy.
// It is process-global authority, not process-global mutable state: callers only
// ever receive a value they cannot change.
func HostMultiUserBackendPolicy() MultiUserBackendPolicy { return hostMultiUserBackendPolicy() }

var hostMultiUserBackendPolicy = sync.OnceValue(func() MultiUserBackendPolicy {
	approvals := multiUserBackendApprovals()
	byKind := make(map[string]MultiUserBackendApproval, len(approvals))
	for _, a := range approvals {
		if _, dup := byKind[a.FactoryKind]; dup {
			// A duplicate approval would make the table ambiguous. The policy is
			// root-owned compile-time data, so this can only be an authoring bug.
			panic("standardplugins: duplicate multi-user backend approval for " + a.FactoryKind)
		}
		byKind[a.FactoryKind] = a
	}
	return MultiUserBackendPolicy{approvals: byKind}
})

// Lookup reports the typed approval for factoryKind, if the host distribution
// approved it for shared multi-user operation.
func (p MultiUserBackendPolicy) Lookup(factoryKind string) (MultiUserBackendApproval, bool) {
	a, ok := p.approvals[strings.TrimSpace(factoryKind)]
	return a, ok
}

// IsApproved reports whether factoryKind is approved for multi_user. Unknown
// factories are denied.
func (p MultiUserBackendPolicy) IsApproved(factoryKind string) bool {
	_, ok := p.Lookup(factoryKind)
	return ok
}

// Approvals returns the full approval table sorted by factory kind. The returned
// slice is a fresh copy; mutating it cannot affect the policy.
func (p MultiUserBackendPolicy) Approvals() []MultiUserBackendApproval {
	out := make([]MultiUserBackendApproval, 0, len(p.approvals))
	for _, a := range p.approvals {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b MultiUserBackendApproval) int {
		return strings.Compare(a.FactoryKind, b.FactoryKind)
	})
	return out
}

// personalAuthFactoryKinds is the defense-in-depth table of first-party factories
// whose upstream credential represents one human user's subscription or personal
// identity rather than an operator/workload credential.
//
// It is documentation plus a regression tripwire, not the enforcement mechanism:
// these kinds must stay absent from multiUserBackendApprovals, and their packaged
// manifests must keep declaring access_scope: local_only together with
// credential_mode: oauth_user, because each effective credential is a user OAuth
// session. That pairing is what the table exists to catch regressing: a
// reclassification to static or none (or a widening of access_scope) is precisely
// the accidental reclassification this table guards, which is why the host approval
// registry - not connector metadata - remains the boundary.
var personalAuthFactoryKinds = []string{
	// GitLab Duo uses a user OAuth session (connector-support/oauthcred).
	"gitlab-duo",
	// MiniMax subscription OAuth via PKCE/user-code.
	"minimax-oauth",
	// Nous Portal scoped OAuth JWT session loaded from an operator token file.
	"nous-portal",
	// Qwen Portal subscription OAuth via PKCE.
	"qwen-oauth",
	// xAI subscription OAuth via OIDC.
	"xai-oauth",
}

// PersonalAuthFactoryKinds returns the sorted personal/subscription-auth factory
// table. It is read-only enumeration for architecture tests and diagnostics.
func PersonalAuthFactoryKinds() []string {
	return slices.Clone(personalAuthFactoryKinds)
}

// multiUserBackendApprovals is the seeded approval table.
//
// Scope discipline for future edits:
//
//   - Approve only remote-provider inference adapters whose credential is owned by
//     the deployment operator (static API key, workload identity) or which use no
//     upstream credential at all.
//   - Never approve agent runtimes, local process inference servers, or any factory
//     that consumes a human user's subscription/personal credential.
//   - Never approve on the strength of a connector's own manifest declaration: a new
//     connector is denied by default, so an entry here IS the privilege grant.
//
// Changing this table is the only way to make a factory multi-user capable, and it
// must be a separate, explicitly reviewed change outside the connector subtree.
func multiUserBackendApprovals() []MultiUserBackendApproval {
	const (
		operatorAPIKey = "operator API key; remote provider HTTP inference"
		workloadID     = "workload identity; remote provider HTTP inference"
	)
	return []MultiUserBackendApproval{
		// Essential built-in backend table. Every entry is a remote provider adapter
		// driven by operator configuration, which is why current shared deployments
		// remain valid.
		{FactoryKind: openairesponses.ID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: openailegacy.ID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: anthropic.ID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: alibabatokenplanintl.ID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: gemini.ID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: bedrock.ID, CredentialClass: MultiUserCredentialWorkload, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: workloadID},
		// Generic compatible-provider modes: operator-configured base_url + api_key.
		{FactoryKind: CustomOpenAILegacyCompatibleID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: CustomOpenAIResponsesCompatibleID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: CustomAnthropicCompatibleID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: CustomOpenResponsesCompatibleID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: CustomSystemOneCompatibleID, CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: "operator environment bearer key; stateless remote System One evaluation; explicitly approved shared use"},

		// First-party hosted connectors: operator API key over remote HTTPS.
		{FactoryKind: "azure-openai", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "cloudflare", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "cohere", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "commandcode-anthropic", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "commandcode-openai", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "databricks-ai", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "huggingface", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "infomaniak-ai", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "nvidia", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "oci-generative-ai", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "ollama-cloud", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "opencode-go", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "opencode-zen", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "openrouter", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "replicate", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "sagemaker", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "sapaicore", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "snowflake-cortex", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "vertex", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},
		{FactoryKind: "watsonx", CredentialClass: MultiUserCredentialOperatorStatic, ExecutionLocation: MultiUserExecutionRemoteProvider, Reference: operatorAPIKey},

		// Deterministic synthetic stub. It is a local process, but it reads no
		// user-local credential, model state, or trust material, so it crosses no
		// boundary that a shared deployment must protect. Shipped example
		// configurations enable it under access.mode: multi_user, so denying it would
		// invalidate a currently valid shared deployment.
		{FactoryKind: "local-stub", CredentialClass: MultiUserCredentialNone, ExecutionLocation: MultiUserExecutionDeterministicLocalStub, Reference: "deterministic stub; no upstream credential and no user-local trust material; used by shipped single-user and multi_user examples"},
	}
}
