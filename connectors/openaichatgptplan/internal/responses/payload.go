// Package responses projects canonical calls onto the public OpenAI Responses
// API used for ChatGPT plan usage.
//
// The package is provider-local by construction. Nothing here is a canonical
// or core contract: the canonical side of the seam is pkg/aipapi.Call, and
// the wire side is the documented public Responses request. No OpenAI-specific
// field, switch, or option is added to pkg/aipapi or to generic core, so the
// preview policy stays where the provider semantics live.
//
// The public documentation is the authority for the wire contract:
//
//   - https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
//   - https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations
//
// The canonical-to-wire projection is implemented here (spec task 4.2) and the
// preview request policy is layered on top of it (spec task 4.3, policy.go).
// The projection itself is deterministic and provider-local: instructions,
// input items, the function catalogue, the tool choice and the reasoning
// effort. It does not decide which unsupported top-level fields the policy
// admits; AdmitCall owns that admission, and it runs before any network
// execution.
package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// PublicBaseURL is the documented public origin for ChatGPT plan usage.
//
// The documentation is explicit that plan usage goes to the public
// https://api.openai.com/v1 endpoints and never to ChatGPT's private
// backend-api routes. The origin is therefore a constant of this package and
// not an operator setting: the connector talks to the public API or it does
// not talk at all, and no configuration can point it at a private route.
const PublicBaseURL = "https://api.openai.com"

// ResponsesPath is the documented public Responses path.
//
// The documentation names POST /v1/responses as the only inference route for
// this flow, and the preview limitations state that another route is not
// permission to use it.
const ResponsesPath = "/v1/responses"

// UserAgent identifies this connector truthfully.
//
// The private Codex transport identified itself as the Codex CLI. The public
// route must not impersonate another vendor's client, so the value names the
// product that actually sends the request. It carries no version: the
// connector does not know one, and a version it cannot verify would be a
// lie in a different direction.
const UserAgent = "AIProxer"

// WireRequest is the fully resolved description of one upstream Responses
// request.
//
// It carries the public endpoint, the exact header set, and the exact JSON
// body. The credential is deliberately absent: the selected profile's access
// token is added by the transport, so no secret can reach the projection and
// no fixture can capture one.
type WireRequest struct {
	// Method is the HTTP method of the request.
	Method string
	// URL is the absolute public endpoint of the request.
	URL string
	// Headers is the exact header set the transport must send, except for the
	// credential header the transport adds.
	Headers http.Header
	// Body is the exact JSON request document.
	Body []byte
}

// ProjectInput carries everything the canonical-to-wire projection needs.
//
// Call is the canonical request and Model is the provider model id selected
// for the attempt. The downstream delivery mode travels inside
// Call.Invocation and must not change the request: the public route always
// streams, and the downstream mode is a client-side collection concern.
//
// The call is handed over by value and is read-only for the projection: a
// projection that rewrites the canonical request must clone it first, so a
// caller's canonical state cannot change under it.
type ProjectInput struct {
	// Call is the canonical request to project.
	Call aipapi.Call
	// Model is the provider model id for the attempt.
	Model string
}

// ErrProjectionUnimplemented reports that the canonical-to-wire projection is
// not implemented yet.
//
// It is retained for the task 4.1 contract gate, which probes this seam for
// the sentinel to decide whether the projection exists. The gate opens on the
// implementation state and not on a stored switch, so the implemented
// projector (spec task 4.2) simply never returns this sentinel and needs no
// gate removal. No production caller treats it as a policy decision.
var ErrProjectionUnimplemented = errors.New("responses: the canonical-to-wire projection is not implemented")

// ErrCallNotRepresentable reports that the canonical call carries a form the
// public Responses request cannot represent lawfully.
//
// It is the projection's refusal vocabulary, not a preview-policy decision:
// spec task 4.3 owns which supported request the policy admits, and this seam
// refuses only what it cannot project at all. A refusal is always explicit -
// the projector never drops an arrived item to make a request look projectable.
var ErrCallNotRepresentable = errors.New("responses: the canonical call has no lawful public Responses representation")

// ProjectionReason identifies a stable projection refusal cause.
type ProjectionReason string

const (
	// ProjectionReasonModelRequired reports an attempt without a model id.
	ProjectionReasonModelRequired ProjectionReason = "model_required"
	// ProjectionReasonDecision reports a typed decision request, which is not
	// a conversation and has no Responses input.
	ProjectionReasonDecision ProjectionReason = "decision_request"
	// ProjectionReasonPreviousResponse reports a proxy-owned continuation
	// whose history is not carried by the call.
	ProjectionReasonPreviousResponse ProjectionReason = "previous_response_id"
	// ProjectionReasonSemanticExtension reports residual semantics with no
	// public wire carrier.
	ProjectionReasonSemanticExtension ProjectionReason = "semantic_extension"
	// ProjectionReasonInstructions reports instruction content that cannot be
	// expressed as the instructions string.
	ProjectionReasonInstructions ProjectionReason = "instructions_not_text"
	// ProjectionReasonSystemContent reports system conversation content that
	// cannot be expressed in the instructions string.
	ProjectionReasonSystemContent ProjectionReason = "system_content_not_text"
	// ProjectionReasonRole reports a conversation role with no legal position
	// in the public input.
	ProjectionReasonRole ProjectionReason = "role_not_projectable"
	// ProjectionReasonPart reports a content part with no legal public input
	// representation.
	ProjectionReasonPart ProjectionReason = "part_not_projectable"
	// ProjectionReasonAssistantMedia reports media on an assistant message,
	// which has no documented input representation.
	ProjectionReasonAssistantMedia ProjectionReason = "assistant_media"
	// ProjectionReasonItemKind reports an item kind with no public carrier.
	ProjectionReasonItemKind ProjectionReason = "item_kind_not_projectable"
	// ProjectionReasonItemReference reports a provider item reference the HTTP
	// route cannot resolve.
	ProjectionReasonItemReference ProjectionReason = "item_reference"
	// ProjectionReasonReasoningReplay reports reasoning history; spec task 8.2
	// owns optional reasoning replay.
	ProjectionReasonReasoningReplay ProjectionReason = "reasoning_replay"
	// ProjectionReasonToolCall reports a tool call whose identity cannot be
	// taken from exactly one authority.
	ProjectionReasonToolCall ProjectionReason = "tool_call_not_projectable"
	// ProjectionReasonToolResult reports a tool result with no pinned wire
	// shape.
	ProjectionReasonToolResult ProjectionReason = "tool_result_not_projectable"
	// ProjectionReasonFileReference reports a file reference whose bytes are
	// not inline.
	ProjectionReasonFileReference ProjectionReason = "file_reference_not_inline"
	// ProjectionReasonToolChoiceSubset reports the canonical allowed-tools
	// subset, which has no pinned wire shape. This refusal is owned here, not
	// by the preview policy: AdmitCall propagates it unchanged and never widens
	// the allowed tools to make a call admissible.
	ProjectionReasonToolChoiceSubset ProjectionReason = "tool_choice_subset"
	// ProjectionReasonAssistantPhase reports the assistant phase annotation,
	// which has no pinned wire shape on this route.
	ProjectionReasonAssistantPhase ProjectionReason = "assistant_phase"
)

// ProjectionError is one deterministic projection refusal.
//
// Reason and Field are the stable, developer-facing identities of the
// refusal. Detail is bounded context and never carries conversation content:
// a refusal diagnostic must not become a transcript sink.
type ProjectionError struct {
	Reason ProjectionReason
	Field  string
	Detail string
}

// Error renders the reason and the canonical field, never the content.
func (e *ProjectionError) Error() string {
	if e == nil {
		return ErrCallNotRepresentable.Error()
	}
	msg := ErrCallNotRepresentable.Error() + ": " + string(e.Reason)
	if e.Field != "" {
		msg += " at " + e.Field
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap reports the projection refusal class.
func (e *ProjectionError) Unwrap() error { return ErrCallNotRepresentable }

// projectionError constructs one refusal.
func projectionError(reason ProjectionReason, field, detail string) error {
	return &ProjectionError{Reason: reason, Field: field, Detail: detail}
}

// ProjectCall projects one canonical call into the public Responses request.
//
// The projection is deterministic: the same canonical call and model produce
// the same method, endpoint, header set and body bytes. The downstream
// delivery mode is not read at all, so it cannot change the upstream request:
// the public route always streams, and a non-streaming client is a downstream
// collection concern.
//
// The canonical call is read through its own authority. Call.Items wins when
// the item authority is present, and Call.Instructions/Call.Messages otherwise;
// the projector never rebuilds one authority from the other.
//
// A call the public route cannot represent is refused with a ProjectionError
// and never projected partially. Spec task 4.3 adds the preview request policy
// on top of this seam; this function owns only the instruction, input and tool
// projection.
func ProjectCall(in ProjectInput) (WireRequest, error) {
	if strings.TrimSpace(in.Model) == "" {
		return WireRequest{}, projectionError(ProjectionReasonModelRequired, "Model",
			"the provider model id is required")
	}
	call := in.Call
	if err := call.Validate(); err != nil {
		return WireRequest{}, fmt.Errorf("responses: the canonical call is invalid: %w", err)
	}
	if err := checkProjectableEnvelope(call); err != nil {
		return WireRequest{}, err
	}
	instructions, err := projectInstructions(call)
	if err != nil {
		return WireRequest{}, err
	}
	input, err := projectInput(call)
	if err != nil {
		return WireRequest{}, err
	}
	body, err := json.Marshal(wireRequestDocument{
		Model:        in.Model,
		Instructions: instructions,
		Input:        input,
		ToolChoice:   projectToolChoice(call.ToolChoice),
		Reasoning:    projectReasoning(call.Options),
		// The two transport flags are owned by the preview policy
		// (policy.go), so the projector and the wire-side guard cannot
		// disagree about them.
		Store:  previewStore,
		Stream: previewStream,
	})
	if err != nil {
		return WireRequest{}, fmt.Errorf("responses: the projected request cannot be encoded: %w", err)
	}
	headers := make(http.Header, 3)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", UserAgent)
	return WireRequest{
		Method:  http.MethodPost,
		URL:     PublicBaseURL + ResponsesPath,
		Headers: headers,
		Body:    body,
	}, nil
}

// checkProjectableEnvelope refuses call-level forms the projection cannot
// carry.
//
// These are not preview-policy limits. A typed decision is not a conversation;
// a proxy continuation names history the call does not carry; a semantic
// residual has no public field; and the allowed-tools subset has no pinned
// wire shape. Each refusal is owned here: the preview policy propagates it
// unchanged rather than omitting the value (which would widen tool access) or
// reclassifying it as an ordinary policy omission.
func checkProjectableEnvelope(call aipapi.Call) error {
	switch {
	case call.Decision != nil:
		return projectionError(ProjectionReasonDecision, "Decision",
			"a typed decision request is not a conversation")
	case call.PreviousResponseID != "":
		return projectionError(ProjectionReasonPreviousResponse, "PreviousResponseID",
			"the public HTTP route has no previous_response_id; the request must carry its history")
	case len(call.SemanticExtensions) > 0:
		return projectionError(ProjectionReasonSemanticExtension, "SemanticExtensions",
			"the public route has no carrier for semantic residuals")
	case len(call.ToolChoice.AllowedTools) > 0:
		return projectionError(ProjectionReasonToolChoiceSubset, "ToolChoice.AllowedTools",
			"the public route's tool choice has no subset form")
	}
	return nil
}

// wireRequestDocument is the exact public Responses request body.
//
// The field order is fixed by this struct so the same canonical call always
// produces the same bytes. `store` and `stream` are mandatory on this route
// and are never taken from the canonical call.
type wireRequestDocument struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions,omitempty"`
	Input        []any          `json:"input"`
	ToolChoice   any            `json:"tool_choice,omitempty"`
	Reasoning    *wireReasoning `json:"reasoning,omitempty"`
	Store        bool           `json:"store"`
	Stream       bool           `json:"stream"`
}

// wireReasoning is the request-side reasoning control.
type wireReasoning struct {
	Effort string `json:"effort"`
}

// wireFunctionToolChoice is a forced function reference.
type wireFunctionToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// projectToolChoice maps the canonical tool choice without changing its
// meaning.
//
// An unset mode sends no tool_choice key at all. The explicit automatic mode
// is "auto", the explicit no-tool mode is "none", the "any" mode is
// "required", and the required mode with a name is a function reference.
func projectToolChoice(choice aipapi.ToolChoice) any {
	switch choice.Mode {
	case "":
		return nil
	case aipapi.ToolChoiceAuto:
		return "auto"
	case aipapi.ToolChoiceNone:
		return "none"
	case aipapi.ToolChoiceAny:
		return "required"
	case aipapi.ToolChoiceRequired:
		return wireFunctionToolChoice{Type: "function", Name: choice.Name}
	}
	// An unknown mode is already refused by aipapi.Call.Validate.
	return nil
}

// projectReasoning maps the canonical reasoning effort when it is set.
//
// The other generation options are deliberately absent: the preview request
// policy (policy.go) decides whether a request carrying them is refused or
// served with an explicit, recorded omission, and the projector never makes
// that choice silently.
func projectReasoning(options aipapi.GenerationOptions) *wireReasoning {
	if options.ReasoningEffort == "" {
		return nil
	}
	return &wireReasoning{Effort: options.ReasoningEffort}
}

// projectInstructions folds the instruction text and the explicit system
// conversation text into the one instructions string.
//
// A text part contributes its text verbatim. A message's block is its text
// parts joined with "\n"; a block whose trimmed text is empty contributes
// nothing. The blocks are joined with "\n\n": the instruction blocks first, in
// Call.Instructions order, and then the system blocks, in conversation order,
// wherever they sit in the conversation. No system-role item is emitted as an
// input item - the public route rejects it.
func projectInstructions(call aipapi.Call) (string, error) {
	blocks := make([]string, 0, len(call.Instructions)+len(call.Messages))
	appendBlock := func(block string) {
		if block != "" {
			blocks = append(blocks, block)
		}
	}
	for i, msg := range call.Instructions {
		block, err := textBlockFromParts(msg.Parts, fmt.Sprintf("Instructions[%d]", i), ProjectionReasonInstructions)
		if err != nil {
			return "", err
		}
		appendBlock(block)
	}
	for i, msg := range call.Messages {
		if msg.Role != aipapi.RoleSystem {
			continue
		}
		block, err := textBlockFromParts(msg.Parts, fmt.Sprintf("Messages[%d]", i), ProjectionReasonSystemContent)
		if err != nil {
			return "", err
		}
		appendBlock(block)
	}
	for i, item := range call.Items {
		if item.Kind != aipapi.ItemKindMessage || item.Role != aipapi.RoleSystem {
			continue
		}
		block, err := textBlockFromContent(item.Content, fmt.Sprintf("Items[%d]", i))
		if err != nil {
			return "", err
		}
		appendBlock(block)
	}
	return strings.Join(blocks, "\n\n"), nil
}

// textBlockFromParts renders one message-authority message as an instruction
// block.
func textBlockFromParts(parts []aipapi.Part, field string, reason ProjectionReason) (string, error) {
	texts := make([]string, 0, len(parts))
	for i, part := range parts {
		if part.Kind != aipapi.PartText {
			return "", projectionError(reason, fmt.Sprintf("%s.Parts[%d]", field, i),
				fmt.Sprintf("part kind %q cannot be expressed in the instructions string", part.Kind))
		}
		texts = append(texts, part.Text)
	}
	return joinTextBlock(texts), nil
}

// textBlockFromContent renders one item-authority message item as an
// instruction block.
func textBlockFromContent(parts []aipapi.ContentPart, field string) (string, error) {
	texts := make([]string, 0, len(parts))
	for i, part := range parts {
		if part.Kind != aipapi.ContentPartText {
			return "", projectionError(ProjectionReasonSystemContent, fmt.Sprintf("%s.Content[%d]", field, i),
				fmt.Sprintf("content part kind %q cannot be expressed in the instructions string", part.Kind))
		}
		texts = append(texts, part.Text)
	}
	return joinTextBlock(texts), nil
}

// joinTextBlock joins one message's text parts and drops a whitespace-only
// block. The text itself is never trimmed: it contributes verbatim.
func joinTextBlock(texts []string) string {
	if len(texts) == 0 {
		return ""
	}
	block := strings.Join(texts, "\n")
	if strings.TrimSpace(block) == "" {
		return ""
	}
	return block
}
