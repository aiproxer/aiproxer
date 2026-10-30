package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// First-party census + default-deny proofs for the host-owned multi-user
// approval registry (issue #701).
//
// The census tests below are static: they never start a connector process and
// never build a connector module. The generic host gate is proven once in
// internal/infra/runtimebundle; connector posture is proven here from the
// checked-in manifest/release metadata, compared against the distribution's own
// read-only approval API.

const multiUserPolicyFixtureDir = "internal/archtest/testdata/backend_multi_user_policy"

// censusExportFixture is the negative-fixture shape for one synthetic first-party
// export. Fixtures are the same shape the live census produces, so the same
// detectors run against both.
type censusExportFixture struct {
	Connector      string `json:"connector"`
	PluginID       string `json:"plugin_id"`
	Kind           string `json:"kind"`
	CredentialMode string `json:"credential_mode"`
	AccessScope    string `json:"access_scope"`
	ExecutionClass string `json:"execution_class"`
	ProcessSharing string `json:"process_sharing"`
}

func (f censusExportFixture) toExport() firstPartyExport {
	return firstPartyExport(f)
}

func liveCensus(t *testing.T) firstPartyCensus {
	t.Helper()
	root := repoRoot(t)
	census, err := collectFirstPartyCensus(filepath.Join(root, filepath.FromSlash(firstPartyConnectorsDir)))
	if err != nil {
		t.Fatalf("first-party census: %v", err)
	}
	if census.Exports == nil {
		t.Fatal("census produced no exports; census must never be silently empty")
	}
	return census
}

func readExportFixture(t *testing.T, name string) firstPartyExport {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(multiUserPolicyFixtureDir), name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	var got censusExportFixture
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	if got.Kind == "" {
		t.Fatalf("fixture %s declares no kind", name)
	}
	return got.toExport()
}

// TestFirstPartyCensus_NoInvisibleFirstPartyExport proves the census sees every
// first-party connector export. A connector that cannot be enumerated is a
// silent hole in the security boundary, so this fails closed.
func TestFirstPartyCensus_NoInvisibleFirstPartyExport(t *testing.T) {
	t.Parallel()
	census := liveCensus(t)
	if len(census.Escapes) != 0 {
		t.Fatalf("first-party exports invisible to the census:\n  %s", strings.Join(census.Escapes, "\n  "))
	}
	if _, err := census.byKind(); err != nil {
		t.Fatal(err)
	}
	for _, export := range census.Exports {
		if export.AccessScope == "" || export.CredentialMode == "" || export.ExecutionClass == "" {
			t.Fatalf("%s: manifest export must declare access_scope, credential_mode and execution_class: %+v", export.Ref(), export)
		}
		if export.PluginID == "" {
			t.Fatalf("%s: manifest export has no plugin_id", export.Ref())
		}
		if export.ExecutionClass == classAgentRuntime && hostAllowsMultiUser(loadHostMultiUserDecision(), export) {
			t.Fatalf("actual manifest agent runtime %s must be denied under multi_user", export.Ref())
		}
	}
}

// TestFirstPartyCensus_PostureInvariantsHold freezes the first-party invariants:
// every agent runtime, every user-OAuth factory, and every factory the host lists
// as personal/subscription-auth must stay local_only, and no posture value may be
// unknown so a new connector cannot slip an unrecognized class past the census.
func TestFirstPartyCensus_PostureInvariantsHold(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	personalAuth := personalAuthExportKinds(host)
	for _, export := range liveCensus(t).Exports {
		if reason := firstPartyPostureViolation(export, personalAuth); reason != "" {
			t.Errorf("%s (%s): %s", export.Ref(), export.PluginID, reason)
		}
	}
}

// TestFirstPartyCensus_AgentRuntimeExportsAreCovered proves the census actually
// observes the sensitive agent-runtime family named in the issue; an empty set
// would make the invariant above vacuous.
func TestFirstPartyCensus_AgentRuntimeExportsAreCovered(t *testing.T) {
	t.Parallel()
	byKind, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{
		"acp",
		"agycliacp",
		"cursorcliacp",
		"geminicliacp",
		"claudeagentacp",
		"openai-codex",
		"openai-codex-app-server",
	} {
		export, ok := byKind[kind]
		if !ok {
			t.Errorf("first-party census does not see factory %q", kind)
			continue
		}
		if export.AccessScope != scopeLocalOnly {
			t.Errorf("%s: personal/agent-runtime factory must declare access_scope=%q, got %q", export.Ref(), scopeLocalOnly, export.AccessScope)
		}
		if kind != "openai-codex" && export.ExecutionClass != classAgentRuntime {
			t.Errorf("%s: must declare execution_class=%q, got %q", export.Ref(), classAgentRuntime, export.ExecutionClass)
		}
	}
}

// TestFirstPartyCensus_PersonalAuthKindsExistAndStayLocalOnly proves the host's
// personal/subscription-auth table names real connectors and that every one of
// them is `local_only` in its packaged manifest. A stale table entry would
// silently stop protecting a real connector.
func TestFirstPartyCensus_PersonalAuthKindsExistAndStayLocalOnly(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	if len(host.PersonalAuthKinds) == 0 {
		t.Fatal("host policy declares no personal-auth factory table; the local_only tripwire would be vacuous")
	}
	byKind, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range host.PersonalAuthKinds {
		export, ok := byKind[kind]
		if !ok {
			t.Errorf("host personal-auth table names %q, which is not a first-party connector export", kind)
			continue
		}
		if export.AccessScope != scopeLocalOnly {
			t.Errorf("%s: personal/subscription-auth factory must declare access_scope=%q, got %q", export.Ref(), scopeLocalOnly, export.AccessScope)
		}
		if export.CredentialMode != credentialOAuthUser {
			t.Errorf("%s: personal/subscription-auth factory must declare credential_mode=%q, got %q", export.Ref(), credentialOAuthUser, export.CredentialMode)
		}
	}
}

// TestFirstPartyCensus_ApprovalsAreCategorized proves every host approval is
// either an essential in-process built-in (catalog-verified, out of connector
// census scope) or a first-party connector export the census can verify. Nothing
// is assumed valid just because it is not a connector.
func TestFirstPartyCensus_ApprovalsAreCategorized(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	if len(host.ApprovedKinds) == 0 {
		t.Fatal("host approval registry is empty; the cross-check would be vacuous")
	}
	essential := 0
	connector := 0
	for _, kind := range host.ApprovedKinds {
		switch _, inCensus := census[kind]; {
		case host.isConnectorApproval(kind) && inCensus:
			connector++
		case !host.isConnectorApproval(kind) && standardplugins.IsEssentialBackendKind(kind):
			essential++
		default:
			t.Errorf("approved kind %q is neither an essential built-in nor a first-party connector export; the census cannot verify it", kind)
		}
	}
	if essential == 0 {
		t.Fatal("no essential built-in approval resolved through the actual catalog; the built-in/connector split is untested")
	}
	if connector == 0 {
		t.Fatal("no connector approval resolved; the connector cross-check is untested")
	}
}

// TestFirstPartyCensus_ApprovedKindsExistAndStayCompatible proves the privileged
// surface matches reality: no stale approval survives a rename/removal, and no
// approved factory drifts away from a multi-user-compatible posture.
func TestFirstPartyCensus_ApprovedKindsExistAndStayCompatible(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	if stale := staleApprovals(host, census); len(stale) != 0 {
		t.Errorf("host multi-user approvals name connector factories that no longer exist (remove the approval with the factory):\n  %s", strings.Join(stale, "\n  "))
	}
	if broken := brokenApprovals(host, census); len(broken) != 0 {
		t.Errorf("host multi-user approvals are incompatible with current manifest posture:\n  %s", strings.Join(broken, "\n  "))
	}
}

// TestFirstPartyCensus_ApprovalsExcludePersonalAndAgentFactories keeps
// user-subscription and local agent-runtime factories out of the shared-use
// approval set even if a later edit forgets the posture rule.
func TestFirstPartyCensus_ApprovalsExcludePersonalAndAgentFactories(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	personal := personalAuthExportKinds(host)
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range host.ApprovedKinds {
		if why, named := personal[kind]; named {
			t.Errorf("personal-auth factory %q (%s) must never be approved for multi_user", kind, why)
		}
		if export, ok := census[kind]; ok && export.ExecutionClass == classAgentRuntime {
			t.Errorf("agent runtime %q must not be approved for multi_user without a reviewed shared-service security design", kind)
		}
	}
}

// TestFirstPartyCensus_EligibilityFollowsTheRestrictiveSource proves the live
// census composes the same way the host gate does: an approved remote-inference
// connector stays eligible, and a local-only or personal/subscription-auth
// connector is denied regardless of how permissively it declares itself.
func TestFirstPartyCensus_EligibilityFollowsTheRestrictiveSource(t *testing.T) {
	t.Parallel()
	host := loadHostMultiUserDecision()
	census, err := liveCensus(t).byKind()
	if err != nil {
		t.Fatal(err)
	}
	eligible := 0
	for kind, export := range census {
		if !hostAllowsMultiUser(host, export) {
			continue
		}
		eligible++
		if _, approved := host.Approvals[kind]; !approved {
			t.Errorf("%s is eligible without a host approval; absence must deny", export.Ref())
		}
		if export.AccessScope != scopeAny {
			t.Errorf("%s is eligible with access_scope %q; only access_scope %q may be multi-user eligible", export.Ref(), export.AccessScope, scopeAny)
		}
	}
	if eligible == 0 {
		t.Fatal("no first-party connector is multi-user eligible; the approval cross-check would be vacuous")
	}

	mustDeny := append([]string(nil), host.PersonalAuthKinds...)
	mustDeny = append(mustDeny, "llamacpp", "lmstudio", "vllm")
	mustDeny = append(mustDeny, slices.Sorted(mapsKeys(provisionalSIWCFactoryKinds))...)
	for _, kind := range mustDeny {
		export, ok := census[kind]
		if !ok {
			if _, forwardLooking := provisionalSIWCFactoryKinds[kind]; forwardLooking {
				continue
			}
			t.Errorf("census does not see first-party factory %q", kind)
			continue
		}
		if hostAllowsMultiUser(host, export) {
			t.Errorf("%s must not be multi-user eligible: local inference servers and personal/subscription-auth connectors are denied", export.Ref())
		}
	}
}

func mapsKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
