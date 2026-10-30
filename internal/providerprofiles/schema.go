// Package providerprofiles contains the bounded, declarative provider-profile
// seam. It deliberately has no runtime, network, process, or provider-plugin
// dependencies; composition roots compile profiles into family adapters.
package providerprofiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const (
	APIVersionV1       = "aip.provider-profile/v1"
	MaxStringBytes     = 256
	MaxHeaders         = 16
	MaxModels          = 2048
	MaxCapabilities    = 32
	MaxDialects        = 32
	MaxQuirks          = 8
	MaxCatalogProfiles = 4096
	MaxCatalogBytes    = 1 << 20
	MaxProfileBytes    = 256 << 10
)

type Family string

const (
	FamilyOpenAIChat      Family = "openai-chat-compatible"
	FamilyOpenAIResponses Family = "openai-responses-compatible"
	FamilyAnthropic       Family = "anthropic-compatible"
	FamilyOpenResponses   Family = "openresponses-compatible"
	FamilySystemOne       Family = "systemone-compatible"
)

type PathPolicy string

const PathPolicyFamilyDefault PathPolicy = "family_default"

type AuthMode string

const (
	AuthNone      AuthMode = "none"
	AuthBearerEnv AuthMode = "bearer_env"
	AuthAPIKeyEnv AuthMode = "api_key_env"
)

type DiscoveryPolicy string

const (
	DiscoveryFamilyDefault DiscoveryPolicy = "family_default"
	DiscoveryStatic        DiscoveryPolicy = "static"
	DiscoveryDisabled      DiscoveryPolicy = "disabled"
)

type NamespaceMode string

const (
	NamespacePreserve NamespaceMode = "preserve"
	NamespacePrefix   NamespaceMode = "prefix"
	NamespaceStrip    NamespaceMode = "strip_prefix"
)

type AccountingSource string

const (
	AccountingProviderReported AccountingSource = "provider_reported"
	AccountingProviderCountAPI AccountingSource = "provider_count_api"
	AccountingLocalTokenizer   AccountingSource = "local_tokenizer"
	AccountingLocalEstimator   AccountingSource = "local_estimator"
)

// QuirkID identifies bounded family-owned behavior implemented by the
// corresponding adapter. Unsupported identifiers fail closed during validation.
type QuirkID string

const (
	QuirkAnthropicV1Models            QuirkID = "anthropic.v1_models"
	QuirkOpenAIResponsesPath          QuirkID = "openai.responses_path"
	QuirkOpenAIPromptCacheKey         QuirkID = "openai.prompt_cache_key"
	QuirkOpenAIChatReasoningContent   QuirkID = "openai.chat.reasoning_content_on_tool_calls"
	QuirkOpenAIResponsesToolAdjacency QuirkID = "openai.responses.tool_output_adjacency"
)

type Endpoint struct {
	BaseURL    string     `json:"base_url" yaml:"base_url"`
	PathPolicy PathPolicy `json:"path_policy" yaml:"path_policy"`
}
type Auth struct {
	Mode   AuthMode `json:"mode" yaml:"mode"`
	EnvVar string   `json:"env_var" yaml:"env_var"`
	Secret string   `json:"secret,omitempty" yaml:"secret,omitempty"`
}
type SafeHeader struct {
	Name  string `json:"name" yaml:"name"`
	Value string `json:"value" yaml:"value"`
}
type Namespace struct {
	Mode   NamespaceMode `json:"mode" yaml:"mode"`
	Prefix string        `json:"prefix,omitempty" yaml:"prefix,omitempty"`
}
type Model struct {
	CanonicalID string `json:"canonical_id" yaml:"canonical_id"`
	NativeID    string `json:"native_id" yaml:"native_id"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name,omitempty"`
}
type ModelDiscovery struct {
	Policy    DiscoveryPolicy `json:"discovery" yaml:"discovery"`
	Path      string          `json:"path,omitempty" yaml:"path,omitempty"`
	Namespace Namespace       `json:"namespace" yaml:"namespace"`
	Static    []Model         `json:"static,omitempty" yaml:"static,omitempty"`
}
type TokenizerAccounting struct {
	TokenizerID string           `json:"id,omitempty" yaml:"id,omitempty"`
	Source      AccountingSource `json:"source,omitempty" yaml:"source,omitempty"`
}
type CapabilityOverrides struct {
	Enable  []aipapi.Capability `json:"enable,omitempty" yaml:"enable,omitempty"`
	Disable []aipapi.Capability `json:"disable,omitempty" yaml:"disable,omitempty"`
}
type DialectOverrides struct {
	Item       []aipapi.DialectRequirement   `json:"items,omitempty" yaml:"items,omitempty"`
	Reasoning  []aipapi.DialectRequirement   `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Compaction []aipapi.DialectRequirement   `json:"compaction,omitempty" yaml:"compaction,omitempty"`
	Extensions []aipapi.ExtensionRequirement `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

type Profile struct {
	APIVersion   string              `json:"api_version" yaml:"api_version"`
	ID           string              `json:"id" yaml:"id"`
	Family       Family              `json:"family" yaml:"family"`
	Endpoint     Endpoint            `json:"endpoint" yaml:"endpoint"`
	Auth         Auth                `json:"auth" yaml:"auth"`
	Headers      []SafeHeader        `json:"headers,omitempty" yaml:"headers,omitempty"`
	Models       ModelDiscovery      `json:"models" yaml:"models"`
	Tokenizer    TokenizerAccounting `json:"tokenizer" yaml:"tokenizer"`
	Capabilities CapabilityOverrides `json:"capabilities" yaml:"capabilities"`
	Dialects     DialectOverrides    `json:"dialects" yaml:"dialects"`
	Quirks       []QuirkID           `json:"quirks,omitempty" yaml:"quirks,omitempty"`
	// Transform is intentionally rejected. Keeping this field makes the boundary
	// explicit when decoding future data rather than silently accepting a DSL.
	Transform string `json:"transform,omitempty" yaml:"transform,omitempty"`
}

type Compiled struct {
	Profile      Profile
	FamilyMax    aipapi.BackendCaps
	Capabilities aipapi.BackendCaps
	Dialects     aipapi.DialectSupport
}

var (
	safeName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	safeHeaderName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	safeEnv        = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]{0,127}$`)
)

func Validate(p Profile) error {
	if p.APIVersion != APIVersionV1 {
		return fmt.Errorf("profile %q: unsupported api_version %q", p.ID, p.APIVersion)
	}
	if !safeName.MatchString(p.ID) {
		return fmt.Errorf("profile: invalid id")
	}
	if len(p.Family) > MaxStringBytes || len(p.Endpoint.PathPolicy) > MaxStringBytes || len(p.Models.Path) > MaxStringBytes || len(p.Models.Namespace.Prefix) > MaxStringBytes {
		return fmt.Errorf("profile %q: string field exceeds bound", p.ID)
	}
	if p.Models.Policy == DiscoveryStatic && len(p.Models.Static) == 0 {
		return fmt.Errorf("profile %q: static discovery requires models", p.ID)
	}
	if p.Family == FamilySystemOne && p.Models.Policy != DiscoveryStatic {
		return fmt.Errorf("profile %q: System One requires static discovery", p.ID)
	}
	famCaps, ok := familyCapabilities(p.Family)
	if !ok {
		return fmt.Errorf("profile %q: unknown family %q", p.ID, p.Family)
	}
	if err := validateEndpoint(p.Endpoint); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if err := validateAuth(p.Auth); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if (p.Family == FamilyAnthropic && p.Auth.Mode == AuthBearerEnv) || (p.Family != FamilyAnthropic && p.Auth.Mode == AuthAPIKeyEnv) {
		return fmt.Errorf("profile %q: auth mode %q is not supported by family %q", p.ID, p.Auth.Mode, p.Family)
	}
	if p.Family != FamilyOpenAIChat && p.Family != FamilyOpenAIResponses && p.Family != FamilySystemOne && len(p.Headers) > 0 {
		return fmt.Errorf("profile %q: static headers are not supported by family %q", p.ID, p.Family)
	}
	if len(p.Headers) > MaxHeaders {
		return fmt.Errorf("profile %q: too many headers", p.ID)
	}
	seenHeaders := map[string]bool{}
	for _, h := range p.Headers {
		name := strings.TrimSpace(h.Name)
		lowerName := strings.ToLower(name)
		if !safeHeaderName.MatchString(name) || !allowedHeader(lowerName) || strings.ContainsAny(h.Value, "\r\n\x00") || len(h.Value) > MaxStringBytes {
			return fmt.Errorf("profile %q: unsafe header %q", p.ID, name)
		}
		if seenHeaders[strings.ToLower(name)] {
			return fmt.Errorf("profile %q: duplicate header %q", p.ID, name)
		}
		seenHeaders[strings.ToLower(name)] = true
	}
	if err := validateModels(p.Models); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if err := validateTokenizer(p.Tokenizer); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if len(p.Capabilities.Enable) > MaxCapabilities || len(p.Capabilities.Disable) > MaxCapabilities {
		return fmt.Errorf("profile %q: too many capabilities", p.ID)
	}
	seenCapabilities := map[aipapi.Capability]bool{}
	for _, c := range p.Capabilities.Enable {
		if _, ok := famCaps[c]; !ok {
			return fmt.Errorf("profile %q: capability elevation %q", p.ID, c)
		}
		if seenCapabilities[c] {
			return fmt.Errorf("profile %q: duplicate capability %q", p.ID, c)
		}
		seenCapabilities[c] = true
	}
	for _, c := range p.Capabilities.Disable {
		if _, ok := famCaps[c]; !ok {
			return fmt.Errorf("profile %q: unknown capability %q", p.ID, c)
		}
		if seenCapabilities[c] {
			return fmt.Errorf("profile %q: duplicate capability %q", p.ID, c)
		}
		seenCapabilities[c] = true
	}
	for _, enabled := range p.Capabilities.Enable {
		if slices.Contains(p.Capabilities.Disable, enabled) {
			return fmt.Errorf("profile %q: capability %q is both enabled and disabled", p.ID, enabled)
		}
	}
	if err := validateDialects(p.Dialects); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if err := validateDialectFamily(p.Family, p.Dialects); err != nil {
		return fmt.Errorf("profile %q: %w", p.ID, err)
	}
	if len(p.Quirks) > MaxQuirks {
		return fmt.Errorf("profile %q: too many quirks", p.ID)
	}
	seenQuirks := map[QuirkID]bool{}
	for _, q := range p.Quirks {
		if seenQuirks[q] {
			return fmt.Errorf("profile %q: duplicate quirk %q", p.ID, q)
		}
		seenQuirks[q] = true
		if !quirkAllowed(p.Family, q) {
			return fmt.Errorf("profile %q: unknown quirk %q", p.ID, q)
		}
	}
	if p.Models.Path != "" && !slices.Contains(p.Quirks, QuirkAnthropicV1Models) {
		return fmt.Errorf("profile %q: model discovery path requires %q", p.ID, QuirkAnthropicV1Models)
	}
	if slices.Contains(p.Quirks, QuirkAnthropicV1Models) && p.Models.Path == "" {
		return fmt.Errorf("profile %q: quirk %q requires models.path", p.ID, QuirkAnthropicV1Models)
	}
	if p.Transform != "" {
		return fmt.Errorf("profile %q: arbitrary transforms are not supported", p.ID)
	}
	if p.Tokenizer.Source != "" && p.Tokenizer.Source != AccountingLocalTokenizer {
		return fmt.Errorf("profile %q: accounting source %q is not supported by the family adapters", p.ID, p.Tokenizer.Source)
	}
	if p.Models.Policy == DiscoveryDisabled {
		return fmt.Errorf("profile %q: disabled model discovery is not supported by the family adapters", p.ID)
	}
	return nil
}

func Compile(p Profile) (Compiled, error) {
	if err := Validate(p); err != nil {
		return Compiled{}, err
	}
	famCaps, _ := familyCapabilities(p.Family)
	capabilities := aipapi.NewBackendCaps()
	for c := range famCaps {
		capabilities[c] = struct{}{}
	}
	for _, c := range p.Capabilities.Disable {
		delete(capabilities, c)
	}
	for _, c := range p.Capabilities.Enable {
		capabilities[c] = struct{}{}
	}
	return Compiled{Profile: p, FamilyMax: famCaps, Capabilities: capabilities, Dialects: aipapi.NormalizeDialectSupport(aipapi.DialectSupport{ItemDialects: p.Dialects.Item, ReasoningDialects: p.Dialects.Reasoning, CompactionDialects: p.Dialects.Compaction, ExtensionTypes: p.Dialects.Extensions})}, nil
}

func familyCapabilities(f Family) (aipapi.BackendCaps, bool) {
	switch f {
	case FamilySystemOne:
		return aipapi.NewBackendCaps(aipapi.CapabilityDecisions), true
	case FamilyOpenAIChat, FamilyOpenAIResponses:
		return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityVision, aipapi.CapabilityDocuments, aipapi.CapabilityReasoning, aipapi.CapabilityParallelToolCalls), true
	case FamilyAnthropic:
		return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityVision, aipapi.CapabilityDocuments, aipapi.CapabilityParallelToolCalls, aipapi.CapabilityReasoningReplay), true
	case FamilyOpenResponses:
		return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityVision, aipapi.CapabilityDocuments, aipapi.CapabilityReasoning, aipapi.CapabilityOrderedItems, aipapi.CapabilityItemReferences, aipapi.CapabilityCompaction, aipapi.CapabilityOpaqueExtensions), true
	default:
		return nil, false
	}
}

func validateEndpoint(e Endpoint) error {
	if len(e.BaseURL) > MaxStringBytes {
		return fmt.Errorf("endpoint exceeds bound")
	}
	if e.PathPolicy != PathPolicyFamilyDefault {
		return fmt.Errorf("unsupported path policy %q", e.PathPolicy)
	}
	raw := strings.TrimSpace(e.BaseURL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid endpoint")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("invalid endpoint: remote HTTP requires HTTPS")
	}
	if strings.Contains(u.EscapedPath(), "..") {
		return fmt.Errorf("invalid endpoint: path traversal")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func validateAuth(a Auth) error {
	if len(a.EnvVar) > MaxStringBytes || len(a.Secret) > MaxStringBytes {
		return fmt.Errorf("auth field exceeds bound")
	}
	if a.Secret != "" {
		return fmt.Errorf("credential values are forbidden")
	}
	switch a.Mode {
	case AuthNone:
		if a.EnvVar != "" {
			return fmt.Errorf("none auth cannot reference credentials")
		}
	case AuthBearerEnv, AuthAPIKeyEnv:
		if !safeEnv.MatchString(a.EnvVar) {
			return fmt.Errorf("invalid credential environment reference")
		}
	default:
		return fmt.Errorf("unknown auth mode %q", a.Mode)
	}
	return nil
}

func validateModels(m ModelDiscovery) error {
	if len(m.Static) > MaxModels {
		return fmt.Errorf("model inventory exceeds bound")
	}
	if m.Policy != DiscoveryFamilyDefault && m.Policy != DiscoveryStatic && m.Policy != DiscoveryDisabled {
		return fmt.Errorf("unknown model discovery policy %q", m.Policy)
	}
	if m.Policy == DiscoveryDisabled {
		return fmt.Errorf("disabled model discovery is not supported by the family adapters")
	}
	if m.Policy == DiscoveryStatic && m.Path != "" {
		return fmt.Errorf("static model discovery cannot also specify a path")
	}
	if m.Namespace.Mode != NamespacePreserve && m.Namespace.Mode != NamespacePrefix && m.Namespace.Mode != NamespaceStrip {
		return fmt.Errorf("unknown namespace mode %q", m.Namespace.Mode)
	}
	if m.Namespace.Mode == NamespacePrefix && !safeName.MatchString(m.Namespace.Prefix) {
		return fmt.Errorf("invalid model namespace prefix")
	}
	seenCanonical, seenNative := map[string]bool{}, map[string]bool{}
	for _, model := range m.Static {
		if len(model.NativeID) > MaxStringBytes || len(model.CanonicalID) > MaxStringBytes || len(model.DisplayName) > MaxStringBytes || model.NativeID == "" || model.CanonicalID == "" || !safeModelID(model.NativeID) || !safeModelID(model.CanonicalID) || strings.ContainsAny(model.DisplayName, "\r\n\x00") {
			return fmt.Errorf("invalid static model")
		}
		if seenCanonical[model.CanonicalID] || seenNative[model.NativeID] {
			return fmt.Errorf("duplicate static model id")
		}
		seenCanonical[model.CanonicalID], seenNative[model.NativeID] = true, true
	}
	if m.Namespace.Mode != NamespacePreserve {
		return fmt.Errorf("model namespace mode %q is not supported by the family adapters", m.Namespace.Mode)
	}
	if m.Namespace.Prefix != "" {
		return fmt.Errorf("namespace prefix is only valid with prefix mode")
	}
	if len(m.Path) > MaxStringBytes {
		return fmt.Errorf("model discovery path exceeds bound")
	}
	if m.Path != "" {
		if !strings.HasPrefix(m.Path, "/") || strings.ContainsAny(m.Path, "?#\r\n\x00") || strings.Contains(m.Path, "//") {
			return fmt.Errorf("invalid model discovery path")
		}
		decodedPath, err := url.PathUnescape(m.Path)
		if err != nil || strings.ContainsAny(decodedPath, "\r\n\x00") {
			return fmt.Errorf("invalid model discovery path")
		}
		for segment := range strings.SplitSeq(decodedPath, "/") {
			if segment == "." || segment == ".." {
				return fmt.Errorf("model discovery path traversal")
			}
		}
	}
	return nil
}

func safeModelID(id string) bool {
	return strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\r\n\x00?#") && strings.Trim(id, "/") != "" && !strings.Contains(id, "//") && !strings.Contains(id, "..")
}

func validateTokenizer(t TokenizerAccounting) error {
	if len(t.TokenizerID) > MaxStringBytes {
		return fmt.Errorf("tokenizer id exceeds bound")
	}
	switch t.Source {
	case "", AccountingProviderReported, AccountingProviderCountAPI, AccountingLocalTokenizer, AccountingLocalEstimator:
	default:
		return fmt.Errorf("unknown accounting source %q", t.Source)
	}
	return nil
}

func validateDialects(d DialectOverrides) error {
	if len(d.Item)+len(d.Reasoning)+len(d.Compaction)+len(d.Extensions) > MaxDialects {
		return fmt.Errorf("dialect declarations exceed bound")
	}
	seen := map[string]bool{}
	for _, x := range d.Item {
		if (x.Kind != "" && x.Kind != "item") || x.Dialect == "" || len(x.Dialect) > MaxStringBytes || len(x.Implementor) > MaxStringBytes || !safeSmallValue(x.Dialect) || !safeSmallValue(x.Implementor) {
			return fmt.Errorf("invalid item dialect")
		}
		key := "item|" + x.Dialect + "|" + x.Implementor
		if seen[key] {
			return fmt.Errorf("duplicate dialect")
		}
		seen[key] = true
	}
	for _, x := range d.Reasoning {
		if (x.Kind != "" && x.Kind != "reasoning") || x.Dialect == "" || len(x.Dialect) > MaxStringBytes || len(x.Implementor) > MaxStringBytes || !safeSmallValue(x.Dialect) || !safeSmallValue(x.Implementor) {
			return fmt.Errorf("invalid reasoning dialect")
		}
		key := "reasoning|" + x.Dialect + "|" + x.Implementor
		if seen[key] {
			return fmt.Errorf("duplicate dialect")
		}
		seen[key] = true
	}
	for _, x := range d.Compaction {
		if (x.Kind != "" && x.Kind != "compaction") || x.Dialect == "" || len(x.Dialect) > MaxStringBytes || len(x.Implementor) > MaxStringBytes || !safeSmallValue(x.Dialect) || !safeSmallValue(x.Implementor) {
			return fmt.Errorf("invalid compaction dialect")
		}
		key := "compaction|" + x.Dialect + "|" + x.Implementor
		if seen[key] {
			return fmt.Errorf("duplicate dialect")
		}
		seen[key] = true
	}
	for _, x := range d.Extensions {
		if x.Namespace == "" || x.Type == "" || len(x.Namespace) > MaxStringBytes || len(x.Type) > MaxStringBytes || len(x.Implementor) > MaxStringBytes || !safeSmallValue(x.Namespace) || !safeSmallValue(x.Type) || !safeSmallValue(x.Implementor) {
			return fmt.Errorf("invalid extension declaration")
		}
		key := x.Namespace + "|" + x.Type + "|" + x.Implementor
		if seen[key] {
			return fmt.Errorf("duplicate extension declaration")
		}
		seen[key] = true
	}
	return nil
}

func safeSmallValue(value string) bool {
	return strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func validateDialectFamily(f Family, d DialectOverrides) error {
	if f != FamilyOpenResponses && len(d.Item)+len(d.Reasoning)+len(d.Compaction)+len(d.Extensions) > 0 {
		return fmt.Errorf("family %q does not support dialect overrides", f)
	}
	for _, x := range append(append(append([]aipapi.DialectRequirement{}, d.Item...), d.Compaction...), d.Reasoning...) {
		if x.Dialect != "openresponses.2026-04-24" && x.Dialect != "item_reference" {
			return fmt.Errorf("unsupported OpenResponses dialect %q", x.Dialect)
		}
	}
	return nil
}

func quirkAllowed(f Family, q QuirkID) bool {
	// Keep this allowlist synchronized with executable family-adapter behavior.
	// A profile must never validate a quirk that composition silently drops.
	switch q {
	case QuirkAnthropicV1Models:
		return f == FamilyAnthropic
	case QuirkOpenAIPromptCacheKey:
		return f == FamilyOpenAIChat || f == FamilyOpenAIResponses
	case QuirkOpenAIChatReasoningContent:
		return f == FamilyOpenAIChat
	case QuirkOpenAIResponsesToolAdjacency:
		return f == FamilyOpenAIResponses
	default:
		return false
	}
}

func allowedHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "authorization" || lower == "cookie" || lower == "set-cookie" || lower == "x-api-key" || lower == "api-key" || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		return false
	}
	return strings.HasPrefix(lower, "x-provider-") || lower == "anthropic-version" || lower == "anthropic-beta" || lower == "openai-beta" || lower == "user-agent"
}

type Catalog struct{ profiles []Profile }

func NewCatalog(profiles []Profile) (*Catalog, error) {
	if len(profiles) > MaxCatalogProfiles {
		return nil, fmt.Errorf("profile catalog exceeds count bound")
	}
	out := make([]Profile, len(profiles))
	for i, profile := range profiles {
		out[i] = cloneProfile(profile)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for i := range out {
		if i > 0 && out[i].ID == out[i-1].ID {
			return nil, fmt.Errorf("duplicate profile %q", out[i].ID)
		}
		if err := Validate(out[i]); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(out[i])
		if len(encoded) > MaxProfileBytes {
			return nil, fmt.Errorf("profile %q exceeds byte bound", out[i].ID)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("profile catalog: encode: %w", err)
	}
	if len(encoded) > MaxCatalogBytes {
		return nil, fmt.Errorf("profile catalog exceeds byte bound")
	}
	return &Catalog{profiles: out}, nil
}

func (c *Catalog) Profiles() []Profile {
	if c == nil {
		return nil
	}
	out := make([]Profile, len(c.profiles))
	for i, profile := range c.profiles {
		out[i] = cloneProfile(profile)
	}
	return out
}

// DecodeJSON decodes one profile with a closed field set.
func DecodeJSON(data []byte) (Profile, error) {
	if len(data) > MaxProfileBytes {
		return Profile{}, fmt.Errorf("provider profile exceeds byte bound")
	}
	var p Profile
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("provider profile: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Profile{}, fmt.Errorf("provider profile: trailing JSON value")
		}
		return Profile{}, fmt.Errorf("provider profile: trailing JSON: %w", err)
	}
	if err := Validate(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// DecodeYAML decodes one profile with a closed field set.
func DecodeYAML(data []byte) (Profile, error) {
	if len(data) > MaxProfileBytes {
		return Profile{}, fmt.Errorf("provider profile exceeds byte bound")
	}
	var p Profile
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("provider profile: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Profile{}, fmt.Errorf("provider profile: trailing YAML document")
		}
		return Profile{}, fmt.Errorf("provider profile: trailing YAML: %w", err)
	}
	if err := Validate(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}
