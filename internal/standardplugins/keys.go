package standardplugins

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/hostcredentials"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

// Built-in provider credential namespace roots. Each root is the unique
// nonsecret identity of one provider quota domain, except the explicitly
// shared OpenAI Chat/Responses alias pair (see the task-1 namespace
// registry): OPENAI_API_KEY backs both openai-responses and openai-legacy,
// while every other root is owned by exactly one family.
const (
	openAICredentialRoot           = "OPENAI_API_KEY"
	anthropicCredentialRoot        = "ANTHROPIC_API_KEY"
	alibabaTokenPlanCredentialRoot = "ALIBABA_TOKEN_PLAN_API_KEY"
	geminiCredentialRoot           = "GEMINI_API_KEY"
)

// BuiltInCredentialRoots returns the fixed built-in provider roots in stable
// order. Composition uses it to collision-check custom compatible roots
// against provider namespaces.
func BuiltInCredentialRoots() []string {
	return []string{
		openAICredentialRoot,
		anthropicCredentialRoot,
		alibabaTokenPlanCredentialRoot,
		geminiCredentialRoot,
	}
}

// UpstreamAPIKeys carries default API key material resolved once at an
// authorized capture point (composition-root startup or per-candidate
// snapshot) when plugin YAML leaves api_key empty.
// Treat all string values as secrets: do not log them or include them in error text.
type UpstreamAPIKeys struct {
	OpenAI           []string
	Anthropic        []string
	AlibabaTokenPlan []string
	Gemini           []string
	// QuotaSwitchThreshold is the nonsecret proxy-wide quota-switch
	// threshold bound at capture from upstream_credentials. Zero selects the
	// coordinator default; adapters pass it to the credential coordinator.
	QuotaSwitchThreshold time.Duration
}

// CredentialEnvironment is the minimal host-lookup capability credential
// capture needs. Both featurehost.HostEnvironment and osenv.Process satisfy
// it structurally without importing either here.
type CredentialEnvironment interface {
	Lookup(name string) (value string, ok bool)
}

// osProcessEnv adapts the process environment to hostcredentials.Environment
// for the startup capture path. Per-candidate captures pass the injected host
// environment instead; serving paths never call this.
type osProcessEnv struct{}

func (osProcessEnv) Lookup(name string) (string, bool) { return os.LookupEnv(name) }

// ResolveUpstreamAPIKeysFromEnv reads the built-in provider roots plus
// numbered suffixes with the default proxy-wide policy. It is the startup
// capture entrypoint (see [InstallStandardBundleOn]); per-candidate captures
// use [ResolveUpstreamAPIKeysWithPolicy] with the configured policy and the
// injected host environment.
// Migrated external connectors (OpenRouter, NVIDIA, Hugging Face, Ollama, local
// runtimes, OpenCode, Codex) receive credentials only via plugin config YAML /
// secrets.
func ResolveUpstreamAPIKeysFromEnv() UpstreamAPIKeys {
	keys, err := ResolveUpstreamAPIKeysWithPolicy(osProcessEnv{}, config.DefaultUpstreamMaxEnvIndex)
	if err != nil {
		return UpstreamAPIKeys{}
	}
	return keys
}

// ResolveUpstreamAPIKeysWithPolicy resolves the built-in provider roots
// through the shared policy-driven host resolver: deterministic sparse scan
// through maxIndex, first-occurrence de-duplication, explicit root_1
// rejection and value-size budgets. Errors are secret-free and name only the
// offending root or slot.
func ResolveUpstreamAPIKeysWithPolicy(env CredentialEnvironment, maxIndex int) (UpstreamAPIKeys, error) {
	resolved, err := hostcredentials.ResolveAll(BuiltInCredentialRoots(), hostEnvAdapter{env}, maxIndex)
	if err != nil {
		return UpstreamAPIKeys{}, err
	}
	return UpstreamAPIKeys{
		OpenAI:           hostcredentials.Values(resolved[openAICredentialRoot]),
		Anthropic:        hostcredentials.Values(resolved[anthropicCredentialRoot]),
		AlibabaTokenPlan: hostcredentials.Values(resolved[alibabaTokenPlanCredentialRoot]),
		Gemini:           hostcredentials.Values(resolved[geminiCredentialRoot]),
	}, nil
}

// hostEnvAdapter bridges the local CredentialEnvironment to the resolver's
// structurally identical interface without importing host packages here.
type hostEnvAdapter struct {
	env CredentialEnvironment
}

func (a hostEnvAdapter) Lookup(name string) (string, bool) {
	if a.env == nil {
		return "", false
	}
	return a.env.Lookup(name)
}

// upstreamKeysFromSnapshot maps an authoritative per-candidate snapshot onto
// the built-in provider fields. Roots the snapshot does not carry resolve to
// no keys rather than falling back to stale material.
func upstreamKeysFromSnapshot(snap pluginreg.HostCredentialSnapshot) UpstreamAPIKeys {
	return UpstreamAPIKeys{
		OpenAI:               snap.Keys(openAICredentialRoot),
		Anthropic:            snap.Keys(anthropicCredentialRoot),
		AlibabaTokenPlan:     snap.Keys(alibabaTokenPlanCredentialRoot),
		Gemini:               snap.Keys(geminiCredentialRoot),
		QuotaSwitchThreshold: snap.QuotaSwitchRetryAfter,
	}
}

// customCompatibleCredentialKinds are the in-process compatible families whose
// credential namespace is bound per instance via api_key_env_var_root
// (requirement 1.4), not fixed in the namespace registry.
var customCompatibleCredentialKinds = map[string]struct{}{
	CustomOpenAILegacyCompatibleID:    {},
	CustomOpenAIResponsesCompatibleID: {},
	CustomAnthropicCompatibleID:       {},
	CustomOpenResponsesCompatibleID:   {},
	CustomSystemOneCompatibleID:       {},
}

// customCredentialRoots collects the validated nonsecret environment roots
// bound by enabled custom compatible instances. Roots are syntax-checked here
// so a malformed binding fails the candidate before any lookup.
func customCredentialRoots(cfg *config.Config) ([]string, error) {
	if cfg == nil {
		return nil, fmt.Errorf("standardplugins: credential snapshot: nil config")
	}
	var roots []string
	for _, row := range cfg.Plugins.Backends {
		if !row.Enabled {
			continue
		}
		if _, ok := customCompatibleCredentialKinds[row.FactoryID()]; !ok {
			continue
		}
		decoded, err := decodeCompatibleRowProjection(row.FactoryID(), row)
		if err != nil {
			return nil, err
		}
		root := strings.TrimSpace(decoded.APIKeyEnvVarRoot)
		if root == "" {
			continue
		}
		if err := config.ValidateCredentialEnvRoot(root); err != nil {
			return nil, fmt.Errorf("compatible backend instance %q: %w", row.InstanceID(), err)
		}
		roots = append(roots, root)
	}
	return roots, nil
}

// SnapshotCandidateCredentials resolves the complete authorized credential
// snapshot for one candidate: built-in provider roots plus every custom
// compatible root bound by the candidate config, through the shared
// policy-driven resolver with the candidate's configured bound.
//
// A nil env produces the zero snapshot (legacy fallback mode for callers
// without an injected host environment). Any policy violation, malformed
// root, oversized value or namespace collision fails the whole snapshot with
// a secret-free error so the candidate is rejected before publication and the
// active generation stays untouched (requirement 2.5).
func SnapshotCandidateCredentials(env CredentialEnvironment, cfg *config.Config) (pluginreg.HostCredentialSnapshot, error) {
	if env == nil {
		return pluginreg.HostCredentialSnapshot{}, nil
	}
	if cfg == nil {
		return pluginreg.HostCredentialSnapshot{}, fmt.Errorf("standardplugins: credential snapshot: nil config")
	}
	maxIndex, err := cfg.UpstreamCredentials.EffectiveMaxEnvIndex()
	if err != nil {
		return pluginreg.HostCredentialSnapshot{}, err
	}
	quotaSwitch, err := cfg.UpstreamCredentials.EffectiveQuotaSwitchRetryAfter()
	if err != nil {
		return pluginreg.HostCredentialSnapshot{}, err
	}
	customRoots, err := customCredentialRoots(cfg)
	if err != nil {
		return pluginreg.HostCredentialSnapshot{}, err
	}
	roots := append(append([]string(nil), BuiltInCredentialRoots()...), customRoots...)
	for _, root := range customRoots {
		for _, builtIn := range BuiltInCredentialRoots() {
			if root == builtIn {
				return pluginreg.HostCredentialSnapshot{}, fmt.Errorf("standardplugins: credential snapshot: custom root %q collides with built-in provider namespace %q (declare an explicit same-provider alias or use a distinct root)", root, builtIn)
			}
		}
	}
	resolved, err := hostcredentials.ResolveAll(roots, hostEnvAdapter{env}, maxIndex)
	if err != nil {
		return pluginreg.HostCredentialSnapshot{}, err
	}
	byRoot := make(map[string][]string, len(resolved))
	for root, slots := range resolved {
		byRoot[root] = hostcredentials.Values(slots)
	}
	return pluginreg.HostCredentialSnapshot{ByRoot: byRoot, MaxEnvIndex: maxIndex, QuotaSwitchRetryAfter: quotaSwitch}, nil
}
