package aipapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Role identifies who produced a message in the canonical turn sequence.
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// SessionRef carries client hints, core continuity identifiers, and optional proxy-owned session authority.
//
// ClientSessionID, ContinuityKey, and ALegID are hints or correlation values unless validated through
// proxy-owned secure-session state. AuthoritativeSessionID is the proxy-owned session id when issued
// by the secure session layer. ResumeToken is a bearer resume proof; it must never be forwarded to backends
// or persisted raw by adapters—only validated via secure-session fingerprints.
//
// JSON name remains "SessionID" for wire compatibility.
type SessionRef struct {
	ClientSessionID        string
	ContinuityKey          string
	ALegID                 string
	AuthoritativeSessionID string `json:"SessionID,omitempty"`
	ResumeToken            string
	// Metadata carries validated client session metadata. It is never treated as
	// proxy authority unless a separate trusted carrier establishes that field.
	Metadata map[string]string `json:"-"`
}

// CorrelationID returns a stable identifier for diagnostics and traffic capture: authoritative id when set, otherwise the client hint.
func (s SessionRef) CorrelationID() string {
	if x := strings.TrimSpace(s.AuthoritativeSessionID); x != "" {
		return x
	}
	return strings.TrimSpace(s.ClientSessionID)
}

// RouteIntent captures routing input produced by a frontend decoder.
// The planner owns interpretation; this stays an opaque intent string at the API layer.
type RouteIntent struct {
	Selector string
}

// Message is one ordered turn in the conversation.
type Message struct {
	Role  Role
	Parts []Part
	// Metadata carries proxy-owned message-level traceability (for example
	// interleaved-thinking injection markers). It is never serialized to wire
	// payloads and never treated as client input.
	Metadata map[string]string `json:"-"`
}

// Call is the canonical request envelope shared across frontends.
type Call struct {
	ID           string
	Session      SessionRef
	Route        RouteIntent
	Instructions []Message
	Messages     []Message
	Items        []Item
	// Decision is exclusive request authority for typed evaluations.
	Decision *DecisionRequest `json:",omitempty"`
	// PreviousResponseID identifies a proxy-owned continuation parent. It allows
	// an item-authoritative continuation request to carry an intentionally empty
	// input item slice; the continuation resolver supplies the materialized items.
	PreviousResponseID string
	// PromptCacheKey is a proxy-carried prompt-caching hint for the remote
	// OpenResponses endpoint. It is protocol-neutral metadata (never a canonical
	// trajectory control); the OpenResponses backend forwards it on compact
	// requests so a schema-permitted client hint is never silently dropped.
	PromptCacheKey string
	// SemanticExtensions carries bounded residual semantics whose identity and
	// presence participate in admission. PromptCacheKey is a source-compatible
	// alias; when both forms are present they must agree.
	SemanticExtensions []SemanticExtension
	Tools              []ToolDef
	ToolChoice         ToolChoice
	Options            GenerationOptions
	// Extensions carries proxy-owned residual metadata. Keys in the reserved
	// "aip." namespace are set only by proxy code and are never populated from
	// client input; frontends must strip such keys from client-supplied data
	// and consumers must not treat them as client assertions.
	Extensions map[string]json.RawMessage
	Invocation Invocation `json:"-"`

	// MaxPendingWireEvents caps backend adapter-internal pending event queues per stream (0 = unlimited).
	// Not client API; the core executor sets this from server config when non-zero.
	MaxPendingWireEvents int `json:"-"`

	// StructuralCause is the proxy-owned, content-free label of a structural
	// change the proxy applied to this request, for example a compaction
	// rewrite. It is never client input, is never serialized to a
	// provider payload, and carries no content: a bounded label only. The
	// prompt-cache collapse diagnostic reads it to attribute a cache
	// discontinuity to the change that caused it.
	//
	// The first structural change recorded on a request wins: it is the
	// earliest change to the cached prefix, and every later change is
	// downstream of it.
	//
	// A transformation that reverses a proxy-owned change clears the label, so
	// the diagnostic never attributes a change the provider-bound request no
	// longer carries. The proxy cannot verify the change's presence without
	// inspecting content, which it deliberately does not do.
	//
	// The request publishes the label at most once per cache key: the change it
	// names happened once, so a later leg of the same request carries no
	// label.
	StructuralCause string `json:"-"`
}

// HasItemAuthority reports whether this call uses ordered item authority (non-nil Items slice).
func (c Call) HasItemAuthority() bool {
	return c.Items != nil
}

// Validate checks canonical invariants and unsupported combinations for this call.
func (c Call) Validate() error {
	if c.Decision != nil {
		if len(c.Messages) > 0 || len(c.Instructions) > 0 || c.Items != nil || c.PreviousResponseID != "" ||
			len(c.Tools) > 0 || c.ToolChoice.Mode != "" || c.ToolChoice.Name != "" || len(c.ToolChoice.AllowedTools) > 0 {
			return &ValidationError{Field: "Decision", Message: "conflicting decision and message, item, continuation or tool authorities"}
		}
		if err := c.validateEnvelopeSizes(); err != nil {
			return err
		}
		if err := c.Decision.Validate(); err != nil {
			return err
		}
	} else if c.HasItemAuthority() {
		if len(c.Messages) > 0 || len(c.Instructions) > 0 {
			return &ValidationError{Field: "Items", Message: "conflicting raw item and legacy message authorities"}
		}
		if len(c.Items) == 0 && strings.TrimSpace(c.PreviousResponseID) == "" {
			return &ValidationError{Field: "Items", Message: "at least one item is required unless previous_response_id is set"}
		}
		if err := c.validateEnvelopeSizes(); err != nil {
			return err
		}
		allItemIDs := make(map[string]int, len(c.Items))
		for i, item := range c.Items {
			if item.ID != "" {
				if _, exists := allItemIDs[item.ID]; exists {
					return &ValidationError{Field: itemField(i).withSuffix(".ID").String(), Message: fmt.Sprintf("duplicate item ID %q", item.ID)}
				}
				allItemIDs[item.ID] = i
			}
		}

		seenCallIDs := make(map[string]bool, len(c.Items))
		var reasoningBytes int64

		for i, item := range c.Items {
			path := itemField(i)
			if err := item.validate(path); err != nil {
				return err
			}
			if item.Kind == ItemKindItemReference {
				if idx, exists := allItemIDs[item.Reference.ID]; exists && idx > i {
					return &ValidationError{Field: path.withSuffix(".Reference.ID").String(), Message: fmt.Sprintf("forward item reference to %q", item.Reference.ID)}
				}
			}
			if item.Kind == ItemKindToolCall {
				if seenCallIDs[item.ToolCall.CallID] {
					return &ValidationError{Field: path.withSuffix(".ToolCall.CallID").String(), Message: fmt.Sprintf("duplicate call ID %q", item.ToolCall.CallID)}
				}
				seenCallIDs[item.ToolCall.CallID] = true
			}
			if item.Kind == ItemKindToolResult {
				if !seenCallIDs[item.ToolResult.CallID] {
					return &ValidationError{Field: path.withSuffix(".ToolResult.CallID").String(), Message: fmt.Sprintf("orphan tool result for call ID %q", item.ToolResult.CallID)}
				}
			}

			if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
				reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(item.Reasoning.Reasoning)))
			}
			for _, cp := range item.Content {
				if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
					reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(cp.Reasoning)))
				}
			}
			if item.ToolResult != nil {
				for _, cp := range item.ToolResult.Parts {
					if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
						reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(cp.Reasoning)))
					}
				}
			}
			if reasoningBytes > int64(MaxReasoningBytesPerCall) {
				return &ValidationError{
					Field:   path.String(),
					Message: fmt.Sprintf("total reasoning payload exceeds %d bytes", MaxReasoningBytesPerCall),
				}
			}
		}
	} else {
		if len(c.Messages) == 0 {
			return &ValidationError{Field: "Messages", Message: "at least one message is required"}
		}
		if err := c.validateEnvelopeSizes(); err != nil {
			return err
		}
		var reasoningBytes int64
		for i, m := range c.Messages {
			if m.Role == "" {
				return &ValidationError{Field: fmt.Sprintf("Messages[%d].Role", i), Message: "role is required"}
			}
			if len(m.Parts) == 0 {
				return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts", i), Message: "at least one part is required"}
			}
			for j, p := range m.Parts {
				if err := p.validate(); err != nil {
					return &ValidationError{Field: fmt.Sprintf("Messages[%d].Parts[%d]", i, j), Message: err.Error()}
				}
				if p.Kind == PartReasoning {
					if m.Role != RoleAssistant {
						return &ValidationError{
							Field:   fmt.Sprintf("Messages[%d].Parts[%d]", i, j),
							Message: "reasoning parts are only allowed on assistant messages",
						}
					}
					reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(p.Reasoning)))
					if reasoningBytes > int64(MaxReasoningBytesPerCall) {
						return &ValidationError{
							Field:   fmt.Sprintf("Messages[%d].Parts[%d]", i, j),
							Message: fmt.Sprintf("total reasoning payload exceeds %d bytes", MaxReasoningBytesPerCall),
						}
					}
				}
			}
		}
		for i, m := range c.Instructions {
			if m.Role == "" {
				return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Role", i), Message: "role is required"}
			}
			if len(m.Parts) == 0 {
				return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts", i), Message: "at least one part is required"}
			}
			for j, p := range m.Parts {
				if err := p.validate(); err != nil {
					return &ValidationError{Field: fmt.Sprintf("Instructions[%d].Parts[%d]", i, j), Message: err.Error()}
				}
				if p.Kind == PartReasoning {
					if m.Role != RoleAssistant {
						return &ValidationError{
							Field:   fmt.Sprintf("Instructions[%d].Parts[%d]", i, j),
							Message: "reasoning parts are only allowed on assistant messages",
						}
					}
					reasoningBytes = addSaturatingInt64(reasoningBytes, int64(ReasoningPayloadBytes(p.Reasoning)))
					if reasoningBytes > int64(MaxReasoningBytesPerCall) {
						return &ValidationError{
							Field:   fmt.Sprintf("Instructions[%d].Parts[%d]", i, j),
							Message: fmt.Sprintf("total reasoning payload exceeds %d bytes", MaxReasoningBytesPerCall),
						}
					}
				}
			}
		}
	}
	for i, t := range c.Tools {
		if t.Name == "" {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Name", i), Message: "tool name is required"}
		}
		if len(t.Parameters) > 0 && !json.Valid(t.Parameters) {
			return &ValidationError{Field: fmt.Sprintf("Tools[%d].Parameters", i), Message: "parameters must be valid JSON when set"}
		}
	}
	if err := c.ToolChoice.validate(len(c.Tools), c.Tools); err != nil {
		return err
	}
	if err := c.Options.validate(); err != nil {
		return err
	}
	if _, err := c.PromptCacheKeyValue(); err != nil {
		return err
	}
	return nil
}

func addSaturatingInt64(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// SaturatingAddInt64 returns a+b clamped at math.MaxInt64 (negatives treated as 0).
func SaturatingAddInt64(a, b int64) int64 { return addSaturatingInt64(a, b) }

// CallReasoningPayloadBytes returns the saturating sum of reasoning Text+Signature+Opaque
// lengths across the normalized call trajectory (dialect and non-reasoning content excluded).
func CallReasoningPayloadBytes(c *Call) int64 {
	if c == nil {
		return 0
	}
	var n int64
	for _, item := range NormalizedItems(*c) {
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
			n = addSaturatingInt64(n, int64(ReasoningPayloadBytes(item.Reasoning.Reasoning)))
		}
		for _, cp := range item.Content {
			if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
				n = addSaturatingInt64(n, int64(ReasoningPayloadBytes(cp.Reasoning)))
			}
		}
		if item.ToolResult != nil {
			for _, cp := range item.ToolResult.Parts {
				if cp.Kind == ContentPartReasoning && cp.Reasoning != nil {
					n = addSaturatingInt64(n, int64(ReasoningPayloadBytes(cp.Reasoning)))
				}
			}
		}
	}
	return n
}
