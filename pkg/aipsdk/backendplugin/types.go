package backendplugin

import "github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"

// UsageEvidence is canonical usage/billing evidence with explicit presence.
type UsageEvidence struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
	TotalTokens      *int64
	Presence         UsagePresence
	RawUsageJSON     RawJSON
	// InputTokensExcludeCacheRead declares that the provider cache reads are
	// not contained in InputTokens. The zero value retains the legacy
	// inclusive-input interpretation.
	InputTokensExcludeCacheRead bool
	// InputTokensExcludeCacheWrite declares that the provider cache writes are
	// not contained in InputTokens. The zero value means InputTokens already
	// includes the cache writes, which is the legacy inclusive-input
	// interpretation.
	//
	// The declaration is independent of InputTokensExcludeCacheRead: a
	// producer may exclude reads while including writes, exclude writes while
	// including reads, or exclude both.
	InputTokensExcludeCacheWrite bool
}

// CapabilitySummary summarizes canonical backend capabilities.
type CapabilitySummary struct {
	Streaming          bool
	Tools              bool
	Vision             bool
	Documents          bool
	StructuredOutputs  bool
	Reasoning          bool
	ReasoningReplay    bool
	ParallelToolCalls  bool
	OrderedItems       bool
	ItemReferences     bool
	Compaction         bool
	AssistantPhase     bool
	OpaqueExtensions   bool
	VideoInput         bool
	Annotations        bool
	AssistantMediaRefs bool
}

// DialectSupport carries exact dialect/extension support advertised by a plugin profile.
type DialectSupport struct {
	ItemDialects       []DialectRequirementDTO
	ReasoningDialects  []DialectRequirementDTO
	CompactionDialects []DialectRequirementDTO
	ExtensionTypes     []ExtensionRequirementDTO
}

// TransportCapabilitySummary summarizes transport capabilities.
type TransportCapabilitySummary struct {
	Keepalive           bool
	Cancellation        bool
	BidirectionalStream bool
}

// PluginDescriptor describes a plugin executable and its factory exports.
type PluginDescriptor struct {
	ProtocolMajor uint32
	ProtocolMinor uint32
	PluginID      string
	Version       string
	BuildID       string
	Features      []Feature
	Factories     []FactoryDescriptor
}

// FactoryDescriptor is generic export metadata for one factory kind.
type FactoryDescriptor struct {
	Kind                      string
	DisplayName               string
	Description               string
	CredentialMode            CredentialMode
	AccessScope               AccessScope
	RoutePrefixes             []string
	SupportsCountTokens       bool
	SupportsFinalizeBilling   bool
	SupportsDynamicInventory  bool
	SupportsModelAwareProfile bool
	ProcessSharing            ProcessSharing
	Experimental              bool
	Deprecated                bool
	StaticCapabilities        CapabilitySummary
	TransportCapabilities     TransportCapabilitySummary
	// SupportsAccountingEvidenceV2 is an explicit descriptor capability. The
	// negotiated feature remains the transport authority for each session.
	SupportsAccountingEvidenceV2 bool
}

// SecretBundle carries configure-time secrets (never logged by this package).
type SecretBundle struct {
	Values map[string][]byte
}

// RuntimePolicy is the stable DTO projection of host runtime policy for external
// executable plugins. Hosts must project policy into this type (and its wire
// form in api/backendplugin/v1) rather than sharing *http.Client, internal
// identity config, or mutable registries across the process boundary.
type RuntimePolicy struct {
	MaxRequestBytes         uint64
	MaxStreamFrameBytes     uint64
	MaxPendingEvents        uint64
	RequestTimeoutMS        int64
	CancelDeadlineMS        int64
	DiagnosticsVerbosity    string
	MaxConcurrentExecutions uint32
	LocalOnly               bool
	AllowedEnvNames         []string
	DisableTransportRetries bool
	// QuotaSwitchRetryAfterMS is the nonsecret proxy-wide quota-switch
	// threshold in milliseconds (upstream_credentials.quota_switch_retry_after).
	// Zero selects the coordinator default; a positive value is the explicit
	// Retry-After duration strictly above which a 429 advances to the next
	// ordered credential. Negative values are normalized to zero (default)
	// by hosts and connectors.
	QuotaSwitchRetryAfterMS int64
}

// ConfigureRequest configures one backend instance after negotiation and peer auth.
// External plugin configure receives only opaque ConfigYAML, Secrets, RuntimePolicy,
// and negotiation metadata — never *http.Client, internal identity, or a registry.
type ConfigureRequest struct {
	InstanceID       string
	FactoryKind      string
	ConfigYAML       []byte
	Secrets          SecretBundle
	RuntimePolicy    RuntimePolicy
	Negotiation      Negotiation
	NegotiationToken string
}

// ResolvedProfile is the capability/transport profile for an instance or model.
type ResolvedProfile struct {
	Capabilities             CapabilitySummary
	TransportCapabilities    TransportCapabilitySummary
	DialectSupport           DialectSupport
	ReasoningReplaySupported bool
	RoutePrefixes            []string
	EnforceMaxOutput         bool
	MaxOutputTokens          *uint32
	SupportsCountTokens      bool
	SupportsFinalizeBilling  bool
	SupportsDynamicInventory bool
	EvidenceSource           string
	ProfileVersion           string
	PromptCacheProfile       promptcache.Profile
	// SupportsAccountingEvidenceV2 is the resolved instance capability bit.
	SupportsAccountingEvidenceV2 bool
	// ReasoningEffortValues is the optional provider-owned graded effort
	// declaration for the exact requested model. Nil means no statement.
	ReasoningEffortValues *ReasoningEffortValuesV1
}

// ModelDescriptor is one inventory row.
type ModelDescriptor struct {
	CanonicalModelID string
	NativeModelID    string
	DisplayName      string
	RoutePrefix      string
	FactoryKind      string
	// IsDefault marks the model the connector designates as the instance's
	// default: the model a request that names no model should use. The
	// connector owns that choice, because only the connector knows the model's
	// role. At most one model of a response may carry it; a connector that
	// cannot name a default leaves every model unmarked.
	IsDefault    bool
	Capabilities CapabilitySummary
}

// ListModelsResponse is a bounded inventory result.
type ListModelsResponse struct {
	Models             []ModelDescriptor
	InventorySource    string
	FetchedUnixMS      int64
	RefreshAfterUnixMS *int64
	ErrorCode          string
}

// Invocation is the canonical attempt payload for execute/count.
type Invocation struct {
	RequestID        string
	AttemptID        string
	ALegID           string
	BLegID           string
	CanonicalModelID string
	NativeModelID    string
	Instructions     []Message
	Messages         []Message
	Tools            []ToolDef
	ToolChoice       *string
	Options          GenerationOptions
	SafeMetadata     map[string]string
	// ProxyOwnedSessionID is injected by the proxy host from aipapi.Call.Session.
	// It is not client metadata and must not be populated from SafeMetadata.
	ProxyOwnedSessionID string

	// PromptCacheKey is a source-compatible legacy alias. SemanticExtensions is
	// the sole authority for new carrier-aware paths; bridges reject conflicting
	// values rather than choosing between two representations.
	PromptCacheKey     string
	SemanticExtensions []SemanticExtension

	// Ordered-item ABI fields (protocol minor >= ProtocolMinorOrderedItems).
	Operation            string
	DeliveryMode         string
	TransportMode        string
	ItemAuthority        bool
	Items                []InvocationItem
	ProtocolRequirements ProtocolRequirements
}

// SemanticExtension is the SDK form of aipapi.SemanticExtension.
type SemanticExtension struct {
	Namespace   string
	Type        string
	Implementor string
	Direction   string
	Presence    SemanticExtensionPresence
	Data        RawJSON
}

type SemanticExtensionPresence string

const (
	SemanticExtensionNull  SemanticExtensionPresence = "null"
	SemanticExtensionValue SemanticExtensionPresence = "value"
)

// Message is an ordered role + parts unit.
type Message struct {
	Role  Role
	Parts []Part
}

// Part is one canonical content part with explicit optional fields.
type Part struct {
	Kind             PartKind
	Text             *string
	ImageRef         *string
	FileRef          *string
	ReasoningText    *string
	ReasoningDialect *string
	ReasoningOpaque  RawJSON
	ToolArgsJSON     RawJSON
	ToolCallID       *string
	ToolName         *string

	// ReasoningSummary, ReasoningContent, and ReasoningEncryptedContent preserve
	// exact OpenAI Responses reasoning-item fields with absent/null/value presence.
	// Additive v1.3 fields gated by FeatureExactOpenResponsesFields.
	ReasoningSummary          RawJSON
	ReasoningContent          RawJSON
	ReasoningEncryptedContent RawJSON
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name           string
	Description    string
	ParametersJSON RawJSON
}

// GenerationOptions carries optional generation controls with presence.
type GenerationOptions struct {
	MaxOutputTokens    *uint32
	TemperatureMillis  *int32
	ReasoningEffort    *string
	ParallelToolCalls  *bool
	ResponseMIMEType   *string
	ResponseSchemaJSON RawJSON
}

// CountTokensRequest asks a plugin to count tokens for an invocation.
type CountTokensRequest struct {
	InstanceID string
	ModelID    string
	Invocation Invocation
}

// CountTokensResponse returns count evidence with presence.
type CountTokensResponse struct {
	InputTokens     *int64
	Presence        UsagePresence
	EvidenceQuality string
}

// FinalizeBillingRequest finalizes billing for an immutable attempt lineage.
type FinalizeBillingRequest struct {
	InstanceID     string
	ALegID         string
	BLegID         string
	ModelID        string
	Reason         string
	IdempotencyKey string
}

// FinalizeBillingResponse returns billing/usage evidence.
type FinalizeBillingResponse struct {
	Usage           UsageEvidence
	EvidenceQuality string
	// AccountingV2 retains every provider observation returned by the finalizer.
	// It is host-only and may include multiple charge events for one attempt.
	AccountingV2 []AccountingEvidenceV2
}

// PluginError is a classified plugin error.
type PluginError struct {
	Code            ErrorCode
	Message         string
	Retryable       bool
	OutputCommitted bool
}

// CancelOutcome reports cancellation handling.
type CancelOutcome struct {
	Acknowledged bool
	Detail       string
	Reason       CancelReason
	Mode         CancelMode
}

// Terminal ends an execute attempt exactly once.
type Terminal struct {
	Status TerminalStatus
	Error  *PluginError
}

// CanonicalEvent is one ordered stream event payload (sequence lives on the frame).
type CanonicalEvent struct {
	Kind             EventKind
	MessageIndex     *int32
	Delta            *string
	Signature        *string
	Opaque           []byte
	ToolCallID       *string
	ToolName         *string
	Usage            *UsageEvidence
	Warning          *string
	Error            *PluginError
	ImageRef         *string
	FileRef          *string
	ReasoningDialect *string
	ReasoningOpaque  []byte

	// ReasoningSummary, ReasoningContent, and ReasoningEncryptedContent preserve
	// exact OpenAI Responses reasoning-item fields with absent/null/value presence.
	// Additive v1.3 fields gated by FeatureExactOpenResponsesFields.
	ReasoningSummary          RawJSON
	ReasoningContent          RawJSON
	ReasoningEncryptedContent RawJSON

	// FinishReason is optional terminal taxonomy on response_finished (vendor
	// stop/finish reason). Nil/absent means a legacy producer left terminal
	// semantics to frontend inference (mirrors aipapi.Event presence).
	FinishReason *string
	// ResponseStatus is explicit terminal status on response_finished
	// ("completed" or "incomplete"). Nil/absent means legacy inference.
	ResponseStatus *string
}

// ClientFrame is a host-to-plugin Execute frame.
type ClientFrame struct {
	Kind                 ClientFrameKind
	Sequence             uint64
	InstanceID           string
	Invocation           *Invocation
	CancelReason         CancelReason
	CancelDeadlineUnixMS int64
}

// ServerFrame is a plugin-to-host Execute frame.
type ServerFrame struct {
	Kind          ServerFrameKind
	Sequence      uint64
	Event         *CanonicalEvent
	Diagnostic    string
	CancelOutcome *CancelOutcome
	Terminal      *Terminal
	Accounting    *AccountingEvidence
	// AccountingV2 is the additive typed V2 sideband. It is never a canonical
	// client event and is legal only after explicit feature negotiation.
	AccountingV2           *AccountingEvidenceV2
	PromptCacheObservation *promptcache.Observation
}

// AccountingEvidence is host-only provider billing evidence and is never a
// canonical stream event. DedupeKey identifies one provider charge.
// InputTokensExcludeCacheRead declares that cache reads are not contained in
// the input quantity; the zero value retains the legacy inclusive-input
// interpretation for uncached-input projections. InputTokensExcludeCacheWrite
// declares the same relation for cache writes and is independent of the read
// declaration.
type AccountingEvidence struct {
	InputTokens                  *int64
	OutputTokens                 *int64
	CacheReadTokens              *int64
	CacheWriteTokens             *int64
	ReasoningTokens              *int64
	TotalTokens                  *int64
	Presence                     UsagePresence
	Source                       AccountingSource
	Authority                    AccountingAuthority
	Plane                        AccountingPlane
	DedupeKey                    string
	InputTokensExcludeCacheRead  bool
	InputTokensExcludeCacheWrite bool
}

// AccountingEvidenceSource is the internal seam used to drain host-only
// evidence without yielding it from the canonical EventStream.
type AccountingEvidenceSource interface {
	DrainAccountingEvidence() []AccountingEvidence
}

// AccountingEvidenceV2Source is the host-only drain seam for typed economic
// observations. Implementations must return copies and retain no caller-owned
// mutable buffers.
type AccountingEvidenceV2Source interface {
	DrainAccountingEvidenceV2() []AccountingEvidenceV2
}

// EconomicEvidenceSource is a neutral alias for AccountingEvidenceV2Source.
type EconomicEvidenceSource interface {
	DrainEconomicEvidence() []AccountingEvidenceV2
}

// HealthResponse reports plugin health.
type HealthResponse struct {
	Serving bool
	Detail  string
}

// GracefulShutdownRequest asks the plugin to drain.
type GracefulShutdownRequest struct {
	DrainTimeoutMS int64
}

// GracefulShutdownResponse acknowledges shutdown.
type GracefulShutdownResponse struct {
	Accepted bool
}
