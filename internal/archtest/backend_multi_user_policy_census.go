package archtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// First-party multi-user security census (issue #701).
//
// The census keeps the host-owned multi-user approval registry from drifting away
// from what first-party connectors actually ship. It reads the two typed,
// checked-in artifacts that declare a connector's security posture:
//
//	connectors/<name>/release.yaml        (aip.connector.release/v1)
//	connectors/<name>/<manifest_template> (aiproxer.backendplugin.manifest/v1)
//
// Both are parsed into typed models, never scraped with ad-hoc text matching.
// Everything below is a pure function over the parsed census so the negative
// fixtures in the sibling test files drive the same detectors the live
// repository drives.

const (
	connectorReleaseSchema  = "aip.connector.release/v1"
	backendManifestSchema   = "aiproxer.backendplugin.manifest/v1"
	firstPartyConnectorsDir = "connectors"
	firstPartySupportDir    = "connector-support"
	hostPolicyFile          = "internal/standardplugins/multi_user_backend_policy.go"
	scopeAny                = "any"
	scopeLocalOnly          = "local_only"
	classInference          = "inference"
	classAgentRuntime       = "agent_runtime"
	credentialOAuthUser     = "oauth_user"
	credentialUnknown       = "unknown"
)

var (
	knownAccessScopes    = []string{scopeAny, scopeLocalOnly}
	knownCredentialModes = []string{"static", "workload", credentialOAuthUser, "none", credentialUnknown}
	knownExecutionClass  = []string{classInference, classAgentRuntime}
	// approvalKindFields are struct field names a typed approval row may use to
	// carry its factory kind.
	approvalKindFields = []string{"FactoryKind", "Kind", "Factory"}
)

// connectorRelease is the typed projection of a connector release descriptor.
// Only census-relevant fields are declared; the release schema is owned by
// tools/backendplugin and may gain fields without a census change.
type connectorRelease struct {
	Schema           string `yaml:"schema"`
	PluginID         string `yaml:"plugin_id"`
	FactoryKind      string `yaml:"factory_kind"`
	ManifestTemplate string `yaml:"manifest_template"`
}

// manifestTemplate is the typed projection of a packaged manifest template export
// list. Templates carry placeholder digest/build fields, so only export identity
// and security posture is projected here; real artifacts are validated by
// pkg/aipsdk/backendplugin/manifest at install time.
type manifestTemplate struct {
	Schema   string           `json:"schema"`
	PluginID string           `json:"plugin_id"`
	Exports  []manifestExport `json:"exports"`
}

type manifestExport struct {
	Kind           string `json:"kind"`
	CredentialMode string `json:"credential_mode"`
	AccessScope    string `json:"access_scope"`
	ProcessSharing string `json:"process_sharing"`
	ExecutionClass string `json:"execution_class"`
}

// firstPartyExport is one declared factory export of one first-party connector.
type firstPartyExport struct {
	Connector      string
	PluginID       string
	Kind           string
	CredentialMode string
	AccessScope    string
	ExecutionClass string
	ProcessSharing string
}

// Ref identifies a census entry in guard messages.
func (e firstPartyExport) Ref() string { return e.Connector + "/" + e.Kind }

// hostMultiUserDecision is the read-only host view the census compares against.
//
// The approval registry is consumed through the distribution's own enumerable
// API ([standardplugins.HostMultiUserBackendPolicy] and
// [standardplugins.PersonalAuthFactoryKinds]) instead of a syntax projection, so
// a constant-valued approval row can never be silently dropped, and the census
// cannot drift from the values the runtime gate actually reads.
//
// A [MultiUserBackendApproval] is never mutated here: [Approvals] hands back a
// fresh slice and the census only reads it.
type hostMultiUserDecision struct {
	// Approvals is the host approval table indexed by factory kind.
	Approvals map[string]standardplugins.MultiUserBackendApproval
	// ApprovedKinds is [Approvals]' key set, sorted for deterministic reporting.
	ApprovedKinds []string
	// EssentialKinds is the actual in-process built-in catalog
	// ([standardplugins.EssentialBackendKinds]). Essential approvals are in-process
	// registrations, not connector exports, so the connector census cannot and must
	// not cross-check them.
	EssentialKinds []string
	// PersonalAuthKinds is the host's own defense-in-depth
	// personal/subscription-auth table, so the census never restates it.
	PersonalAuthKinds []string
}

// loadHostMultiUserDecision reads the host-owned multi-user decision surfaces.
func loadHostMultiUserDecision() hostMultiUserDecision {
	approvals := standardplugins.HostMultiUserBackendPolicy().Approvals()
	decision := hostMultiUserDecision{
		Approvals:         make(map[string]standardplugins.MultiUserBackendApproval, len(approvals)),
		ApprovedKinds:     make([]string, 0, len(approvals)),
		EssentialKinds:    standardplugins.EssentialBackendKinds(),
		PersonalAuthKinds: standardplugins.PersonalAuthFactoryKinds(),
	}
	for _, approval := range approvals {
		decision.Approvals[approval.FactoryKind] = approval
		decision.ApprovedKinds = append(decision.ApprovedKinds, approval.FactoryKind)
	}
	slices.Sort(decision.ApprovedKinds)
	slices.Sort(decision.EssentialKinds)
	slices.Sort(decision.PersonalAuthKinds)
	return decision
}

// isConnectorApproval reports whether an approved kind is a first-party connector
// export rather than an essential in-process built-in. Essential kinds are outside
// the connector census by construction, so a stale approval check must not treat
// them as missing factories.
func (h hostMultiUserDecision) isConnectorApproval(kind string) bool {
	_, essential := essentialKindIndex(h.EssentialKinds)[kind]
	return !essential
}

func essentialKindIndex(kinds []string) map[string]struct{} {
	out := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		out[k] = struct{}{}
	}
	return out
}

// personalAuthExportKinds merges the host's personal/subscription-auth table with
// the provisional SIWC kinds this migration spec names. The provisional entries are
// forward-looking: they are enforced automatically the moment that connector ships
// and are skipped while it does not exist.
func personalAuthExportKinds(host hostMultiUserDecision) map[string]string {
	out := make(map[string]string, len(host.PersonalAuthKinds)+len(provisionalSIWCFactoryKinds))
	for _, kind := range host.PersonalAuthKinds {
		out[kind] = "personal/subscription-auth credential of one human user"
	}
	maps.Copy(out, provisionalSIWCFactoryKinds)
	return out
}

// firstPartyCensus is the whole first-party export universe plus the structural
// escapes that must never be silently invisible to the host gate.
type firstPartyCensus struct {
	Exports []firstPartyExport
	Escapes []string
}

// provisionalSIWCFactoryKinds names the SIWC factories the migration spec declares.
// The posture rules are compiled in by kind, so a named kind is checked from the
// moment it appears in the first-party census and is skipped while it does not
// exist: `openai-chatgpt-plan` is enforced from the moment the direct-inference
// connector ships, and `openai-chatgpt-plan-app-server` from the moment the
// agent-runtime phase ships. Keeping them named makes the intent auditable and
// keeps the census honest about what does not exist.
//
// `openai-codex` and `openai-codex-app-server` are deliberately absent: they are
// the legacy personal-subscription factories that the SIWC migration removes, and
// their posture is covered by the generic `agent_runtime => local_only` and
// `oauth_user => local_only` rules. They are additionally covered transitively: the
// census requires every kind in the host's personal-auth table to exist, so a
// personal-auth factory cannot quietly disappear from the posture checks.
var provisionalSIWCFactoryKinds = map[string]string{
	"openai-chatgpt-plan":            "SIWC ChatGPT-plan OAuth profile of the selected user",
	"openai-chatgpt-plan-app-server": "SIWC-backed Codex app-server of the selected user",
}

// collectFirstPartyCensus enumerates every first-party connector export from the
// checked-in release descriptors and manifest templates. It never guesses: a
// connector that cannot be enumerated is reported as a census escape.
func collectFirstPartyCensus(connectorsRoot string) (firstPartyCensus, error) {
	entries, err := os.ReadDir(connectorsRoot)
	if err != nil {
		return firstPartyCensus{}, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "_") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)

	var census firstPartyCensus
	for _, name := range names {
		dir := filepath.Join(connectorsRoot, name)
		rel := path.Join(firstPartyConnectorsDir, name)
		raw, readErr := os.ReadFile(filepath.Join(dir, "release.yaml"))
		if readErr != nil {
			census.Escapes = append(census.Escapes, fmt.Sprintf("%s has no release.yaml: the first-party census cannot see it", rel))
			continue
		}
		var release connectorRelease
		if err := yaml.Unmarshal(raw, &release); err != nil {
			return census, fmt.Errorf("%s/release.yaml: %w", rel, err)
		}
		if release.Schema != connectorReleaseSchema {
			census.Escapes = append(census.Escapes, fmt.Sprintf("%s/release.yaml schema %q is not %q", rel, release.Schema, connectorReleaseSchema))
			continue
		}
		if strings.TrimSpace(release.ManifestTemplate) == "" {
			census.Escapes = append(census.Escapes, fmt.Sprintf("%s/release.yaml declares no manifest_template", rel))
			continue
		}
		exports, escapes, err := readConnectorManifestTemplate(dir, rel, release)
		if err != nil {
			return census, err
		}
		census.Exports = append(census.Exports, exports...)
		census.Escapes = append(census.Escapes, escapes...)
	}
	slices.SortFunc(census.Exports, func(a, b firstPartyExport) int { return strings.Compare(a.Ref(), b.Ref()) })
	return census, nil
}

// readConnectorManifestTemplate loads one connector's manifest template and
// reconciles it with the release descriptor that points at it.
func readConnectorManifestTemplate(dir, rel string, release connectorRelease) ([]firstPartyExport, []string, error) {
	tmplRel := path.Join(rel, filepath.ToSlash(release.ManifestTemplate))
	clean := path.Clean(release.ManifestTemplate)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, []string{fmt.Sprintf("%s manifest_template %q escapes its connector module", rel, release.ManifestTemplate)}, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(clean)))
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: manifest template %q unreadable: %v", rel, release.ManifestTemplate, err)}, nil
	}
	var tmpl manifestTemplate
	if err := json.Unmarshal(raw, &tmpl); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tmplRel, err)
	}
	var escapes []string
	if tmpl.Schema != backendManifestSchema {
		escapes = append(escapes, fmt.Sprintf("%s schema %q is not %q", tmplRel, tmpl.Schema, backendManifestSchema))
	}
	if tmpl.PluginID != release.PluginID {
		escapes = append(escapes, fmt.Sprintf("%s plugin_id %q does not match release.yaml plugin_id %q", tmplRel, tmpl.PluginID, release.PluginID))
	}
	if len(tmpl.Exports) == 0 {
		return nil, append(escapes, fmt.Sprintf("%s declares no exports", tmplRel)), nil
	}
	exports := make([]firstPartyExport, 0, len(tmpl.Exports))
	seen := map[string]struct{}{}
	hasFactoryKind := false
	for _, ex := range tmpl.Exports {
		hasFactoryKind = hasFactoryKind || ex.Kind == release.FactoryKind
		if _, dup := seen[ex.Kind]; dup {
			escapes = append(escapes, fmt.Sprintf("%s declares export kind %q twice", tmplRel, ex.Kind))
		}
		seen[ex.Kind] = struct{}{}
		exports = append(exports, firstPartyExport{
			Connector:      rel,
			PluginID:       tmpl.PluginID,
			Kind:           ex.Kind,
			CredentialMode: ex.CredentialMode,
			AccessScope:    ex.AccessScope,
			ExecutionClass: ex.ExecutionClass,
			ProcessSharing: ex.ProcessSharing,
		})
	}
	if !hasFactoryKind {
		escapes = append(escapes, fmt.Sprintf("%s release.yaml factory_kind %q is declared by no export of %s", rel, release.FactoryKind, tmplRel))
	}
	return exports, escapes, nil
}

// byKind indexes a census by factory kind, failing closed on duplicates.
func (c firstPartyCensus) byKind() (map[string]firstPartyExport, error) {
	out := make(map[string]firstPartyExport, len(c.Exports))
	for _, e := range c.Exports {
		if prev, dup := out[e.Kind]; dup {
			return nil, fmt.Errorf("factory kind %q is exported twice: %s and %s", e.Kind, prev.Ref(), e.Ref())
		}
		out[e.Kind] = e
	}
	return out, nil
}

// firstPartyPostureViolation reports the first violated first-party multi-user
// security invariant for one export, or "" when the export is consistent.
//
//	execution_class: agent_runtime => access_scope: local_only  (product policy; secondary)
//	credential_mode: oauth_user    => access_scope: local_only  (generic personal-credential rule)
//	named personal-auth kind        => access_scope: local_only  (defense in depth)
//	closed value sets: unknown or empty values always fail closed
func firstPartyPostureViolation(e firstPartyExport, personalAuth map[string]string) string {
	if !slices.Contains(knownAccessScopes, e.AccessScope) {
		return fmt.Sprintf("access_scope %q is not one of %q/%q", e.AccessScope, scopeAny, scopeLocalOnly)
	}
	if !slices.Contains(knownCredentialModes, e.CredentialMode) {
		return fmt.Sprintf("credential_mode %q is not a known credential posture", e.CredentialMode)
	}
	if !slices.Contains(knownExecutionClass, e.ExecutionClass) {
		return fmt.Sprintf("execution_class %q is not one of %q/%q", e.ExecutionClass, classInference, classAgentRuntime)
	}
	if e.ExecutionClass == classAgentRuntime && e.AccessScope != scopeLocalOnly {
		return fmt.Sprintf("execution_class %q requires access_scope %q but declares %q", classAgentRuntime, scopeLocalOnly, e.AccessScope)
	}
	if e.CredentialMode == credentialOAuthUser && e.AccessScope != scopeLocalOnly {
		return fmt.Sprintf("credential_mode %q requires access_scope %q but declares %q", credentialOAuthUser, scopeLocalOnly, e.AccessScope)
	}
	if why, named := personalAuth[e.Kind]; named && e.AccessScope != scopeLocalOnly {
		return fmt.Sprintf("personal-auth factory (%s) requires access_scope %q but declares %q", why, scopeLocalOnly, e.AccessScope)
	}
	return ""
}

// approvedPostureViolation reports why an approved factory kind is no longer
// compatible with its host multi-user approval, or "" when it still is. This is
// strictly the multi-user compatibility projection of the posture invariants, so it
// also fails closed on every posture violation. The reasons are ordered so the
// report names the most specific incompatibility first.
func approvedPostureViolation(e firstPartyExport, personalAuth map[string]string) string {
	if reason := firstPartyPostureViolation(e, personalAuth); reason != "" {
		return reason
	}
	switch {
	case e.ExecutionClass == classAgentRuntime:
		return fmt.Sprintf("approved for multi_user but execution_class is %q", classAgentRuntime)
	case e.CredentialMode == credentialOAuthUser:
		return fmt.Sprintf("approved for multi_user but credential_mode is %q", credentialOAuthUser)
	case e.CredentialMode == credentialUnknown || e.CredentialMode == "":
		return fmt.Sprintf("approved for multi_user but credential_mode is %q", e.CredentialMode)
	}
	if e.AccessScope != scopeAny {
		return fmt.Sprintf("approved for multi_user but access_scope is %q, want %q", e.AccessScope, scopeAny)
	}
	if e.ExecutionClass != classInference {
		return fmt.Sprintf("approved for multi_user but execution_class is %q, want %q", e.ExecutionClass, classInference)
	}
	return ""
}

// hostAllowsMultiUser mirrors the host composition gate: the most restrictive
// source wins, so eligibility needs BOTH multi-user posture compatibility AND an
// explicit host-owned approval. Absence from the approval registry is denial, and
// an approval can never widen a local-only, user-OAuth, unknown-credential, or
// agent-runtime posture.
func hostAllowsMultiUser(host hostMultiUserDecision, e firstPartyExport) bool {
	if approvedPostureViolation(e, personalAuthExportKinds(host)) != "" {
		return false
	}
	_, approved := host.Approvals[e.Kind]
	return approved
}

// staleApprovals returns approval entries whose factory kind no longer exists in
// the census (a rename or removal that left the privileged surface behind).
//
// Essential built-in kinds are excluded through the actual
// [standardplugins.EssentialBackendKinds] catalog: they are in-process
// registrations, not connector exports, so their absence from the connector census
// is expected rather than a stale approval.
func staleApprovals(host hostMultiUserDecision, census map[string]firstPartyExport) []string {
	var stale []string
	for _, kind := range host.ApprovedKinds {
		if !host.isConnectorApproval(kind) {
			continue
		}
		if _, ok := census[kind]; ok {
			continue
		}
		if _, forwardLooking := provisionalSIWCFactoryKinds[kind]; forwardLooking {
			continue
		}
		stale = append(stale, kind)
	}
	return stale
}

// brokenApprovals returns approved kinds whose current manifest posture is no
// longer compatible with shared multi-user operation.
func brokenApprovals(host hostMultiUserDecision, census map[string]firstPartyExport) []string {
	personalAuth := personalAuthExportKinds(host)
	var broken []string
	for _, kind := range host.ApprovedKinds {
		if !host.isConnectorApproval(kind) {
			continue
		}
		export, ok := census[kind]
		if !ok {
			continue
		}
		if reason := approvedPostureViolation(export, personalAuth); reason != "" {
			broken = append(broken, fmt.Sprintf("%s (%s, plugin %s): %s", kind, export.Connector, export.PluginID, reason))
		}
	}
	return broken
}
