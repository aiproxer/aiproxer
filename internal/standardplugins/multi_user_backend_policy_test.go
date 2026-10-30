package standardplugins_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestHostMultiUserBackendPolicy_defaultDeniesUnlistedFactories(t *testing.T) {
	t.Parallel()
	policy := standardplugins.HostMultiUserBackendPolicy()

	// A brand-new connector must not be able to grant itself multi-user
	// eligibility, no matter what it declares in its own manifest.
	for _, kind := range []string{
		"new-acp-agent-runtime",
		"totally-bogus-factory",
		"openai-codex-app-server",
		"acp",
		"minimax-oauth",
		"qwen-oauth",
		"xai-oauth",
		"nous-portal",
		"gitlab-duo",
		"llamacpp",
		"lmstudio",
		"vllm",
		"ollama",
		"openai-codex",
		"",
	} {
		if _, ok := policy.Lookup(kind); ok {
			t.Fatalf("factory %q must not be approved for multi_user", kind)
		}
		if policy.IsApproved(kind) {
			t.Fatalf("IsApproved(%q) must be false by default", kind)
		}
	}
}

func TestHostMultiUserBackendPolicy_seedsCurrentSharedDeployments(t *testing.T) {
	t.Parallel()
	policy := standardplugins.HostMultiUserBackendPolicy()

	// Every essential built-in backend kind is a remote provider adapter driven by
	// an operator API key or workload identity, so each current shared deployment
	// stays valid.
	for _, kind := range standardplugins.EssentialBackendKinds() {
		approval, ok := policy.Lookup(kind)
		if !ok {
			t.Fatalf("essential backend kind %q must stay approved for multi_user", kind)
		}
		if approval.ExecutionLocation != standardplugins.MultiUserExecutionRemoteProvider {
			t.Fatalf("essential backend kind %q must be remote_provider, got %q", kind, approval.ExecutionLocation)
		}
		switch approval.CredentialClass {
		case standardplugins.MultiUserCredentialOperatorStatic,
			standardplugins.MultiUserCredentialWorkload,
			standardplugins.MultiUserCredentialNone:
		default:
			t.Fatalf("essential backend kind %q has non-approvable credential class %q", kind, approval.CredentialClass)
		}
		if strings.TrimSpace(approval.Reference) == "" {
			t.Fatalf("approval for %q must record a rationale/reference", kind)
		}
	}

	// The deterministic stub is credential-less and reads no user-local trust
	// material, and shipped example configs enable it under access.mode: multi_user,
	// so it must stay valid. Its approval is explicit, never implicit.
	stub, ok := policy.Lookup("local-stub")
	if !ok {
		t.Fatal("deterministic local stub must stay approved for multi_user example deployments")
	}
	if stub.CredentialClass != standardplugins.MultiUserCredentialNone ||
		stub.ExecutionLocation != standardplugins.MultiUserExecutionDeterministicLocalStub {
		t.Fatalf("local-stub approval = %+v; want none/deterministic_local_stub", stub)
	}
}

func TestHostMultiUserBackendPolicy_neverApprovesLocalOrPersonalAuth(t *testing.T) {
	t.Parallel()
	policy := standardplugins.HostMultiUserBackendPolicy()
	approvals := policy.Approvals()

	for _, a := range approvals {
		if a.ExecutionLocation == standardplugins.MultiUserExecutionLocalProcess {
			t.Fatalf("approval %q must not be a local process backend", a.FactoryKind)
		}
		if slices.Contains(standardplugins.PersonalAuthFactoryKinds(), a.FactoryKind) {
			t.Fatalf("personal/subscription-auth factory %q must never be approved for multi_user", a.FactoryKind)
		}
	}
	for _, kind := range []string{"acp", "agycliacp", "cursorcliacp", "geminicliacp", "claudeagentacp"} {
		if policy.IsApproved(kind) {
			t.Fatalf("agent-runtime factory %q must not be approved", kind)
		}
	}
}

func TestHostMultiUserBackendPolicy_approvedSetIsWellFormed(t *testing.T) {
	t.Parallel()
	approvals := standardplugins.HostMultiUserBackendPolicy().Approvals()
	if len(approvals) == 0 {
		t.Fatal("approval policy must not be empty")
	}

	seen := map[string]struct{}{}
	for _, a := range approvals {
		if a.FactoryKind == "" || strings.TrimSpace(a.FactoryKind) != a.FactoryKind {
			t.Fatalf("approval has malformed factory kind %q", a.FactoryKind)
		}
		if _, dup := seen[a.FactoryKind]; dup {
			t.Fatalf("duplicate approval for %q", a.FactoryKind)
		}
		seen[a.FactoryKind] = struct{}{}
		if a.CredentialClass == "" {
			t.Fatalf("approval %q must declare a credential class", a.FactoryKind)
		}
		if a.ExecutionLocation == "" {
			t.Fatalf("approval %q must declare an execution location", a.FactoryKind)
		}
		if strings.TrimSpace(a.Reference) == "" {
			t.Fatalf("approval %q must record a rationale/reference", a.FactoryKind)
		}
	}
}

func TestHostMultiUserBackendPolicy_approvalsAreStableAndImmutable(t *testing.T) {
	t.Parallel()
	first := standardplugins.HostMultiUserBackendPolicy().Approvals()
	second := standardplugins.HostMultiUserBackendPolicy().Approvals()
	if !slices.Equal(first, second) {
		t.Fatal("approval enumeration must be stable across calls")
	}
	if !slices.IsSortedFunc(first, func(a, b standardplugins.MultiUserBackendApproval) int {
		return strings.Compare(a.FactoryKind, b.FactoryKind)
	}) {
		t.Fatalf("approval enumeration must be sorted by factory kind: %v", first)
	}

	// Mutating the returned slice must not be able to grant eligibility.
	first[0].FactoryKind = "attacker-injected"
	again := standardplugins.HostMultiUserBackendPolicy().Approvals()
	if slices.ContainsFunc(again, func(a standardplugins.MultiUserBackendApproval) bool {
		return a.FactoryKind == "attacker-injected"
	}) {
		t.Fatal("caller mutation leaked into the host policy")
	}
	if standardplugins.HostMultiUserBackendPolicy().IsApproved("attacker-injected") {
		t.Fatal("caller mutation granted multi-user approval")
	}
}

func TestHostMultiUserBackendPolicy_lookupMatchesEnumeration(t *testing.T) {
	t.Parallel()
	policy := standardplugins.HostMultiUserBackendPolicy()
	for _, a := range policy.Approvals() {
		got, ok := policy.Lookup(a.FactoryKind)
		if !ok || got != a {
			t.Fatalf("Lookup(%q) = %+v, %v; want %+v, true", a.FactoryKind, got, ok, a)
		}
	}
}

func TestPersonalAuthFactoryKinds_coversUserOAuthCredentialConnectors(t *testing.T) {
	t.Parallel()
	kinds := standardplugins.PersonalAuthFactoryKinds()
	if !slices.IsSorted(kinds) {
		t.Fatalf("personal-auth factory kinds must be sorted: %v", kinds)
	}
	if len(kinds) == 0 {
		t.Fatal("personal-auth table must not be empty")
	}
	for _, kind := range []string{"minimax-oauth", "qwen-oauth", "xai-oauth", "nous-portal", "gitlab-duo"} {
		if !slices.Contains(kinds, kind) {
			t.Fatalf("user-OAuth credential connector %q must stay listed as personal auth", kind)
		}
	}
	if slices.Contains(kinds, "openai-responses") {
		t.Fatal("operator API-key backend must not be listed as personal auth")
	}
}
