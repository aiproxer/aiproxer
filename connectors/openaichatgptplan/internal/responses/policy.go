package responses

// The centralized SIWC preview request policy.
//
// The public ChatGPT-plan preview contract is narrower than the Responses API:
// two transport flags are mandatory, a defined set of top-level fields and
// hosted tools is unsupported, HTTP must not carry previous_response_id, and
// several canonical controls have no documented wire shape on this route. The
// projection (payload.go, input.go) decides how a supported request is
// rendered; this file decides which request is admissible at all and which
// canonical values are omitted, and it makes every omission explicit.
//
// # Where the policy sits
//
// The policy sits AROUND the projector, not inside its per-item rules:
//
//  1. AdmitCall reads the canonical call and makes the preview decisions
//     (transport flags, previous-response history, generation options, cache
//     hints, protocol extensions) on a clone of the call.
//  2. It hands the decided call to ProjectCall, so the 4.2 projection rules
//     (instruction folding, chronology, carriers, identity bytes) stay the
//     only authority for how supported content is rendered.
//  3. It checks the projected request with AdmitProjectedRequest, which
//     enforces the wire-side preview contract: the two mandatory transport
//     flags, no previous_response_id, no documented unsupported top-level
//     field, and no unsupported (hosted) tool declaration.
//
// # No hidden degradation
//
// A supported request is served unchanged. A canonical value the public route
// cannot carry is either REFUSED with a typed PolicyError or OMITTED with an
// Omission that names the canonical field, the wire field it would have used,
// and a stable reason. The omissions travel back to the caller in
// Admission.Omissions, which is the logging/observability seam: the transport
// integration (spec task 5.3) records them, so an operator can see every
// degradation of an attempt instead of discovering it in provider behaviour.
//
// The default posture is interoperability: serve the request with explicit,
// observable omission, because a sampling hint or a cache hint is not worth
// breaking a session over. PolicyModeStrict is the operator's alternative: a
// call carrying any unsupported control is refused before projection. Both
// postures refuse the same things: a request whose history is missing, a
// hosted tool, and a projected document that would violate the wire contract.
// A materialized continuation is not a degradation in either posture - the
// call carries its full history, and only the proxy-owned id is dropped.
//
// # Provider-local
//
// Nothing here is a canonical or core contract. The policy consumes
// pkg/aipapi.Call and the connector's own WireRequest, and no OpenAI-specific
// field, switch or option is added to pkg/aipapi or to generic core.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// previewStore and previewStream are the two transport flags the public
// preview contract requires on every request.
//
// The policy owns them, and the projector reads them from here, so there is
// exactly one place that decides the transport posture. The values are
// constants, not configuration and not call data: a client cannot request
// storage, and the downstream delivery mode cannot turn the upstream stream
// off.
const (
	previewStore  = false
	previewStream = true
)

// ErrRequestNotAdmissible reports that the SIWC preview policy refused a
// request.
//
// It is the policy's refusal class. A caller can tell it apart from a
// canonical validation failure and from a projection refusal, so the frontend
// layer can map it to a stable provider-limitation error instead of a generic
// failure.
var ErrRequestNotAdmissible = errors.New("responses: the request is not admissible under the SIWC preview policy")

// ErrUnknownPolicyMode reports an unrecognized preview policy posture.
//
// An unknown posture is a configuration error, never a reason to fall back to
// a default: silently serving a strict operator's request with omissions would
// hide the degradation the operator asked to refuse.
var ErrUnknownPolicyMode = errors.New("responses: unknown SIWC preview policy mode")

// PolicyMode is the operator's preview admission posture.
type PolicyMode string

const (
	// PolicyModeInteroperable serves a request whose unsupported controls can
	// be omitted, and records every omission. It is the default posture: the
	// vision ranks session continuity above exact fidelity, and a sampling or
	// cache hint is not worth refusing a session over.
	PolicyModeInteroperable PolicyMode = "interoperable"
	// PolicyModeStrict refuses a request that carries any unsupported control,
	// instead of serving it with an omission. It never refuses a materialized
	// continuation: that call carries its full history, so serving it loses
	// nothing.
	PolicyModeStrict PolicyMode = "strict"
)

// ParsePolicyMode resolves the operator's configured preview posture.
//
// The empty posture is the documented default, not a guessed value: a caller
// that does not configure a posture gets the vision's default policy, and a
// caller that configures a posture the policy does not know is told so. The
// configuration boundary calls this so an unknown posture is refused at
// Configure instead of in the middle of an attempt.
func ParsePolicyMode(value string) (PolicyMode, error) {
	return PolicyMode(value).normalize()
}

// normalize resolves the posture and refuses an unknown one.
//
// The empty posture is the documented default, not a guessed value: a caller
// that does not configure a posture gets the vision's default policy, and a
// caller that configures a posture the policy does not know is told so.
func (m PolicyMode) normalize() (PolicyMode, error) {
	switch PolicyMode(strings.TrimSpace(string(m))) {
	case "", PolicyModeInteroperable:
		return PolicyModeInteroperable, nil
	case PolicyModeStrict:
		return PolicyModeStrict, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownPolicyMode, string(m))
	}
}

// PolicyReason identifies a stable preview-policy decision or refusal cause.
type PolicyReason string

const (
	// PolicyReasonUnsupportedField reports a documented unsupported top-level
	// Responses field.
	PolicyReasonUnsupportedField PolicyReason = "unsupported_field"
	// PolicyReasonUnsupportedTool reports a hosted tool class the preview
	// route does not support, or the top-level programmatic_tool_calling
	// control.
	PolicyReasonUnsupportedTool PolicyReason = "unsupported_tool"
	// PolicyReasonTransportFlag reports a projected request that does not
	// carry the mandatory transport flags.
	PolicyReasonTransportFlag PolicyReason = "transport_flag_not_forced"
	// PolicyReasonMalformedRequest reports a projected request that is not a
	// JSON object at all.
	PolicyReasonMalformedRequest PolicyReason = "malformed_request"
	// PolicyReasonPreviousResponseOnWire reports a projected request that
	// still carries previous_response_id.
	PolicyReasonPreviousResponseOnWire PolicyReason = "previous_response_id_on_wire"
	// PolicyReasonPreviousResponseMissingHistory reports a continuation whose
	// parent history the call does not carry. The route takes history in
	// input, so such a call cannot be served without inventing a conversation.
	PolicyReasonPreviousResponseMissingHistory PolicyReason = "previous_response_missing_history"
	// PolicyReasonPreviousResponseOmitted reports the proxy-owned continuation
	// id of a materialized call: the history is carried in input, and the id
	// itself is omitted.
	PolicyReasonPreviousResponseOmitted PolicyReason = "previous_response_id_omitted"
	// PolicyReasonGenerationOption reports a canonical generation option the
	// public route cannot carry.
	PolicyReasonGenerationOption PolicyReason = "generation_option_unsupported"
	// PolicyReasonCacheHint reports a prompt-cache hint the public route does
	// not accept.
	PolicyReasonCacheHint PolicyReason = "cache_hint_unsupported"
	// PolicyReasonProtocolExtension reports a protocol extension with no
	// public wire carrier.
	PolicyReasonProtocolExtension PolicyReason = "protocol_extension_unsupported"
)

// PolicyError is one deterministic preview-policy refusal.
//
// Reason and Field are the stable, developer-facing identities of the refusal.
// Detail is bounded context and never carries conversation content or a
// credential, so a refusal diagnostic cannot become a transcript sink.
type PolicyError struct {
	Reason PolicyReason
	Field  string
	Detail string
}

// Error renders the reason and the field, never the content.
func (e *PolicyError) Error() string {
	if e == nil {
		return ErrRequestNotAdmissible.Error()
	}
	msg := ErrRequestNotAdmissible.Error() + ": " + string(e.Reason)
	if e.Field != "" {
		msg += " at " + e.Field
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap reports the policy refusal class.
func (e *PolicyError) Unwrap() error { return ErrRequestNotAdmissible }

// policyError constructs one policy refusal.
func policyError(reason PolicyReason, field, detail string) error {
	return &PolicyError{Reason: reason, Field: field, Detail: detail}
}

// Omission is one explicit, observable degradation.
//
// Field is the canonical field that carried the value, Wire is the Responses
// field the request would have used when one exists, and Reason is the stable
// policy reason. An omission that is not returned here did not happen: the
// policy never drops an arrived value without recording it.
type Omission struct {
	Field  string
	Wire   string
	Reason PolicyReason
	Detail string
}

// Admission is the outcome of one preview-policy admission.
//
// Request is the legal public Responses request. Omissions lists every
// canonical value the policy left out, in a deterministic order. On a
// refusal, the zero Admission is returned: no WireRequest exists for a request
// the policy did not admit, so a caller cannot accidentally send one.
type Admission struct {
	Request   WireRequest
	Omissions []Omission
}

// generationOptionRule is one canonical generation option the public preview
// route cannot carry.
//
// Field is the canonical field name, Wire is the Responses field the request
// would have carried, and set reports whether the canonical call uses it. The
// table is the policy's whole generation-option surface: a new canonical
// option is added here once and every posture follows.
type generationOptionRule struct {
	field string
	wire  string
	set   func(aipapi.GenerationOptions) bool
}

// unsupportedGenerationOptions is the documented generation-option table.
//
// Temperature, top_p and max_output_tokens are the fields the preview
// limitations page names as unsupported. Verbosity, parallel tool calls and a
// response MIME type have no documented wire shape on this route (see the 4.1
// fixture contract's known gaps), so they cannot be forwarded either. The
// policy omits each one observably, or refuses it under PolicyModeStrict.
var unsupportedGenerationOptions = []generationOptionRule{
	{field: "Options.Temperature", wire: "temperature", set: func(o aipapi.GenerationOptions) bool { return o.Temperature != nil }},
	{field: "Options.TopP", wire: "top_p", set: func(o aipapi.GenerationOptions) bool { return o.TopP != nil }},
	{field: "Options.MaxOutputTokens", wire: "max_output_tokens", set: func(o aipapi.GenerationOptions) bool { return o.MaxOutputTokens != nil }},
	{field: "Options.Verbosity", wire: "text.verbosity", set: func(o aipapi.GenerationOptions) bool { return o.Verbosity != "" }},
	{field: "Options.ParallelToolCalls", wire: "parallel_tool_calls", set: func(o aipapi.GenerationOptions) bool { return o.ParallelToolCalls != nil }},
	{field: "Options.ResponseMIMEType", wire: "text.format", set: func(o aipapi.GenerationOptions) bool { return o.ResponseMIMEType != "" }},
}

// unsupportedTopLevelFields is the documented unsupported top-level field
// table (requirement 6.2).
//
// The projector never emits one of these keys. The table therefore guards the
// wire seam: a future projection or transport change that reintroduces a field
// is refused instead of being sent, and the list is the document's own list
// rather than a sample.
var unsupportedTopLevelFields = []string{
	"background",
	"conversation",
	"max_output_tokens",
	"max_tool_calls",
	"metadata",
	"moderation",
	"multi_agent",
	"prompt",
	"prompt_cache_retention",
	"safety_identifier",
	"temperature",
	"top_logprobs",
	"top_p",
	"truncation",
	"user",
}

// AdmitCall applies the preview policy to one canonical call.
//
// The call is read, never mutated: every decision is made on a clone, so the
// canonical request a caller holds never changes under the policy. A refused
// call yields the zero Admission and a typed error; an admitted call yields
// the exact public request plus every explicit omission.
func AdmitCall(in ProjectInput, mode PolicyMode) (Admission, error) {
	posture, err := mode.normalize()
	if err != nil {
		return Admission{}, err
	}
	// The previous-response decision reads the ORIGINAL call: the item
	// authority marker is part of the canonical state, and a clone of an empty
	// item slice is a nil slice (aipapi.CloneCall), which would turn a
	// missing-history continuation into an invalid message-authority call.
	if err := in.Call.Validate(); err != nil {
		return Admission{}, fmt.Errorf("responses: the canonical call is invalid: %w", err)
	}
	omissions := make([]Omission, 0, 4)

	// The previous-response decision distinguishes a call the continuation
	// materializer assembled from the complete parent trajectory from a call
	// that merely carries some input. Length is not provenance: only the
	// materializer records the proxy-owned marker, so a call without it names
	// a conversation this connector cannot see. Serving such a call would
	// present the model with a fabricated, partial history.
	if in.Call.PreviousResponseID != "" {
		if !aipcont.Materialized(in.Call) {
			return Admission{}, policyError(PolicyReasonPreviousResponseMissingHistory, "PreviousResponseID",
				"the call names a continuation parent but carries no proof that its history was materialized")
		}
		omissions = append(omissions, Omission{
			Field:  "PreviousResponseID",
			Wire:   "previous_response_id",
			Reason: PolicyReasonPreviousResponseOmitted,
			Detail: "the call carries its full history, so the proxy-owned parent id is not sent over HTTP",
		})
	}

	// Every decision below mutates the call, so it works on a clone: the
	// canonical request a caller holds never changes under the policy.
	call := aipapi.CloneCall(in.Call)
	call.PreviousResponseID = ""

	for _, rule := range unsupportedGenerationOptions {
		if !rule.set(call.Options) {
			continue
		}
		if posture == PolicyModeStrict {
			return Admission{}, policyError(PolicyReasonGenerationOption, rule.field,
				fmt.Sprintf("the SIWC preview contract does not accept %s on this route", rule.wire))
		}
		omissions = append(omissions, Omission{
			Field:  rule.field,
			Wire:   rule.wire,
			Reason: PolicyReasonGenerationOption,
			Detail: fmt.Sprintf("omitted: %s has no supported form on the SIWC preview route", rule.wire),
		})
	}

	// The prompt-cache hint has two canonical forms (the source-compatible
	// alias and the negotiated semantic carrier) and one effective value. The
	// documented SIWC request surface does not include prompt_cache_key, and
	// its sibling prompt_cache_retention is explicitly unsupported, so
	// forwarding the hint would be an unproven wire shape rather than a
	// documented one. It is an efficiency hint, not request content: omitting
	// it changes cost, never the answer, so the interoperable posture serves
	// the request and records the loss, and the strict posture refuses it.
	cacheKey, err := call.PromptCacheKeyValue()
	if err != nil {
		return Admission{}, fmt.Errorf("responses: the canonical prompt-cache hint is invalid: %w", err)
	}
	if cacheKey != "" {
		if posture == PolicyModeStrict {
			return Admission{}, policyError(PolicyReasonCacheHint, "PromptCacheKey",
				"the public route has no prompt_cache_key field")
		}
		call.PromptCacheKey = ""
		call.SemanticExtensions = withoutPromptCacheKey(call.SemanticExtensions)
		omissions = append(omissions, Omission{
			Field:  "PromptCacheKey",
			Wire:   "prompt_cache_key",
			Reason: PolicyReasonCacheHint,
			Detail: "omitted: the public route does not accept a prompt-cache hint",
		})
	}

	// A protocol extension is provider-facing metadata with no public wire
	// carrier. Two families are not provider semantics: proxy-internal
	// bookkeeping (the "aip." prefix) and the frontend encode/decode
	// round-trip metadata the canonical extension authority classifies as
	// non-protocol. Neither is reported as a degradation; every other key is.
	extensionKeys := protocolExtensionKeys(call.Extensions)
	if len(extensionKeys) > 0 {
		if posture == PolicyModeStrict {
			return Admission{}, policyError(PolicyReasonProtocolExtension, fmt.Sprintf("Extensions[%q]", extensionKeys[0]),
				"the public route has no carrier for a protocol extension")
		}
		for _, key := range extensionKeys {
			omissions = append(omissions, Omission{
				Field:  fmt.Sprintf("Extensions[%q]", key),
				Reason: PolicyReasonProtocolExtension,
				Detail: "omitted: the public route has no carrier for a protocol extension",
			})
		}
	}

	req, err := ProjectCall(ProjectInput{Call: call, Model: in.Model})
	if err != nil {
		return Admission{}, err
	}
	admitted, err := AdmitProjectedRequest(req)
	if err != nil {
		return Admission{}, err
	}
	admitted.Omissions = omissions
	return admitted, nil
}

// AdmitProjectedRequest applies the wire-side preview contract to one
// projected request.
//
// The transport calls this through AdmitCall, and it is exported because it is
// the seam that must be provable independently of the projector: the canonical
// function-tool catalogue cannot express a hosted tool, so the hosted-tool
// refusal is proven on the exact document that would reach the network. The
// request is admitted unchanged or refused with the zero Admission.
func AdmitProjectedRequest(req WireRequest) (Admission, error) {
	document := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(req.Body)) == 0 {
		return Admission{}, policyError(PolicyReasonMalformedRequest, "body",
			"the projected request has no body to admit")
	}
	if !isJSONObject(req.Body) {
		return Admission{}, policyError(PolicyReasonMalformedRequest, "body",
			"the projected request body is not a JSON object")
	}
	if err := json.Unmarshal(req.Body, &document); err != nil {
		return Admission{}, policyError(PolicyReasonMalformedRequest, "body",
			"the projected request body is not valid JSON")
	}
	if document == nil {
		return Admission{}, policyError(PolicyReasonMalformedRequest, "body",
			"the projected request body is not a JSON object")
	}
	if err := requireTransportFlag(document, "store", previewStore); err != nil {
		return Admission{}, err
	}
	if err := requireTransportFlag(document, "stream", previewStream); err != nil {
		return Admission{}, err
	}
	if _, ok := document["previous_response_id"]; ok {
		return Admission{}, policyError(PolicyReasonPreviousResponseOnWire, "previous_response_id",
			"the public HTTP route takes history in input and must not carry previous_response_id")
	}
	if _, ok := document["programmatic_tool_calling"]; ok {
		return Admission{}, policyError(PolicyReasonUnsupportedTool, "programmatic_tool_calling",
			"the top-level programmatic_tool_calling control is not supported on the SIWC preview route")
	}
	for _, field := range unsupportedTopLevelFields {
		if _, ok := document[field]; ok {
			return Admission{}, policyError(PolicyReasonUnsupportedField, field,
				"the SIWC preview contract does not accept this top-level field")
		}
	}
	if err := checkToolDeclarations(document); err != nil {
		return Admission{}, err
	}
	return Admission{Request: req}, nil
}

// requireTransportFlag enforces one mandatory boolean transport flag.
//
// The flag must be present and a JSON boolean: a missing flag and a string
// spelling of it are both refusals, because the public contract names the
// value and not a coercion of it.
func requireTransportFlag(document map[string]json.RawMessage, field string, want bool) error {
	raw, ok := document[field]
	if !ok {
		return policyError(PolicyReasonTransportFlag, field,
			"the projected request is missing a mandatory transport flag")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return policyError(PolicyReasonTransportFlag, field,
			"the mandatory transport flag is not a JSON boolean")
	}
	got, ok := value.(bool)
	if !ok {
		return policyError(PolicyReasonTransportFlag, field,
			"the mandatory transport flag is not a JSON boolean")
	}
	if got != want {
		return policyError(PolicyReasonTransportFlag, field,
			fmt.Sprintf("the mandatory transport flag must be %t on this route", want))
	}
	return nil
}

// checkToolDeclarations refuses every tool declaration the preview route does
// not support.
//
// The route supports function and custom tools; every documented hosted tool
// class (image generation, file search, Code Interpreter, native computer use,
// hosted MCP/connectors and tool_search) is unsupported. The canonical ToolDef
// has no custom representation, so projectTools emits only function
// declarations: admitting custom here is the wire-side guarantee of the pinned
// contract, not a reachable canonical path. The guard walks the whole catalogue
// and every additional_tools item, so it collects the entire declaration list
// instead of sampling the first entry, and it refuses a declaration with no
// type rather than guessing one.
func checkToolDeclarations(document map[string]json.RawMessage) error {
	if raw, ok := document["tools"]; ok {
		if err := checkToolArray(raw, "tools"); err != nil {
			return err
		}
	}
	rawInput, ok := document["input"]
	if !ok {
		return nil
	}
	items, ok := decodeJSONArray(rawInput)
	if !ok {
		return policyError(PolicyReasonMalformedRequest, "input",
			"the projected input is not a JSON array")
	}
	for i, rawItem := range items {
		if !isJSONObject(rawItem) {
			return policyError(PolicyReasonMalformedRequest, fmt.Sprintf("input[%d]", i),
				"a projected input item is not a JSON object")
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return policyError(PolicyReasonMalformedRequest, fmt.Sprintf("input[%d]", i),
				"a projected input item is not a JSON object")
		}
		rawType, ok := item["type"]
		if !ok {
			continue
		}
		var itemType string
		if err := json.Unmarshal(rawType, &itemType); err != nil {
			return policyError(PolicyReasonMalformedRequest, fmt.Sprintf("input[%d].type", i),
				"the projected input item type is not a JSON string")
		}
		if itemType != "additional_tools" {
			continue
		}
		rawTools, ok := item["tools"]
		if !ok {
			return policyError(PolicyReasonMalformedRequest, fmt.Sprintf("input[%d].tools", i),
				"an additional_tools item carries no tool catalogue")
		}
		if err := checkToolArray(rawTools, fmt.Sprintf("input[%d].tools", i)); err != nil {
			return err
		}
	}
	return nil
}

// decodeJSONArray reads a JSON array and refuses null or a non-array value.
//
// A missing catalogue is an empty array, not null: a null catalogue cannot be
// proven to declare no hosted tool, so it is refused instead of being read as
// an absent one.
func decodeJSONArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, false
	}
	return values, true
}

// isJSONObject reports whether raw is a JSON object.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// checkToolArray enforces the supported-tool-declaration rule for one
// catalogue.
func checkToolArray(raw json.RawMessage, field string) error {
	tools, ok := decodeJSONArray(raw)
	if !ok {
		return policyError(PolicyReasonMalformedRequest, field,
			"the projected tool catalogue is not a JSON array")
	}
	for i, rawTool := range tools {
		path := fmt.Sprintf("%s[%d].type", field, i)
		if !isJSONObject(rawTool) {
			return policyError(PolicyReasonUnsupportedTool, path,
				"the tool declaration is not a JSON object")
		}
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return policyError(PolicyReasonUnsupportedTool, path,
				"the tool declaration is not a JSON object")
		}
		rawType, ok := tool["type"]
		if !ok {
			return policyError(PolicyReasonUnsupportedTool, path,
				"the SIWC preview route requires every tool declaration to name its type")
		}
		var toolType string
		if err := json.Unmarshal(rawType, &toolType); err != nil {
			return policyError(PolicyReasonUnsupportedTool, path,
				"the tool type is not a JSON string")
		}
		if toolType != "function" && toolType != "custom" {
			return policyError(PolicyReasonUnsupportedTool, path,
				fmt.Sprintf("the tool type %q is not supported on the SIWC preview route", toolType))
		}
	}
	return nil
}

// withoutPromptCacheKey removes the negotiated prompt-cache carrier while
// leaving every other residual semantic in place.
//
// The projector still refuses residual semantics it has no carrier for; the
// policy removes exactly the carrier it decided to omit, so removing the cache
// hint does not quietly widen what the projector admits.
func withoutPromptCacheKey(extensions []aipapi.SemanticExtension) []aipapi.SemanticExtension {
	if len(extensions) == 0 {
		return nil
	}
	out := make([]aipapi.SemanticExtension, 0, len(extensions))
	for _, ext := range extensions {
		if ext.Namespace == "aip" && ext.Type == "prompt_cache_key" &&
			ext.Implementor == "proxy" && ext.Direction == aipapi.SemanticExtensionDirectionRequest {
			continue
		}
		out = append(out, ext)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isFrontendRoundTripExtensionKey reports whether a key is frontend
// encode/decode round-trip metadata, mirrored from the canonical extension
// authority (pkg/aipapi/extensions_internal.go, isFrontendWireMetadataExtensionKey).
//
// The authority keeps the predicate unexported because it is not a public
// contract, and the policy must not move its own admission semantics into core.
// The connector therefore mirrors the exact key set and prefix rules; the
// extension proofs in policy_test.go pin every named form and compare the
// observable decision against aipapi.DeriveProtocolRequirements. The parity
// test covers existing forms only: an additive authority change (a newly
// exempted key) needs a manual mirror/corpus update here, it is not
// discovered automatically.
func isFrontendRoundTripExtensionKey(key string) bool {
	key = strings.TrimSpace(key)
	switch key {
	case "openairesponses.model", "openailegacy.model", "gemini.model", "anthropic.model",
		"openailegacy.stream_options":
		return true
	}
	return strings.HasPrefix(key, "nvidia.extra_body.") || strings.HasPrefix(key, "openrouter.")
}

// protocolExtensionKeys returns the protocol-facing extension keys in a
// deterministic order.
//
// Proxy-internal bookkeeping carries the reserved "aip." prefix and frontend
// round-trip metadata belongs to the frontend edge, where it is restored
// before the provider is opened; neither is provider semantics, so neither is
// reported as a degradation. Every other key is protocol-facing metadata with
// no public carrier on this route.
func protocolExtensionKeys(extensions map[string]json.RawMessage) []string {
	if len(extensions) == 0 {
		return nil
	}
	keys := make([]string, 0, len(extensions))
	for key := range extensions {
		if strings.HasPrefix(strings.TrimSpace(key), "aip.") || isFrontendRoundTripExtensionKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
