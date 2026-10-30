package openresponsescompat

import (
	"fmt"
	"net"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const (
	// ID is the stable built-in-compatible factory kind for the generic
	// remote OpenResponses backend (Requirement 9.1).
	ID = "custom-openresponses-compatible"

	// DefaultProfile is the pinned OpenResponses profile for this backend mode.
	DefaultProfile = "2026-04-24"

	// DefaultItemDialect and DefaultCompactionDialect name the pinned portable
	// profile dialects the generic mode satisfies unless explicitly overridden.
	DefaultItemDialect       = "openresponses.2026-04-24"
	DefaultCompactionDialect = "openresponses.2026-04-24"
)

// Request/response limit defaults and maximum bounds. Values are independent
// per instance; limits reject zero, negative, and out-of-range configuration.
const (
	DefaultMaxRequestItems           = 256
	DefaultMaxRequestItemBytes       = 1 << 20
	DefaultMaxRequestContentParts    = 256
	DefaultMaxRequestTools           = 128
	DefaultMaxRequestExtensionBytes  = 64 << 10
	DefaultMaxResponseItems          = 4096
	DefaultMaxResponseEventBytes     = 1 << 20
	DefaultMaxResponseResourceBytes  = 16 << 20
	DefaultMaxResponseReasoningBytes = 1 << 20
	DefaultMaxResponseTextBytes      = 1 << 20

	MaxAllowedRequestItems           = 4096
	MaxAllowedRequestItemBytes       = 16 << 20
	MaxAllowedRequestContentParts    = 4096
	MaxAllowedRequestTools           = 2048
	MaxAllowedRequestExtensionBytes  = 1 << 20
	MaxAllowedResponseItems          = 65536
	MaxAllowedResponseEventBytes     = 16 << 20
	MaxAllowedResponseResourceBytes  = 64 << 20
	MaxAllowedResponseReasoningBytes = 16 << 20
	MaxAllowedResponseTextBytes      = 16 << 20
)

// Config is the strict, secret-free configuration surface for the generic
// OpenResponses backend. Successful decoding never retains literal credential
// values; credentials are resolved from the environment only.
type Config struct {
	BackendPrefix    string
	Profile          string
	BaseURL          string
	APIKeyEnvVarRoot string
	Models           config.CompatibleModeModelsConfig
	Capabilities     []aipapi.Capability
	Dialects         DialectConfig
	RequestLimits    RequestLimits
	ResponseLimits   ResponseLimits
}

// DialectConfig declares exact dialects and extension types satisfied by the
// configured remote endpoint.
type DialectConfig struct {
	Item       []DialectRequirementConfig
	Reasoning  []DialectRequirementConfig
	Compaction []DialectRequirementConfig
	Extensions []ExtensionRequirementConfig
}

// DialectRequirementConfig is one item/reasoning/compaction dialect declaration.
type DialectRequirementConfig struct {
	Dialect     string
	Implementor string
}

// ExtensionRequirementConfig is one bounded extension type declaration.
type ExtensionRequirementConfig struct {
	Namespace   string
	Type        string
	Implementor string
}

// RequestLimits bounds one outbound OpenResponses request.
type RequestLimits struct {
	MaxItems          int
	MaxItemBytes      int
	MaxContentParts   int
	MaxTools          int
	MaxExtensionBytes int
}

// ResponseLimits bounds one inbound OpenResponses response/stream.
type ResponseLimits struct {
	MaxItems          int
	MaxEventBytes     int
	MaxResourceBytes  int
	MaxReasoningBytes int
	MaxTextBytes      int
}

var knownKeys = map[string]struct{}{
	"backend_prefix":       {},
	"profile":              {},
	"base_url":             {},
	"api_key_env_var_root": {},
	"models":               {},
	"capabilities":         {},
	"dialects":             {},
	"request_limits":       {},
	"response_limits":      {},
}

// providerBoundaryKeys names provider-specific controls that are connector
// owned (Requirement 4.11, 9.12): OpenRouter attribution, routing, billing,
// catalog, and proprietary request controls must never be configured on the
// generic mode. They are rejected with a clear boundary message so operators
// route them to the provider connector instead of silently dropping policy.
var providerBoundaryKeys = map[string]struct{}{
	"openrouter":             {},
	"openrouter_attribution": {},
	"openrouter_route":       {},
	"openrouter_provider":    {},
	"openrouter_billing":     {},
	"openrouter_catalog":     {},
	"openrouter_reasoning":   {},
	"app_url":                {},
	"app_title":              {},
	"static_referer":         {},
	"static_title":           {},
	"x_http_referer":         {},
	"x_title":                {},
	"route":                  {},
	"provider":               {},
	"billing":                {},
	"catalog":                {},
	"middleware":             {},
	"transforms":             {},
	"integrations":           {},
	"prediction":             {},
	"debug":                  {},
	"service_tier":           {},
	"session_id":             {},
	"stop_server_tools_when": {},
	"trace":                  {},
	"include":                {},
	"user":                   {},
	"response_format":        {},
	"reasoning":              {},
	"provider_options":       {},
	"provider_controls":      {},
}

var modelsKnownKeys = map[string]struct{}{
	"source": {},
	"path":   {},
	"items":  {},
}

var modelItemKnownKeys = map[string]struct{}{
	"canonical_id": {},
	"native_id":    {},
	"display_name": {},
}

var dialectsKnownKeys = map[string]struct{}{
	"item":       {},
	"reasoning":  {},
	"compaction": {},
	"extensions": {},
}

var dialectRequirementKnownKeys = map[string]struct{}{
	"dialect":     {},
	"implementor": {},
}

var extensionRequirementKnownKeys = map[string]struct{}{
	"namespace":   {},
	"type":        {},
	"implementor": {},
}

var requestLimitsKnownKeys = map[string]struct{}{
	"max_items":           {},
	"max_item_bytes":      {},
	"max_content_parts":   {},
	"max_tools":           {},
	"max_extension_bytes": {},
}

var responseLimitsKnownKeys = map[string]struct{}{
	"max_items":           {},
	"max_event_bytes":     {},
	"max_resource_bytes":  {},
	"max_reasoning_bytes": {},
	"max_text_bytes":      {},
}

var capabilityNames = map[string]aipapi.Capability{
	"streaming":            aipapi.CapabilityStreaming,
	"tools":                aipapi.CapabilityTools,
	"vision":               aipapi.CapabilityVision,
	"documents":            aipapi.CapabilityDocuments,
	"structured_outputs":   aipapi.CapabilityStructuredOutputs,
	"reasoning":            aipapi.CapabilityReasoning,
	"reasoning_replay":     aipapi.CapabilityReasoningReplay,
	"parallel_tool_calls":  aipapi.CapabilityParallelToolCalls,
	"ordered_items":        aipapi.CapabilityOrderedItems,
	"assistant_phase":      aipapi.CapabilityAssistantPhase,
	"video_input":          aipapi.CapabilityVideoInput,
	"item_references":      aipapi.CapabilityItemReferences,
	"compaction":           aipapi.CapabilityCompaction,
	"opaque_extensions":    aipapi.CapabilityOpaqueExtensions,
	"annotations":          aipapi.CapabilityAnnotations,
	"assistant_media_refs": aipapi.CapabilityAssistantMediaRefs,
}

// defaultCapabilities is the portable pinned-profile capability surface the
// generic mode satisfies unless the operator narrows it. Video input and
// reasoning replay require explicit declaration; annotations and assistant
// media references are not representable on the pinned profile and cannot be
// claimed at all (see unsupportedCapabilities).
var defaultCapabilities = []aipapi.Capability{
	aipapi.CapabilityStreaming,
	aipapi.CapabilityTools,
	aipapi.CapabilityVision,
	aipapi.CapabilityDocuments,
	aipapi.CapabilityReasoning,
	aipapi.CapabilityParallelToolCalls,
	aipapi.CapabilityOrderedItems,
	aipapi.CapabilityAssistantPhase,
	aipapi.CapabilityItemReferences,
	aipapi.CapabilityCompaction,
	aipapi.CapabilityOpaqueExtensions,
}

// unsupportedCapabilities are known capability names the generic mode cannot
// satisfy end-to-end on the pinned profile. Declaring them is rejected so
// operators never make dishonest capability claims.
var unsupportedCapabilities = map[aipapi.Capability]struct{}{
	aipapi.CapabilityAnnotations:        {},
	aipapi.CapabilityAssistantMediaRefs: {},
}

// DecodeConfig strictly decodes opaque compatible-mode YAML for the generic
// OpenResponses backend. Strictness is scoped to this decoder; errors are
// instance-scoped and never echo literal secret values.
func DecodeConfig(instanceID, factoryKind string, n yaml.Node) (Config, error) {
	scope := configScope(instanceID, factoryKind)
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Config{}, fmt.Errorf("%s: config is required", scope)
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return Config{}, fmt.Errorf("%s: config is required", scope)
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping", scope)
	}

	// Central static-credential guard (requirements 2.1, 5.4): reject legacy
	// singular/list/structured/token/PAT/private-key fields at any depth plus
	// interpolation aliases and secret paths before typed decode. The approved
	// env root is probed first so the error names the exact binding to use.
	var probe struct {
		APIKeyEnvVarRoot string `yaml:"api_key_env_var_root"`
	}
	_ = root.Decode(&probe)
	if err := config.RejectBackendStaticCredentials(scope, strings.TrimSpace(probe.APIKeyEnvVarRoot), root); err != nil {
		return Config{}, err
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, provider := providerBoundaryKeys[key]; provider {
			return Config{}, fmt.Errorf("%s: config key %q is provider-connector owned (attribution/routing/billing/catalog/proprietary controls); configure the OpenRouter/provider connector instead", scope, key)
		}
		if _, ok := knownKeys[key]; !ok {
			return Config{}, fmt.Errorf("%s: unknown config key %q", scope, key)
		}
	}

	var raw struct {
		BackendPrefix    string    `yaml:"backend_prefix"`
		Profile          string    `yaml:"profile"`
		BaseURL          string    `yaml:"base_url"`
		APIKeyEnvVarRoot string    `yaml:"api_key_env_var_root"`
		Models           yaml.Node `yaml:"models"`
		Capabilities     yaml.Node `yaml:"capabilities"`
		Dialects         yaml.Node `yaml:"dialects"`
		RequestLimits    yaml.Node `yaml:"request_limits"`
		ResponseLimits   yaml.Node `yaml:"response_limits"`
	}
	if err := root.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%s: %w", scope, err)
	}

	prefix := strings.TrimSpace(raw.BackendPrefix)
	if prefix == "" {
		return Config{}, fmt.Errorf("%s: backend_prefix is required", scope)
	}
	profile := strings.TrimSpace(raw.Profile)
	if profile == "" {
		profile = DefaultProfile
	}
	if profile != DefaultProfile {
		return Config{}, fmt.Errorf("%s: unsupported profile %q (supported: %q)", scope, profile, DefaultProfile)
	}
	baseURL := strings.TrimSpace(raw.BaseURL)
	if baseURL == "" {
		return Config{}, fmt.Errorf("%s: base_url is required", scope)
	}
	if _, err := validateBaseURL(baseURL); err != nil {
		return Config{}, fmt.Errorf("%s: %w", scope, err)
	}

	models, err := decodeModels(scope, raw.Models)
	if err != nil {
		return Config{}, err
	}
	caps, err := decodeCapabilities(scope, raw.Capabilities)
	if err != nil {
		return Config{}, err
	}
	dialects, err := decodeDialects(scope, raw.Dialects)
	if err != nil {
		return Config{}, err
	}
	requestLimits, err := decodeRequestLimits(scope, raw.RequestLimits)
	if err != nil {
		return Config{}, err
	}
	responseLimits, err := decodeResponseLimits(scope, raw.ResponseLimits)
	if err != nil {
		return Config{}, err
	}

	return Config{
		BackendPrefix:    prefix,
		Profile:          profile,
		BaseURL:          baseURL,
		APIKeyEnvVarRoot: strings.TrimSpace(raw.APIKeyEnvVarRoot),
		Models:           models,
		Capabilities:     caps,
		Dialects:         dialects,
		RequestLimits:    requestLimits,
		ResponseLimits:   responseLimits,
	}, nil
}

func configScope(instanceID, factoryKind string) string {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		id = "<unknown>"
	}
	kind := strings.TrimSpace(factoryKind)
	if kind == "" {
		kind = "<unknown>"
	}
	return fmt.Sprintf("compatible backend instance %q (factory %q)", id, kind)
}

func fieldPresent(n yaml.Node) bool {
	if n.Kind == 0 {
		return false
	}
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null") {
		return false
	}
	return true
}

// validateBaseURL enforces the absolute HTTP(S) endpoint policy: remote
// endpoints must use HTTPS; the http scheme is an explicit safe test/dev
// override permitted only for loopback hosts.
func validateBaseURL(raw string) (endpoint.Descriptor, error) {
	d, err := endpoint.ParseBaseURL(raw)
	if err != nil {
		return endpoint.Descriptor{}, err
	}
	if d.Scheme() == "http" && !isLoopbackHost(d.Host()) {
		return endpoint.Descriptor{}, fmt.Errorf("endpoint: http base_url is only allowed for loopback test/dev hosts; use https for remote endpoints")
	}
	return d, nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeModels(scope string, n yaml.Node) (config.CompatibleModeModelsConfig, error) {
	if !fieldPresent(n) {
		return config.CompatibleModeModelsConfig{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: models must be a mapping", scope)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if _, ok := modelsKnownKeys[key]; !ok {
			return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: unknown config key %q", scope, "models."+key)
		}
		if key == "items" {
			items := n.Content[i+1]
			if items.Kind != yaml.SequenceNode {
				return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: models.items must be a sequence", scope)
			}
			for itemIndex, item := range items.Content {
				if item.Kind != yaml.MappingNode {
					return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: models.items[%d] must be a mapping", scope, itemIndex)
				}
				for j := 0; j+1 < len(item.Content); j += 2 {
					itemKey := item.Content[j].Value
					if _, ok := modelItemKnownKeys[itemKey]; !ok {
						return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: unknown config key %q", scope, fmt.Sprintf("models.items[%d].%s", itemIndex, itemKey))
					}
				}
			}
		}
	}
	var raw struct {
		Source string `yaml:"source"`
		Path   string `yaml:"path"`
		Items  []struct {
			CanonicalID string `yaml:"canonical_id"`
			NativeID    string `yaml:"native_id"`
			DisplayName string `yaml:"display_name"`
		} `yaml:"items"`
	}
	if err := n.Decode(&raw); err != nil {
		return config.CompatibleModeModelsConfig{}, fmt.Errorf("%s: models: %w", scope, err)
	}
	out := config.CompatibleModeModelsConfig{
		Source: strings.TrimSpace(raw.Source),
		Path:   strings.TrimSpace(raw.Path),
	}
	for _, item := range raw.Items {
		out.Items = append(out.Items, config.CompatibleModeModelItem{
			CanonicalID: strings.TrimSpace(item.CanonicalID),
			NativeID:    strings.TrimSpace(item.NativeID),
			DisplayName: strings.TrimSpace(item.DisplayName),
		})
	}
	return out, nil
}

func decodeCapabilities(scope string, n yaml.Node) ([]aipapi.Capability, error) {
	if !fieldPresent(n) {
		return append([]aipapi.Capability(nil), defaultCapabilities...), nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: capabilities must be a sequence", scope)
	}
	out := make([]aipapi.Capability, 0, len(n.Content))
	for i, item := range n.Content {
		if item.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s: capabilities[%d] must be a string", scope, i)
		}
		name := strings.TrimSpace(item.Value)
		capability, ok := capabilityNames[name]
		if !ok {
			return nil, fmt.Errorf("%s: unknown capability %q", scope, name)
		}
		if _, unsupported := unsupportedCapabilities[capability]; unsupported {
			return nil, fmt.Errorf("%s: capability %q is not representable on the pinned OpenResponses profile and cannot be claimed", scope, name)
		}
		out = append(out, capability)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: capabilities must not be empty", scope)
	}
	return out, nil
}

func defaultDialects() DialectConfig {
	return DialectConfig{
		// The pinned profile carries item_reference items verbatim, so the
		// default item_references capability is truthful only when the exact
		// item_reference item dialect is declared alongside the portable profile
		// item dialect.
		Item: []DialectRequirementConfig{
			{Dialect: DefaultItemDialect},
			{Dialect: "item_reference"},
		},
		Compaction: []DialectRequirementConfig{{Dialect: DefaultCompactionDialect}},
	}
}

func decodeDialects(scope string, n yaml.Node) (DialectConfig, error) {
	if !fieldPresent(n) {
		return defaultDialects(), nil
	}
	if n.Kind != yaml.MappingNode {
		return DialectConfig{}, fmt.Errorf("%s: dialects must be a mapping", scope)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if _, ok := dialectsKnownKeys[key]; !ok {
			return DialectConfig{}, fmt.Errorf("%s: unknown config key %q", scope, "dialects."+key)
		}
	}
	byKey := map[string]yaml.Node{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		byKey[n.Content[i].Value] = *n.Content[i+1]
	}
	item, err := decodeDialectList(scope, "item", byKey["item"])
	if err != nil {
		return DialectConfig{}, err
	}
	reasoning, err := decodeDialectList(scope, "reasoning", byKey["reasoning"])
	if err != nil {
		return DialectConfig{}, err
	}
	compaction, err := decodeDialectList(scope, "compaction", byKey["compaction"])
	if err != nil {
		return DialectConfig{}, err
	}
	extensions, err := decodeExtensionList(scope, byKey["extensions"])
	if err != nil {
		return DialectConfig{}, err
	}
	return DialectConfig{Item: item, Reasoning: reasoning, Compaction: compaction, Extensions: extensions}, nil
}

func decodeDialectList(scope, section string, n yaml.Node) ([]DialectRequirementConfig, error) {
	if !fieldPresent(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: dialects.%s must be a sequence", scope, section)
	}
	out := make([]DialectRequirementConfig, 0, len(n.Content))
	for i, item := range n.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: dialects.%s[%d] must be a mapping", scope, section, i)
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			key := item.Content[j].Value
			if _, ok := dialectRequirementKnownKeys[key]; !ok {
				return nil, fmt.Errorf("%s: unknown config key %q", scope, fmt.Sprintf("dialects.%s[%d].%s", section, i, key))
			}
		}
		var row struct {
			Dialect     string `yaml:"dialect"`
			Implementor string `yaml:"implementor"`
		}
		if err := item.Decode(&row); err != nil {
			return nil, fmt.Errorf("%s: dialects.%s[%d]: %w", scope, section, i, err)
		}
		if strings.TrimSpace(row.Dialect) == "" {
			return nil, fmt.Errorf("%s: dialects.%s[%d]: dialect is required", scope, section, i)
		}
		out = append(out, DialectRequirementConfig{
			Dialect:     strings.TrimSpace(row.Dialect),
			Implementor: strings.TrimSpace(row.Implementor),
		})
	}
	return out, nil
}

func decodeExtensionList(scope string, n yaml.Node) ([]ExtensionRequirementConfig, error) {
	if !fieldPresent(n) {
		return nil, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: dialects.extensions must be a sequence", scope)
	}
	out := make([]ExtensionRequirementConfig, 0, len(n.Content))
	for i, item := range n.Content {
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: dialects.extensions[%d] must be a mapping", scope, i)
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			key := item.Content[j].Value
			if _, ok := extensionRequirementKnownKeys[key]; !ok {
				return nil, fmt.Errorf("%s: unknown config key %q", scope, fmt.Sprintf("dialects.extensions[%d].%s", i, key))
			}
		}
		var row struct {
			Namespace   string `yaml:"namespace"`
			Type        string `yaml:"type"`
			Implementor string `yaml:"implementor"`
		}
		if err := item.Decode(&row); err != nil {
			return nil, fmt.Errorf("%s: dialects.extensions[%d]: %w", scope, i, err)
		}
		if strings.TrimSpace(row.Namespace) == "" || strings.TrimSpace(row.Type) == "" {
			return nil, fmt.Errorf("%s: dialects.extensions[%d]: namespace and type are required", scope, i)
		}
		out = append(out, ExtensionRequirementConfig{
			Namespace:   strings.TrimSpace(row.Namespace),
			Type:        strings.TrimSpace(row.Type),
			Implementor: strings.TrimSpace(row.Implementor),
		})
	}
	return out, nil
}

func defaultRequestLimits() RequestLimits {
	return RequestLimits{
		MaxItems:          DefaultMaxRequestItems,
		MaxItemBytes:      DefaultMaxRequestItemBytes,
		MaxContentParts:   DefaultMaxRequestContentParts,
		MaxTools:          DefaultMaxRequestTools,
		MaxExtensionBytes: DefaultMaxRequestExtensionBytes,
	}
}

func defaultResponseLimits() ResponseLimits {
	return ResponseLimits{
		MaxItems:          DefaultMaxResponseItems,
		MaxEventBytes:     DefaultMaxResponseEventBytes,
		MaxResourceBytes:  DefaultMaxResponseResourceBytes,
		MaxReasoningBytes: DefaultMaxResponseReasoningBytes,
		MaxTextBytes:      DefaultMaxResponseTextBytes,
	}
}

func decodeRequestLimits(scope string, n yaml.Node) (RequestLimits, error) {
	if !fieldPresent(n) {
		return defaultRequestLimits(), nil
	}
	if n.Kind != yaml.MappingNode {
		return RequestLimits{}, fmt.Errorf("%s: request_limits must be a mapping", scope)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if _, ok := requestLimitsKnownKeys[key]; !ok {
			return RequestLimits{}, fmt.Errorf("%s: unknown config key %q", scope, "request_limits."+key)
		}
	}
	var raw struct {
		MaxItems          *int `yaml:"max_items"`
		MaxItemBytes      *int `yaml:"max_item_bytes"`
		MaxContentParts   *int `yaml:"max_content_parts"`
		MaxTools          *int `yaml:"max_tools"`
		MaxExtensionBytes *int `yaml:"max_extension_bytes"`
	}
	if err := n.Decode(&raw); err != nil {
		return RequestLimits{}, fmt.Errorf("%s: request_limits: %w", scope, err)
	}
	out := defaultRequestLimits()
	if raw.MaxItems != nil {
		if err := validateLimit(scope, "request_limits.max_items", *raw.MaxItems, MaxAllowedRequestItems); err != nil {
			return RequestLimits{}, err
		}
		out.MaxItems = *raw.MaxItems
	}
	if raw.MaxItemBytes != nil {
		if err := validateLimit(scope, "request_limits.max_item_bytes", *raw.MaxItemBytes, MaxAllowedRequestItemBytes); err != nil {
			return RequestLimits{}, err
		}
		out.MaxItemBytes = *raw.MaxItemBytes
	}
	if raw.MaxContentParts != nil {
		if err := validateLimit(scope, "request_limits.max_content_parts", *raw.MaxContentParts, MaxAllowedRequestContentParts); err != nil {
			return RequestLimits{}, err
		}
		out.MaxContentParts = *raw.MaxContentParts
	}
	if raw.MaxTools != nil {
		if err := validateLimit(scope, "request_limits.max_tools", *raw.MaxTools, MaxAllowedRequestTools); err != nil {
			return RequestLimits{}, err
		}
		out.MaxTools = *raw.MaxTools
	}
	if raw.MaxExtensionBytes != nil {
		if err := validateLimit(scope, "request_limits.max_extension_bytes", *raw.MaxExtensionBytes, MaxAllowedRequestExtensionBytes); err != nil {
			return RequestLimits{}, err
		}
		out.MaxExtensionBytes = *raw.MaxExtensionBytes
	}
	return out, nil
}

func decodeResponseLimits(scope string, n yaml.Node) (ResponseLimits, error) {
	if !fieldPresent(n) {
		return defaultResponseLimits(), nil
	}
	if n.Kind != yaml.MappingNode {
		return ResponseLimits{}, fmt.Errorf("%s: response_limits must be a mapping", scope)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if _, ok := responseLimitsKnownKeys[key]; !ok {
			return ResponseLimits{}, fmt.Errorf("%s: unknown config key %q", scope, "response_limits."+key)
		}
	}
	var raw struct {
		MaxItems          *int `yaml:"max_items"`
		MaxEventBytes     *int `yaml:"max_event_bytes"`
		MaxResourceBytes  *int `yaml:"max_resource_bytes"`
		MaxReasoningBytes *int `yaml:"max_reasoning_bytes"`
		MaxTextBytes      *int `yaml:"max_text_bytes"`
	}
	if err := n.Decode(&raw); err != nil {
		return ResponseLimits{}, fmt.Errorf("%s: response_limits: %w", scope, err)
	}
	out := defaultResponseLimits()
	if raw.MaxItems != nil {
		if err := validateLimit(scope, "response_limits.max_items", *raw.MaxItems, MaxAllowedResponseItems); err != nil {
			return ResponseLimits{}, err
		}
		out.MaxItems = *raw.MaxItems
	}
	if raw.MaxEventBytes != nil {
		if err := validateLimit(scope, "response_limits.max_event_bytes", *raw.MaxEventBytes, MaxAllowedResponseEventBytes); err != nil {
			return ResponseLimits{}, err
		}
		out.MaxEventBytes = *raw.MaxEventBytes
	}
	if raw.MaxResourceBytes != nil {
		if err := validateLimit(scope, "response_limits.max_resource_bytes", *raw.MaxResourceBytes, MaxAllowedResponseResourceBytes); err != nil {
			return ResponseLimits{}, err
		}
		out.MaxResourceBytes = *raw.MaxResourceBytes
	}
	if raw.MaxReasoningBytes != nil {
		if err := validateLimit(scope, "response_limits.max_reasoning_bytes", *raw.MaxReasoningBytes, MaxAllowedResponseReasoningBytes); err != nil {
			return ResponseLimits{}, err
		}
		out.MaxReasoningBytes = *raw.MaxReasoningBytes
	}
	if raw.MaxTextBytes != nil {
		if err := validateLimit(scope, "response_limits.max_text_bytes", *raw.MaxTextBytes, MaxAllowedResponseTextBytes); err != nil {
			return ResponseLimits{}, err
		}
		out.MaxTextBytes = *raw.MaxTextBytes
	}
	return out, nil
}

func validateLimit(scope, name string, v, limit int) error {
	if v <= 0 {
		return fmt.Errorf("%s: %s must be positive", scope, name)
	}
	if v > limit {
		return fmt.Errorf("%s: %s must be at most %d", scope, name, limit)
	}
	return nil
}
