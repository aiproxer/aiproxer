// Package openresponses is an independent OpenResponses reference client emulator.
//
// It implements black-box client behavior for the OpenResponses 2026-04-24 profile:
// HTTP JSON create, SSE create, standalone compaction, and persistent sequential
// WebSocket turns with continuation and errors. It parses tools, multimodal content,
// assistant phase, reasoning and item lifecycle, prefixed extensions, required
// response presence, cancellation, and slow-consumer behavior.
//
// Independence: this package is test-only support. It MUST NOT import production
// OpenResponses protocol, frontend, backend, or state-machine packages, and it must
// not reuse their wire structs or parsers. Only stdlib plus github.com/gorilla/websocket
// are used here. The immutable official fixtures under testdata/ are the only
// protocol inputs shared with production.
package openresponses

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ScenarioKind classifies a declarative emulator scenario.
type ScenarioKind string

const (
	ScenarioJSONText     ScenarioKind = "json_text"
	ScenarioSSEText      ScenarioKind = "sse_text"
	ScenarioTools        ScenarioKind = "tools"
	ScenarioMultimodal   ScenarioKind = "multimodal"
	ScenarioReasoning    ScenarioKind = "reasoning"
	ScenarioPhase        ScenarioKind = "phase"
	ScenarioContinuation ScenarioKind = "continuation"
	ScenarioCompaction   ScenarioKind = "compaction"
	ScenarioExtensions   ScenarioKind = "extensions"
	ScenarioWebSocket    ScenarioKind = "websocket"
	ScenarioNegative     ScenarioKind = "negative_validation"
	ScenarioAdversarial  ScenarioKind = "adversarial"
)

// ScenarioDescriptor declares a named scenario bound to client emulator behavior.
type ScenarioDescriptor struct {
	ID          string
	Kind        ScenarioKind
	Description string
}

// Validate enforces declarative scenario hygiene.
func (s ScenarioDescriptor) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("scenario id cannot be empty")
	}
	switch s.Kind {
	case ScenarioJSONText, ScenarioSSEText, ScenarioTools, ScenarioMultimodal,
		ScenarioReasoning, ScenarioPhase, ScenarioContinuation, ScenarioCompaction,
		ScenarioExtensions, ScenarioWebSocket, ScenarioNegative, ScenarioAdversarial:
	default:
		return fmt.Errorf("unknown scenario kind %q", s.Kind)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("scenario %q description cannot be empty", s.ID)
	}
	return nil
}

// Portable item discriminator constants.
const (
	ItemMessage            = "message"
	ItemFunctionCall       = "function_call"
	ItemFunctionCallOutput = "function_call_output"
	ItemReasoning          = "reasoning"
	ItemItemReference      = "item_reference"
	ItemCompaction         = "compaction"
)

// ReasoningItem carries provider-controlled reasoning payloads. Encrypted content is
// opaque and never interpreted by the client.
type ReasoningItem struct {
	Content             []ContentPart
	EncryptedContent    string
	EncryptedContentSet bool
	Summary             []ContentPart
}

// Item is the discriminated ordered unit of context on the OpenResponses wire.
type Item struct {
	Type             string
	ID               string
	Status           string
	Role             string
	Phase            string
	Content          []ContentPart
	CallID           string
	Name             string
	Arguments        string
	Output           json.RawMessage
	Reasoning        *ReasoningItem
	EncapsulatedID   string
	EncryptedContent string
	Opaque           json.RawMessage
}

// IsExtension reports whether the item uses a prefixed implementor slug.
func (it Item) IsExtension() bool { return strings.Contains(it.Type, ":") }

// OpaqueItem returns the raw preserved bytes for extension items, nil otherwise.
func (it Item) OpaqueItem() json.RawMessage {
	if !it.IsExtension() {
		return nil
	}
	return it.Opaque
}

// ContentPart is a discriminated content part inside a message.
type ContentPart struct {
	Type        string
	Text        string
	Refusal     string
	Summary     string
	Filename    string
	ImageURL    json.RawMessage
	FileURL     json.RawMessage
	FileData    json.RawMessage
	VideoURL    json.RawMessage
	Annotations json.RawMessage
	Logprobs    json.RawMessage
	Opaque      json.RawMessage
}

// IsExtension reports whether the content part uses a prefixed implementor slug.
func (p ContentPart) IsExtension() bool { return strings.Contains(p.Type, ":") }

// Tool is a function tool or a prefixed hosted tool.
type Tool struct {
	Type        string
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
	Opaque      json.RawMessage
}

// IsExtension reports whether the tool uses a prefixed implementor slug.
func (t Tool) IsExtension() bool { return strings.Contains(t.Type, ":") }

// Usage carries token counters.
type Usage struct {
	InputTokens     int
	CachedTokens    int
	OutputTokens    int
	ReasoningTokens int
	TotalTokens     int
}

// ErrorObject is the structured OpenResponses error payload.
type ErrorObject struct {
	Type    string
	Code    string
	Message string
	Param   string
}

var (
	_ json.Marshaler   = Item{}
	_ json.Unmarshaler = (*Item)(nil)
	_ json.Marshaler   = ContentPart{}
	_ json.Unmarshaler = (*ContentPart)(nil)
	_ json.Marshaler   = Tool{}
	_ json.Unmarshaler = (*Tool)(nil)
	_ json.Marshaler   = ReasoningItem{}
	_ json.Unmarshaler = (*ReasoningItem)(nil)
	_ io.Reader        = (*boundedReader)(nil)
)
