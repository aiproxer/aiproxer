package sessionclassification

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// This file and jev_client.go are the only TypeSafe/Jev wire details in the
// tree: the endpoint, the bearer auth format, the request and response shapes,
// and the answer mapping. The remote port in
// internal/plugins/features/sessionclassification stays provider-neutral, and
// the architecture guard
// TestSessionClassificationVendorDetailsStayInsideTheAdapterPackage enforces
// that boundary (requirements 6.1, 6.7, 7.1, 7.2, 7.5, 11.5; design.md 15, 699,
// 701-714, 1084; research.md 258).
//
// The schema below was reconfirmed against the vendor's current early-access API
// reference at implementation time: https://docs.typesafe.ai/api
//
//	POST https://api.typesafe.ai/v1/systemone
//	Authorization: Bearer <API_KEY>
//	Content-Type: application/json
//
// Request:  {"state": <string|object|array>, "model": "jev-latest",
//	            "questions": {"<id>": {"type": "noul", "instructions": "..."}}}
// Response: {"model": "jev-1.13.0",
//	            "answers": {"<id>": {"type": "noul", "noul": 0.95}},
//	            "usage": {"input_tokens": 296, "output_tokens": 20}}
//
// Two facts from that reference shape the mapping and are restated where they
// are used: a noul question needs only type and instructions (criteria is
// optional), and a noul answer carries only type and noul. Unlike choice and
// score answers, it reports no confidence, because a yes/no question has a
// single probability and the probability already carries the uncertainty.

const (
	// jevEndpointDefault is the documented evaluation endpoint. It is written
	// down here, in the one package the architecture guard exempts, so the port
	// and the SDK stay free of a URL (requirement 11.5).
	jevEndpointDefault = "https://api.typesafe.ai/v1/systemone"
	// jevModelLatest is the vendor's documented model alias. It is pinned rather
	// than configured: the adapter asks one yes/no question and reads one number
	// back, so there is no operational reason to point it at another model, and
	// pinning keeps the request provably within the documented schema.
	jevModelLatest = "jev-latest"
	// jevQuestionID is the adapter-chosen question id. The vendor returns the
	// answer under this same key and never sends the key to the model.
	jevQuestionID = "is_coding_agent_session"
	// jevQuestionTypeNoul is the documented yes/no question type.
	jevQuestionTypeNoul = "noul"
	// jevContentType is the documented request content type.
	jevContentType = "application/json"
	// jevAuthorizationScheme is the documented authorization scheme. The
	// credential itself is resolved from the configured environment reference at
	// call time and never appears here (requirements 7.1, 7.5, 8.3).
	jevAuthorizationScheme = "Bearer"
	// jevInstruction is the adapter-owned question text. It is a fixed bounded
	// literal: it interpolates no turn content, so the model is told what to
	// judge from the derived state members alone and never from user content
	// (requirement 7.2).
	jevInstruction = "Does this derived session metadata describe an autonomous " +
		"coding agent harness making this request? The metadata holds only bounded " +
		"derived facts: the API operation, the normalized client family or an " +
		"ambiguity bit, canonical tool categories, a workspace marker summary, and " +
		"the decisive local evidence code. It contains no prompt, transcript, or " +
		"request content."
	// jevMaxInstructionBytes bounds the adapter-owned question text.
	jevMaxInstructionBytes = 512
	// jevMaxRequestBytes bounds the egress document. The request is already
	// bounded by construction, because every member comes from a closed
	// vocabulary, so this is a fail-closed backstop rather than an expected path.
	jevMaxRequestBytes = 8 << 10
	// jevMaxResponseBytes bounds the vendor response. The read is capped at this
	// size so a hostile or broken endpoint cannot exhaust memory (requirement 6.7).
	jevMaxResponseBytes = 64 << 10
	// jevMaxEndpointBytes bounds the configured endpoint so an origin can never
	// be an unbounded string.
	jevMaxEndpointBytes = 256
	// jevStatusOverloaded is the vendor's documented temporary overload status.
	// It is a 5xx, so it is classified with the rest of the server failures.
	jevStatusOverloaded = 529
)

// jevStateMembers maps every field of the frozen remote contract onto the
// serialized member that carries its derived value. It is exhaustive in both
// directions, and the architecture of RemoteInput itself guarantees the map can
// hold nothing but bounded vocabulary values: the struct has no field for the raw
// User-Agent, a prompt, a transcript, tool names or arguments, a path, a session
// identifier, or a credential (requirements 7.1, 7.2, 7.3, 11.5).
var jevStateMembers = map[string]string{
	"Operation":          "operation",
	"ClientFamily":       "client_family",
	"HasAmbiguousClient": "has_ambiguous_client",
	"ToolCategories":     "tool_categories",
	"WorkspaceClass":     "workspace_class",
	"LocalEvidenceCode":  "local_evidence_code",
}

// jevCategoryOrder is the fixed derived order of the canonical tool categories.
// Only the presence bit travels; a tool name never does (requirement 3.9, 7.2).
var jevCategoryOrder = []struct {
	bit  sdkclassification.ToolCategorySet
	name string
}{
	{bit: sdkclassification.ToolCategoryFileRead, name: "file_read"},
	{bit: sdkclassification.ToolCategoryFileSearch, name: "file_search"},
	{bit: sdkclassification.ToolCategoryOSCommand, name: "os_command"},
	{bit: sdkclassification.ToolCategoryFileEdit, name: "file_edit"},
	{bit: sdkclassification.ToolCategoryFileRemove, name: "file_remove"},
	{bit: sdkclassification.ToolCategoryWebAccess, name: "web_access"},
	{bit: sdkclassification.ToolCategoryUnknownSeen, name: "unknown_seen"},
}

// jevRequest is the documented request body. The vendor accepts a string, an
// object, or an array for state; AIProxer always sends the derived object so the
// payload shape is constant and provable.
type jevRequest struct {
	State     jevState               `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

// jevState is the derived, content-free state document. Every member is a closed
// enumeration value, a fixed bit expansion, or a boolean.
type jevState struct {
	Operation          string   `json:"operation"`
	ClientFamily       string   `json:"client_family"`
	HasAmbiguousClient bool     `json:"has_ambiguous_client"`
	ToolCategories     []string `json:"tool_categories"`
	WorkspaceClass     string   `json:"workspace_class"`
	LocalEvidenceCode  string   `json:"local_evidence_code"`
}

// jevQuestion is the documented question object. A noul question requires only
// the type and the instructions, so nothing else is sent.
type jevQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// jevResponse is the documented response body. Model, answers, and usage are all
// required by the vendor, so each is checked. Unknown members are ignored: the
// schema is early access and may grow, and an added member is not a failure.
type jevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
	Usage   *jevUsage            `json:"usage"`
}

// jevAnswer is the documented answer object. The probability is kept as raw JSON
// so a missing value is distinguishable from a reported zero, so a quoted number
// or a nested object is refused rather than coerced, and so a magnitude outside
// float64 range becomes an explicitly non-finite number the port validator can
// refuse instead of an opaque decode error.
type jevAnswer struct {
	Type string           `json:"type"`
	Noul *json.RawMessage `json:"noul"`
}

// jevUsage is the documented token usage. Classification reads no token count;
// the member is required and its counts must be non-negative, so a response that
// does not satisfy the documented shape is refused instead of half accepted.
// jevUsage models the vendor usage member. Both counts are optional properties
// on the vendor reference, so they are pointers and absence is distinguishable
// from zero.
type jevUsage struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

// newJevState reduces one remote input to the derived state document. It copies
// only the closed-vocabulary values the port already validated; nothing is
// read, reformatted, or invented here.
func newJevState(in featurestate.RemoteInput) jevState {
	return jevState{
		Operation:          string(in.Operation),
		ClientFamily:       string(in.ClientFamily),
		HasAmbiguousClient: in.HasAmbiguousClient,
		ToolCategories:     jevCategoryNames(in.ToolCategories),
		WorkspaceClass:     string(in.WorkspaceClass),
		LocalEvidenceCode:  string(in.LocalEvidenceCode),
	}
}

// jevCategoryNames expands the canonical tool-category bitset into its bounded
// derived names. The result is always non-nil so the member is always a JSON
// array, and only the defined bits are ever named.
func jevCategoryNames(categories sdkclassification.ToolCategorySet) []string {
	names := make([]string, 0, len(jevCategoryOrder))
	for _, category := range jevCategoryOrder {
		if categories&category.bit != 0 {
			names = append(names, category.name)
		}
	}
	return names
}

// jevStateJSONKeys reports the serialized member names of the derived state. It
// is derived from the type rather than restated, so it cannot drift.
func jevStateJSONKeys() []string {
	encoded, err := json.Marshal(jevState{ToolCategories: []string{}})
	if err != nil {
		// A struct of strings, a bool, and a slice cannot fail to encode.
		return nil
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil
	}
	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	return names
}

// jevRequestJSON encodes the bounded request document. Every member is a string,
// a boolean, or a slice of strings, so encoding cannot fail; a nil document is
// still refused by jevMarshalRequest, which keeps a future unmarshalable member
// fail-closed instead of sending a silently coerced body.
func jevRequestJSON(request jevRequest) []byte {
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil
	}
	return encoded
}

// jevMarshalRequest builds the single bounded egress document for one remote
// decision. It refuses anything above the egress bound rather than sending it,
// so the payload size is enforced and not merely assumed from the closed
// vocabularies (requirements 6.7, 7.2).
func jevMarshalRequest(in featurestate.RemoteInput, model string) ([]byte, error) {
	body := jevRequestJSON(jevRequest{
		State: newJevState(in),
		Model: model,
		Questions: map[string]jevQuestion{
			jevQuestionID: {Type: jevQuestionTypeNoul, Instructions: jevInstruction},
		},
	})
	if len(body) == 0 || len(body) > jevMaxRequestBytes {
		return nil, &JevError{
			Kind: JevFailureRequestRefused,
			Err:  errors.New("the derived jev request is empty or above the bounded egress size"),
		}
	}
	return body, nil
}

// decodeJevResponse maps one documented response body onto the bounded port
// decision. Every refusal is a bounded remote error that leaves an unknown
// session unknown and never fails the user request (requirements 6.9, 12.8;
// design.md "Error Handling").
func decodeJevResponse(body []byte) (featurestate.RemoteDecision, error) {
	decoded, err := jevUnmarshalResponse(body)
	if err != nil {
		return featurestate.RemoteDecision{}, err
	}
	probability, err := jevReportedProbability(decoded)
	if err != nil {
		return featurestate.RemoteDecision{}, err
	}
	return jevDecision(probability)
}

// jevUnmarshalResponse decodes exactly one documented response document. A
// trailing document or trailing bytes are refused, and every refusal chains to
// the port's invalid-decision sentinel so a caller classifies it as an unmappable
// remote result (requirements 6.9, 7.5).
func jevUnmarshalResponse(body []byte) (jevResponse, error) {
	var decoded jevResponse
	parser := json.NewDecoder(bytes.NewReader(body))
	if err := parser.Decode(&decoded); err != nil {
		return jevResponse{}, jevUnusableAnswer("the jev response is not one decodable document")
	}
	// A second document must be refused, and the only sound way to prove the
	// first one ended is to ask for whatever follows it and require EOF.
	// json.Decoder.More is NOT that check: it reports whether the NEXT token
	// continues an enclosing array or object, so it reads a trailing ']' or '}'
	// as a delimiter and answers false while bytes remain. That accepted
	// "{...}]", "{...}}" and "{...}]}}" as complete documents.
	var trailing json.RawMessage
	switch err := parser.Decode(&trailing); {
	case errors.Is(err, io.EOF):
		// The single documented document ended exactly at end of body.
	case err == nil:
		return jevResponse{}, jevUnusableAnswer("the jev response carries more than one document")
	default:
		// Non-whitespace bytes that are not a valid document are malformed
		// trailing data, not a second answer.
		return jevResponse{}, jevUnusableAnswer("the jev response carries trailing data")
	}
	return decoded, nil
}

// jevReportedProbability extracts the one reported probability, checking every
// required member of the documented response along the way. No vendor value is
// echoed into the returned error.
func jevReportedProbability(decoded jevResponse) (float64, error) {
	if decoded.Model == "" {
		return 0, jevUnusableAnswer("the jev response reports no model")
	}
	if !jevUsageUsable(decoded.Usage) {
		return 0, jevUnusableAnswer("the jev response reports no usable token usage")
	}
	answer, ok := decoded.Answers[jevQuestionID]
	if !ok {
		return 0, jevUnusableAnswer("the jev response carries no answer for the asked question")
	}
	if answer.Type != jevQuestionTypeNoul {
		return 0, jevUnusableAnswer("the jev answer is not the asked noul answer type")
	}
	if answer.Noul == nil {
		return 0, jevUnusableAnswer("the jev answer reports no probability")
	}
	// A magnitude outside float64 range parses to an infinity with ErrRange. That
	// value is deliberately carried forward so the port validator, not a decode
	// helper, is what refuses it. A quoted number, a boolean, or a nested object
	// is not a number at all and is refused here.
	probability, err := strconv.ParseFloat(string(*answer.Noul), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, jevUnusableAnswer("the jev answer probability is not a number")
	}
	return probability, nil
}

// jevUsageUsable reports whether the required usage member is present with
// usable counts. The vendor reference marks only usage itself as required; the
// two counts are optional properties, so an absent count is tolerated while a
// present count must be non-negative. Requiring both would be stricter than the
// documented schema, and because research.md records that Jev applies no
// output-token charge a response omitting output_tokens would turn every
// successful classification into a refusal and silently disable the feature
// while still failing open. A missing count can never promote anything on its
// own: the counts are never read as a decision input.
func jevUsageUsable(usage *jevUsage) bool {
	if usage == nil {
		return false
	}
	if usage.InputTokens != nil && *usage.InputTokens < 0 {
		return false
	}
	return usage.OutputTokens == nil || *usage.OutputTokens >= 0
}

// jevDecision maps the reported probability onto the port decision and lets the
// port's own validator accept or refuse it.
//
// The reported noul probability is the coding probability. Confidence stays at
// its zero value, because the vendor reports no confidence for a noul answer and
// requirement 6.8 makes the configured positive_threshold the only threshold
// input; RemoteDecision.Positive validates the pair and then compares the
// probability alone, so an unreported confidence cannot block a legitimate
// promotion. No confidence-style value is substituted for a number the service
// did not send, and the answer type is not changed merely to obtain one
// (requirements 6.8, 6.9).
func jevDecision(probability float64) (featurestate.RemoteDecision, error) {
	decision := featurestate.RemoteDecision{CodingProbability: probability}
	if err := featurestate.ValidateRemoteDecision(decision); err != nil {
		return featurestate.RemoteDecision{}, &JevError{
			Kind: JevFailureMalformedResponse,
			Err:  fmt.Errorf("the reported probability cannot be thresholded safely: %w", err),
		}
	}
	return decision, nil
}

// jevUnusableAnswer reports a remote result that does not satisfy the documented
// response shape. It carries the port sentinel so the caller sees an invalid remote
// decision, and the bounded reason so no vendor body and no vendor value is echoed
// (requirements 6.9, 7.5).
func jevUnusableAnswer(reason string) error {
	return &JevError{
		Kind: JevFailureMalformedResponse,
		Err:  fmt.Errorf("%w: %s", featurestate.ErrInvalidRemoteDecision, reason),
	}
}

// jevValidateEndpoint accepts only an absolute, credential-free, query-free
// https origin, plus plaintext http on loopback so a hermetic test endpoint can
// be used. Anything else is refused before an adapter exists, so the bearer
// header can never be aimed at an origin the operator did not configure
// (requirements 6.7, 7.1, 8.4; design.md 711).
func jevValidateEndpoint(endpoint string) error {
	if endpoint == "" {
		return errors.New("jev endpoint is empty")
	}
	if len(endpoint) > jevMaxEndpointBytes {
		return errors.New("jev endpoint exceeds the bounded origin size")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("jev endpoint is not a parsable URL")
	}
	if !parsed.IsAbs() {
		return errors.New("jev endpoint is not an absolute URL")
	}
	if parsed.User != nil {
		return errors.New("jev endpoint carries embedded credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("jev endpoint carries a query or fragment")
	}
	if !jevOriginAllowed(parsed.Scheme, parsed.Hostname()) {
		return errors.New("jev endpoint is not a permitted origin")
	}
	return nil
}

// jevOriginAllowed reports whether a scheme and host may carry the credential.
// TLS is always allowed; plaintext is allowed only for the loopback literals, so
// a production endpoint cannot silently downgrade to http.
func jevOriginAllowed(scheme, host string) bool {
	if host == "" {
		return false
	}
	switch scheme {
	case "https":
		return true
	case "http":
		return host == "127.0.0.1" || host == "::1" || host == "localhost"
	default:
		return false
	}
}
