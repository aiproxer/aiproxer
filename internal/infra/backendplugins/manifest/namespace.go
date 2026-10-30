package manifest

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// canonicalCredentialNamespaces is the closed host registration of static
// provider-secret namespaces (requirements 1.3, 1.4, 4.4, 5.1, 5.3).
//
// Each entry binds one factory kind to its canonical nonsecret credential
// contract: provider environment root, secret slot shape, selection mode and
// quota domain. A provider root is unique unless the entries sharing it name
// the same explicit alias group: OpenAI Chat/Responses deliberately share
// OPENAI_API_KEY, while CommandCode and OpenCode flavors use separate roots
// so independent deployments never alias one quota identity.
//
// Custom compatible-provider kinds carry no fixed root here: requirement 1.4
// permits a validated nonsecret environment-root reference per instance, and
// the resolver owns that binding validation. Host callers admit those
// in-process kinds by passing their IDs explicitly to
// AdmitStaticExportWithCustomRoots, so admission can still tell "known
// custom-root kind" apart from "undeclared new static connector".
//
// Provisional roots marked below (sagemaker, oci, vertex, sapaicore, acp,
// claudeagentacp, openai-codex) keep their current static posture registered
// while their auth-mode reclassification belongs to later tasks; rotation
// behavior for those kinds is not decided here.
func canonicalCredentialNamespaces() []canonicalEntry {
	quota := backendplugin.CredentialSelectionQuotaPreOutput
	apiKey := backendplugin.CredentialSlotAPIKey
	apiToken := backendplugin.CredentialSlotAPIToken
	key := backendplugin.CredentialClassAPIKey
	token := backendplugin.CredentialClassAPIToken
	nonrot := backendplugin.CredentialClassStaticNonrotatable
	hostEnv := backendplugin.CredentialSourceHostEnv
	entry := func(kind, root string, class backendplugin.CredentialClass, slot backendplugin.CredentialSlot, sel backendplugin.CredentialSelection, domain, alias string) canonicalEntry {
		return canonicalEntry{Kind: kind, AliasGroup: alias, Contract: backendplugin.CredentialContract{
			Version: backendplugin.CredentialContractVersion, Class: class, Source: hostEnv, EnvRoot: root, Slot: slot, Selection: sel, QuotaDomain: domain,
		}}
	}
	return []canonicalEntry{
		// Built-in in-process families.
		entry("openai-responses", "OPENAI_API_KEY", key, apiKey, quota, "openai", "openai"),
		entry("openai-legacy", "OPENAI_API_KEY", key, apiKey, quota, "openai", "openai"),
		entry("anthropic", "ANTHROPIC_API_KEY", key, apiKey, quota, "anthropic", ""),
		entry("gemini", "GEMINI_API_KEY", key, apiKey, quota, "gemini", ""),
		entry("alibaba-token-plan-intl", "ALIBABA_TOKEN_PLAN_API_KEY", key, apiKey, quota, "alibaba-token-plan", ""),
		// Executable inference connectors: bearer-key cohort.
		entry("openrouter", "OPENROUTER_API_KEY", key, apiKey, quota, "openrouter", ""),
		entry("nvidia", "NVIDIA_API_KEY", key, apiKey, quota, "nvidia", ""),
		entry("huggingface", "HUGGINGFACE_API_KEY", key, apiKey, quota, "huggingface", ""),
		entry("commandcode-openai", "COMMANDCODE_OPENAI_API_KEY", key, apiKey, quota, "commandcode-openai", ""),
		entry("commandcode-anthropic", "COMMANDCODE_ANTHROPIC_API_KEY", key, apiKey, quota, "commandcode-anthropic", ""),
		entry("opencode-go", "OPENCODE_GO_API_KEY", key, apiKey, quota, "opencode-go", ""),
		entry("opencode-zen", "OPENCODE_ZEN_API_KEY", key, apiKey, quota, "opencode-zen", ""),
		entry("azure-openai", "AZURE_OPENAI_API_KEY", key, apiKey, quota, "azure-openai", ""),
		entry("cloudflare", "CLOUDFLARE_API_TOKEN", token, apiToken, quota, "cloudflare", ""),
		entry("cohere", "COHERE_API_KEY", key, apiKey, quota, "cohere", ""),
		entry("databricks-ai", "DATABRICKS_API_TOKEN", token, apiToken, quota, "databricks-ai", ""),
		entry("infomaniak-ai", "INFOMANIAK_API_KEY", key, apiKey, quota, "infomaniak-ai", ""),
		entry("replicate", "REPLICATE_API_TOKEN", token, apiToken, quota, "replicate", ""),
		entry("snowflake-cortex", "SNOWFLAKE_CORTEX_PAT", token, apiToken, quota, "snowflake-cortex", ""),
		entry("watsonx", "WATSONX_API_KEY", key, apiKey, quota, "watsonx", ""),
		entry("ollama-cloud", "OLLAMA_CLOUD_API_KEY", key, apiKey, quota, "ollama-cloud", ""),
		// Service-key / signing / subscription modes: registered static,
		// explicitly non-rotatable; reclassification belongs to later tasks.
		entry("sagemaker", "AWS_ACCESS_KEY_ID", nonrot, apiKey, backendplugin.CredentialSelectionNoRotation, "aws-sagemaker", ""),
		entry("oci-generative-ai", "OCI_API_KEY", nonrot, apiKey, backendplugin.CredentialSelectionNoRotation, "oci-generative-ai", ""),
		entry("vertex", "VERTEX_API_KEY", nonrot, apiKey, backendplugin.CredentialSelectionNoRotation, "vertex-ai", ""),
		entry("sapaicore", "SAP_AI_CORE_SERVICE_KEY", nonrot, apiToken, backendplugin.CredentialSelectionNoRotation, "sap-ai-core", ""),
		entry("openai-codex", "OPENAI_CODEX_ACCESS_TOKEN", nonrot, apiToken, backendplugin.CredentialSelectionNoRotation, "codex", ""),
		// Agent-runtime session admission: selected once, never replayed.
		// cursorsdk is served by the standalone aiproxer/aiproxer-cursor-sdk
		// plugin, not by an in-tree module: this registration is the host-side
		// credential contract that the plugin's manifest is validated against.
		entry("cursorsdk", "CURSOR_API_KEY", key, apiKey, backendplugin.CredentialSelectionSessionAdmission, "cursor", ""),
		entry("acp", "ACP_API_KEY", nonrot, apiKey, backendplugin.CredentialSelectionSessionAdmission, "acp", ""),
		entry("claudeagentacp", "CLAUDE_CODE_OAUTH_TOKEN", nonrot, apiToken, backendplugin.CredentialSelectionSessionAdmission, "claude-agent", ""),
	}
}

// canonicalEntry is one registered kind-to-contract binding. AliasGroup names
// an explicitly approved same-provider sharing group; empty means the entry's
// environment root must be unique across the registry.
type canonicalEntry struct {
	Kind       string
	AliasGroup string
	Contract   backendplugin.CredentialContract
}

// Custom-root compatible kinds (requirement 1.4) are intentionally NOT named
// here: an architecture gate forbids generic compatible-kind literals in host
// packages, and those in-process kinds never arrive as manifest exports. Host
// callers that admit them pass their IDs explicitly (via the standardplugins
// Custom*CompatibleID constants) to AdmitStaticExportWithCustomRoots.
func customRootContains(customRoots []string, kind string) bool {
	return slices.Contains(customRoots, kind)
}

func canonicalByKind() map[string]canonicalEntry {
	out := make(map[string]canonicalEntry)
	for _, e := range canonicalCredentialNamespaces() {
		out[e.Kind] = e
	}
	return out
}

// LookupCanonicalContract returns the registered contract for a fixed-root
// kind. The second result is false for custom-root and unknown kinds.
func LookupCanonicalContract(kind string) (backendplugin.CredentialContract, bool) {
	e, ok := canonicalByKind()[strings.TrimSpace(kind)]
	if !ok {
		return backendplugin.CredentialContract{}, false
	}
	return e.Contract, true
}

// IsRegisteredCredentialKind reports whether kind has a fixed-root
// registration in this registry. Custom-root compatible kinds are admitted
// through AdmitStaticExportWithCustomRoots with caller-supplied IDs and are
// not fixed here.
func IsRegisteredCredentialKind(kind string) bool {
	_, ok := canonicalByKind()[strings.TrimSpace(kind)]
	return ok
}

// IsDeclaredSameProviderAlias reports whether two kinds are expressly
// declared to share one provider credential namespace (requirement 1.3).
// Accidental root equality without a declaration is a collision, not sharing.
func IsDeclaredSameProviderAlias(a, b string) bool {
	byKind := canonicalByKind()
	ea, ok := byKind[strings.TrimSpace(a)]
	if !ok {
		return false
	}
	eb, ok := byKind[strings.TrimSpace(b)]
	if !ok {
		return false
	}
	return ea.AliasGroup != "" && ea.AliasGroup == eb.AliasGroup
}

// ValidateCredentialNamespaceRegistry proves the registry is self-consistent:
// every entry validates and every fixed root is unique or covered by an
// explicit alias declaration.
func ValidateCredentialNamespaceRegistry() error {
	entries := canonicalCredentialNamespaces()
	seenKind := map[string]struct{}{}
	byRoot := map[string][]canonicalEntry{}
	for _, e := range entries {
		if _, dup := seenKind[e.Kind]; dup {
			return fmt.Errorf("%w: duplicate kind %q", ErrCredentialNamespaceMismatch, e.Kind)
		}
		seenKind[e.Kind] = struct{}{}
		if err := e.Contract.Validate(); err != nil {
			return fmt.Errorf("%w: kind %q: %v", ErrCredentialNamespaceMismatch, e.Kind, err)
		}
		byRoot[e.Contract.EnvRoot] = append(byRoot[e.Contract.EnvRoot], e)
	}
	roots := make([]string, 0, len(byRoot))
	for root := range byRoot {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		group := byRoot[root]
		if len(group) < 2 {
			continue
		}
		alias := group[0].AliasGroup
		if alias == "" {
			return fmt.Errorf("%w: root %q shared without an explicit alias declaration", ErrCredentialNamespaceMismatch, root)
		}
		for _, e := range group[1:] {
			if e.AliasGroup != alias {
				return fmt.Errorf("%w: root %q shared across alias groups", ErrCredentialNamespaceMismatch, root)
			}
		}
	}
	return nil
}

// AdmitStaticExportWithCustomRoots enforces admission like AdmitStaticExport
// but additionally admits known custom-root compatible kinds (requirement
// 1.4) whose credential namespace is bound per instance, not fixed here.
// Callers pass those IDs explicitly (standardplugins Custom*CompatibleID
// constants) so no generic compatible-kind literal lives in host packages.
func AdmitStaticExportWithCustomRoots(exp sdkmanifest.Export, customRoots []string) error {
	return admitStaticExport(exp, customRoots)
}

// AdmitStaticExport enforces the host admission half of the registration
// contract for one manifest export. Non-static exports pass with no contract.
// Static exports must carry a valid contract matching their fixed
// registration exactly. Anything else — unclassified static keys,
// unregistered kinds, drifted bindings — fails closed with a secret-free
// error naming only the kind.
func AdmitStaticExport(exp sdkmanifest.Export) error {
	return admitStaticExport(exp, nil)
}

// registryConsistency proves the compiled-in table is self-consistent once per
// process: every admission decision runs against a validated registry, so a
// future edit that collides two unrelated roots or breaks an entry fails
// closed instead of silently admitting an ambiguous namespace.
var registryConsistency = sync.OnceValue(ValidateCredentialNamespaceRegistry)

// admitStaticExport implements both admission entry points: fixed
// registrations always apply, and caller-supplied customRoots additionally
// admits known custom-root compatible kinds with a standalone valid contract.
func admitStaticExport(exp sdkmanifest.Export, customRoots []string) error {
	if err := registryConsistency(); err != nil {
		return err
	}
	kind := strings.TrimSpace(exp.Kind)
	if exp.CredentialMode != backendplugin.CredentialModeStatic {
		if exp.CredentialContract != nil {
			return fmt.Errorf("%w: kind %q: non-static export must not carry a credential contract",
				sdkmanifest.ErrUnexpectedCredentialContract, kind)
		}
		return nil
	}
	if exp.CredentialContract == nil {
		return fmt.Errorf("%w: kind %q: static export without a credential contract",
			sdkmanifest.ErrMissingCredentialContract, kind)
	}
	if err := exp.CredentialContract.Validate(); err != nil {
		return fmt.Errorf("%w: kind %q: %v", ErrCredentialNamespaceMismatch, kind, err)
	}
	if fixed, ok := canonicalByKind()[kind]; ok {
		if *exp.CredentialContract != fixed.Contract {
			return fmt.Errorf("%w: kind %q diverges from registered env_root %q",
				ErrCredentialNamespaceMismatch, kind, fixed.Contract.EnvRoot)
		}
		return nil
	}
	if customRootContains(customRoots, kind) {
		return nil
	}
	return fmt.Errorf("%w: kind %q", ErrUnregisteredCredentialKind, kind)
}
