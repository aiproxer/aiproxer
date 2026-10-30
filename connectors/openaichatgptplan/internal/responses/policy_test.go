package responses_test

// Behavior proofs for the centralized SIWC preview request policy (task 4.3).
//
// The task 4.1 fixture matrix pins how a supported request is rendered, and
// the task 4.2 proofs pin the projection rules. These proofs exist because the
// preview policy must fail for the rule it NAMES:
//
//   - the forcing proof says every admitted request carries store:false and
//     stream:true, so a posture or delivery mode that changes either flag
//     fails here;
//   - the omission table says every documented unsupported generation option,
//     cache hint and protocol extension is either refused or omitted with a
//     recorded Omission, so a silently dropped value fails here with the
//     canonical field it lost;
//   - the wire-field table says every documented unsupported top-level field
//     is refused, so removing one row from the guard fails here for that
//     field;
//   - the hosted-tool table says every documented hosted tool class is
//     refused before execution, so a guard that samples the first tool or
//     admits a hosted type fails here;
//   - the previous-response proofs say a materialized call is served with its
//     full history and the proxy-owned id omitted, while a call whose history
//     is missing is refused, so a guard that assumes "id means continuation"
//     fails here;
//   - the positive proofs say a minimal, tool and multimodal request is
//     admitted unchanged with zero omissions, so a policy that degrades a
//     supported request fails here.
//
// The wire-side proofs feed a projected request directly. Hosted tools cannot
// be expressed by the canonical function-tool catalogue, so their refusal is
// proven on the exact document that would reach the network; the canonical
// path can only ever produce function tools, and the same guard runs on it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// policyMinimalCall is the smallest valid canonical call the policy admits.
func policyMinimalCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
	}
}

// policyAdmit admits one call and fails when the policy refuses it.
func policyAdmit(t *testing.T, in responses.ProjectInput, mode responses.PolicyMode) responses.Admission {
	t.Helper()
	admission, err := responses.AdmitCall(in, mode)
	if err != nil {
		t.Fatalf("the preview policy must admit this call, got error: %v", err)
	}
	return admission
}

// policyRequireZeroAdmission asserts that a refusal produced no request.
//
// "Zero WireRequest" is the policy's promise to the transport: a refused
// request has nothing to send, so a caller cannot accidentally execute it.
func policyRequireZeroAdmission(t *testing.T, admission responses.Admission, context string) {
	t.Helper()
	if admission.Request.Method != "" || admission.Request.URL != "" ||
		len(admission.Request.Headers) != 0 || len(admission.Request.Body) != 0 {
		t.Fatalf("%s: the refusal must produce zero WireRequest, got %s", context, describeRequest(admission.Request))
	}
	if len(admission.Omissions) != 0 {
		t.Fatalf("%s: the refusal must not carry omissions, got %v", context, admission.Omissions)
	}
}

// policyRequireRefusal asserts the typed policy refusal and its identity.
func policyRequireRefusal(t *testing.T, err error, wantReason responses.PolicyReason, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the preview policy must refuse with reason %q at %q, but the request was admitted", wantReason, wantField)
	}
	if !errors.Is(err, responses.ErrRequestNotAdmissible) {
		t.Fatalf("the refusal must classify as ErrRequestNotAdmissible, got %v", err)
	}
	var refusal *responses.PolicyError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal must carry a PolicyError, got %T: %v", err, err)
	}
	if refusal.Reason != wantReason || refusal.Field != wantField {
		t.Fatalf("the refusal must be %q at %q, got %q at %q: %v", wantReason, wantField, refusal.Reason, refusal.Field, err)
	}
	if refusal.Detail == "" {
		t.Fatalf("the refusal must explain itself, got an empty detail: %v", err)
	}
}

// policyRequireOmissions asserts the exact omission sequence.
func policyRequireOmissions(t *testing.T, admission responses.Admission, want []responses.Omission) {
	t.Helper()
	if len(admission.Omissions) != len(want) {
		t.Fatalf("the policy must record %d omission(s), got %d: %v", len(want), len(admission.Omissions), admission.Omissions)
	}
	for i, expected := range want {
		got := admission.Omissions[i]
		if got.Field != expected.Field || got.Wire != expected.Wire || got.Reason != expected.Reason {
			t.Fatalf("omission %d must be %q/%q/%q, got %q/%q/%q",
				i, expected.Field, expected.Wire, expected.Reason, got.Field, got.Wire, got.Reason)
		}
		if got.Detail == "" {
			t.Fatalf("omission %d (%q) must explain itself, got an empty detail", i, got.Field)
		}
	}
}

// policyRequireBodyFieldAbsent asserts a top-level wire field is absent.
func policyRequireBodyFieldAbsent(t *testing.T, admission responses.Admission, fields ...string) {
	t.Helper()
	body := decodedBody(t, admission.Request)
	for _, field := range fields {
		if _, ok := body[field]; ok {
			t.Fatalf("the admitted request must not carry the unsupported top-level field %q: %v", field, body)
		}
	}
}

// policyBaseBody is a valid projected body the wire-side proofs mutate.
func policyBaseBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	req := projectedRequest(t, responses.ProjectInput{Call: policyMinimalCall(), Model: fixtureModel})
	var document map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &document); err != nil {
		t.Fatalf("the projected base body is not a JSON object: %v", err)
	}
	return document
}

// policyMarshalBody renders a mutated body.
func policyMarshalBody(t *testing.T, document map[string]json.RawMessage) []byte {
	t.Helper()
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("the mutated body cannot be encoded: %v", err)
	}
	return body
}

// policyRequestFromBody wraps a crafted body in a transport-shaped request.
func policyRequestFromBody(t *testing.T, body []byte) responses.WireRequest {
	t.Helper()
	return responses.WireRequest{
		Method:  http.MethodPost,
		URL:     responses.PublicBaseURL + responses.ResponsesPath,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body,
	}
}

// policyDocumentWithTools builds a valid body whose input carries one
// additional_tools item with the supplied tool declarations.
func policyDocumentWithTools(t *testing.T, tools []any) []byte {
	t.Helper()
	document := map[string]any{
		"model": fixtureModel,
		"input": []any{
			map[string]any{
				"type":  "additional_tools",
				"role":  "developer",
				"tools": tools,
			},
		},
		"store":  false,
		"stream": true,
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("the crafted tool document cannot be encoded: %v", err)
	}
	return body
}

// policyDocumentWithTopLevelTools builds a valid body whose top-level tools
// array carries the supplied declarations.
func policyDocumentWithTopLevelTools(t *testing.T, tools []any) []byte {
	t.Helper()
	document := policyBaseBody(t)
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("the crafted top-level tool catalogue cannot be encoded: %v", err)
	}
	document["tools"] = raw
	return policyMarshalBody(t, document)
}

// policyRequireProjectionRefusal asserts the inherited projector refusal and
// its identity.
//
// These refusals are not PolicyErrors: the 4.2 projector owns them, and the
// policy propagates them unchanged instead of reclassifying them as ordinary
// omissions.
func policyRequireProjectionRefusal(t *testing.T, err error, wantReason responses.ProjectionReason, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the projector must refuse with reason %q at %q, but the request was admitted", wantReason, wantField)
	}
	if !errors.Is(err, responses.ErrCallNotRepresentable) {
		t.Fatalf("the refusal must stay the projector's, got %v", err)
	}
	var refusal *responses.ProjectionError
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal must carry a ProjectionError, got %T: %v", err, err)
	}
	if refusal.Reason != wantReason || refusal.Field != wantField {
		t.Fatalf("the refusal must be %q at %q, got %q at %q: %v", wantReason, wantField, refusal.Reason, refusal.Field, err)
	}
}

// TestPreviewPolicyForcesStoreFalseAndStreamTrue is the forcing proof.
//
// Every admitted request carries the two mandatory transport flags, whatever
// the downstream delivery mode and whatever supported or omitted controls the
// call carries. The flags are booleans, so a projection that emits the strings
// "false"/"true" fails here too.
func TestPreviewPolicyForcesStoreFalseAndStreamTrue(t *testing.T) {
	t.Parallel()
	temperature := 0.2
	call := policyMinimalCall()
	call.Options = aipapi.GenerationOptions{Temperature: &temperature}
	call.PromptCacheKey = "cache-1"
	call.Extensions = map[string]json.RawMessage{"beta.feature": json.RawMessage(`true`)}

	for _, mode := range deliveryModes {
		for _, posture := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
			admitted := call
			if posture == responses.PolicyModeStrict {
				// Strict refuses the unsupported controls, so the forcing
				// proof uses the supported core of the same call.
				admitted = policyMinimalCall()
			}
			admitted.Invocation = aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: mode}
			admission := policyAdmit(t, responses.ProjectInput{Call: admitted, Model: fixtureModel}, posture)
			body := decodedBody(t, admission.Request)
			store, ok := body["store"].(bool)
			if !ok || store {
				t.Fatalf("mode %s/%s: every admitted request must carry the boolean store:false, got %v", mode, posture, body["store"])
			}
			stream, ok := body["stream"].(bool)
			if !ok || !stream {
				t.Fatalf("mode %s/%s: every admitted request must carry the boolean stream:true, got %v", mode, posture, body["stream"])
			}
			for _, forbidden := range []string{"background", "conversation", "previous_response_id"} {
				if _, ok := body[forbidden]; ok {
					t.Fatalf("mode %s/%s: the admitted request must not carry %q", mode, posture, forbidden)
				}
			}
		}
	}
}

// TestPreviewPolicyRefusesTransportFlagViolations is the wire-side half of the
// forcing proof: a projected document that does not carry the exact flags is
// refused instead of being sent.
func TestPreviewPolicyRefusesTransportFlagViolations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		mutate    func(map[string]json.RawMessage)
		wantField string
	}{
		{name: "store is true", mutate: func(d map[string]json.RawMessage) { d["store"] = json.RawMessage(`true`) }, wantField: "store"},
		{name: "store is missing", mutate: func(d map[string]json.RawMessage) { delete(d, "store") }, wantField: "store"},
		{name: "store is not a boolean", mutate: func(d map[string]json.RawMessage) { d["store"] = json.RawMessage(`"false"`) }, wantField: "store"},
		{name: "stream is false", mutate: func(d map[string]json.RawMessage) { d["stream"] = json.RawMessage(`false`) }, wantField: "stream"},
		{name: "stream is missing", mutate: func(d map[string]json.RawMessage) { delete(d, "stream") }, wantField: "stream"},
		{name: "stream is not a boolean", mutate: func(d map[string]json.RawMessage) { d["stream"] = json.RawMessage(`"true"`) }, wantField: "stream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			document := policyBaseBody(t)
			tc.mutate(document)
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, policyMarshalBody(t, document)))
			policyRequireRefusal(t, err, responses.PolicyReasonTransportFlag, tc.wantField)
			policyRequireZeroAdmission(t, admission, tc.name)
		})
	}
}

// TestPreviewPolicyOmitsEveryUnsupportedGenerationOption is the
// explicit-omit proof.
//
// Each documented generation option is carried by the canonical call, absent
// from the admitted wire document, and recorded as one omission that names the
// canonical field, the wire field and the reason. Strict posture refuses the
// same call instead of omitting.
func TestPreviewPolicyOmitsEveryUnsupportedGenerationOption(t *testing.T) {
	t.Parallel()
	temperature := 0.7
	topP := 0.9
	maxOutput := 128
	parallel := true
	cases := []struct {
		name      string
		options   aipapi.GenerationOptions
		wantField string
		wantWire  string
	}{
		{name: "temperature", options: aipapi.GenerationOptions{Temperature: &temperature}, wantField: "Options.Temperature", wantWire: "temperature"},
		{name: "top_p", options: aipapi.GenerationOptions{TopP: &topP}, wantField: "Options.TopP", wantWire: "top_p"},
		{name: "max_output_tokens", options: aipapi.GenerationOptions{MaxOutputTokens: &maxOutput}, wantField: "Options.MaxOutputTokens", wantWire: "max_output_tokens"},
		{name: "verbosity", options: aipapi.GenerationOptions{Verbosity: aipapi.VerbosityHigh}, wantField: "Options.Verbosity", wantWire: "text.verbosity"},
		{name: "parallel_tool_calls", options: aipapi.GenerationOptions{ParallelToolCalls: &parallel}, wantField: "Options.ParallelToolCalls", wantWire: "parallel_tool_calls"},
		{name: "response mime type", options: aipapi.GenerationOptions{ResponseMIMEType: "application/json"}, wantField: "Options.ResponseMIMEType", wantWire: "text.format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := policyMinimalCall()
			call.Options = tc.options
			admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			policyRequireOmissions(t, admission, []responses.Omission{{
				Field: tc.wantField, Wire: tc.wantWire, Reason: responses.PolicyReasonGenerationOption,
			}})
			policyRequireBodyFieldAbsent(t, admission, "temperature", "top_p", "max_output_tokens", "text", "parallel_tool_calls")

			strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
			policyRequireRefusal(t, err, responses.PolicyReasonGenerationOption, tc.wantField)
			policyRequireZeroAdmission(t, strictAdmission, "strict "+tc.name)
		})
	}
}

// TestPreviewPolicyCollectsEveryUnsupportedGenerationOption is the
// collect-not-sample proof: a call carrying six unsupported options produces
// six omissions, not the first one.
func TestPreviewPolicyCollectsEveryUnsupportedGenerationOption(t *testing.T) {
	t.Parallel()
	temperature := 0.7
	topP := 0.9
	maxOutput := 128
	parallel := true
	call := policyMinimalCall()
	call.Options = aipapi.GenerationOptions{
		Temperature:       &temperature,
		TopP:              &topP,
		MaxOutputTokens:   &maxOutput,
		Verbosity:         aipapi.VerbosityLow,
		ParallelToolCalls: &parallel,
		ResponseMIMEType:  "text/plain",
	}
	admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	want := []responses.Omission{
		{Field: "Options.Temperature", Wire: "temperature", Reason: responses.PolicyReasonGenerationOption},
		{Field: "Options.TopP", Wire: "top_p", Reason: responses.PolicyReasonGenerationOption},
		{Field: "Options.MaxOutputTokens", Wire: "max_output_tokens", Reason: responses.PolicyReasonGenerationOption},
		{Field: "Options.Verbosity", Wire: "text.verbosity", Reason: responses.PolicyReasonGenerationOption},
		{Field: "Options.ParallelToolCalls", Wire: "parallel_tool_calls", Reason: responses.PolicyReasonGenerationOption},
		{Field: "Options.ResponseMIMEType", Wire: "text.format", Reason: responses.PolicyReasonGenerationOption},
	}
	policyRequireOmissions(t, admission, want)
	policyRequireBodyFieldAbsent(t, admission, "temperature", "top_p", "max_output_tokens", "text", "parallel_tool_calls")
}

// TestPreviewPolicyDecidesCacheHintsObservably is the cache-hint proof.
//
// The legacy field and the negotiated semantic carrier are two forms of the
// same hint, so each is recorded once, the wire document carries no
// prompt_cache_key, and the hint value never appears in the diagnostic.
func TestPreviewPolicyDecidesCacheHintsObservably(t *testing.T) {
	t.Parallel()
	cacheExtension := aipapi.SemanticExtension{
		Namespace:   "aip",
		Type:        "prompt_cache_key",
		Implementor: "proxy",
		Direction:   aipapi.SemanticExtensionDirectionRequest,
		Presence:    aipapi.SemanticExtensionValue,
		Data:        json.RawMessage(`"cache-1"`),
	}
	cases := []struct {
		name string
		call aipapi.Call
	}{
		{name: "legacy field", call: func() aipapi.Call {
			call := policyMinimalCall()
			call.PromptCacheKey = "cache-1"
			return call
		}()},
		{name: "semantic carrier", call: func() aipapi.Call {
			call := policyMinimalCall()
			call.SemanticExtensions = []aipapi.SemanticExtension{cacheExtension}
			return call
		}()},
		{name: "both agreeing", call: func() aipapi.Call {
			call := policyMinimalCall()
			call.PromptCacheKey = "cache-1"
			call.SemanticExtensions = []aipapi.SemanticExtension{cacheExtension}
			return call
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call.Validate(); err != nil {
				t.Fatalf("the cache-hint fixture must be a valid canonical call, otherwise it can fail for the wrong reason: %v", err)
			}
			admission := policyAdmit(t, responses.ProjectInput{Call: tc.call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			policyRequireOmissions(t, admission, []responses.Omission{{
				Field: "PromptCacheKey", Wire: "prompt_cache_key", Reason: responses.PolicyReasonCacheHint,
			}})
			policyRequireBodyFieldAbsent(t, admission, "prompt_cache_key")
			if strings.Contains(admission.Omissions[0].Detail, "cache-1") {
				t.Fatalf("the omission diagnostic must not carry the hint value: %v", admission.Omissions[0].Detail)
			}

			strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: tc.call, Model: fixtureModel}, responses.PolicyModeStrict)
			policyRequireRefusal(t, err, responses.PolicyReasonCacheHint, "PromptCacheKey")
			policyRequireZeroAdmission(t, strictAdmission, "strict "+tc.name)
		})
	}
}

// TestPreviewPolicyKeepsTheProjectorRefusalForSemanticCarriers pins the 4.2
// boundary: the projector still refuses residual semantics, and the policy
// removes only the cache-hint carrier it decided to omit.
func TestPreviewPolicyKeepsTheProjectorRefusalForSemanticCarriers(t *testing.T) {
	t.Parallel()
	call := policyMinimalCall()
	call.SemanticExtensions = []aipapi.SemanticExtension{{
		Namespace:   "aip",
		Type:        "prompt_cache_key",
		Implementor: "proxy",
		Direction:   aipapi.SemanticExtensionDirectionRequest,
		Presence:    aipapi.SemanticExtensionValue,
		Data:        json.RawMessage(`"cache-1"`),
	}}
	if _, err := responses.ProjectCall(responses.ProjectInput{Call: call, Model: fixtureModel}); !errors.Is(err, responses.ErrCallNotRepresentable) {
		t.Fatalf("the 4.2 projector must keep refusing residual semantics, got %v", err)
	}
	other := policyMinimalCall()
	other.SemanticExtensions = []aipapi.SemanticExtension{{
		Namespace:   "acme",
		Type:        "beta",
		Implementor: "acme",
		Direction:   aipapi.SemanticExtensionDirectionRequest,
		Presence:    aipapi.SemanticExtensionValue,
		Data:        json.RawMessage(`{"x":1}`),
	}}
	admission, err := responses.AdmitCall(responses.ProjectInput{Call: other, Model: fixtureModel}, responses.PolicyModeInteroperable)
	if err == nil {
		t.Fatalf("a residual semantic with no public carrier must stay refused, got %s", describeRequest(admission.Request))
	}
	if !errors.Is(err, responses.ErrCallNotRepresentable) {
		t.Fatalf("the residual-semantic refusal must stay the projector's, got %v", err)
	}
}

// TestPreviewPolicyKeepsUnrelatedResidualSemanticsWhenOmittingACacheHint is the
// conflict-seeded cache-hint proof.
//
// Omitting the recognized cache-hint carrier must not clear an unrelated
// residual semantic and silently make an unrepresentable call admissible. The
// call carries an agreeing legacy/semantic hint AND an unrelated semantic
// carrier: interoperable posture must still refuse through the projector and
// return the zero Admission, so a helper that clears every residual semantic
// fails here.
func TestPreviewPolicyKeepsUnrelatedResidualSemanticsWhenOmittingACacheHint(t *testing.T) {
	t.Parallel()
	call := policyMinimalCall()
	call.PromptCacheKey = "cache-1"
	call.SemanticExtensions = []aipapi.SemanticExtension{
		{
			Namespace:   "aip",
			Type:        "prompt_cache_key",
			Implementor: "proxy",
			Direction:   aipapi.SemanticExtensionDirectionRequest,
			Presence:    aipapi.SemanticExtensionValue,
			Data:        json.RawMessage(`"cache-1"`),
		},
		{
			Namespace:   "acme",
			Type:        "beta",
			Implementor: "acme",
			Direction:   aipapi.SemanticExtensionDirectionRequest,
			Presence:    aipapi.SemanticExtensionValue,
			Data:        json.RawMessage(`{"x":1}`),
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the mixed cache-hint fixture must be a valid canonical call: %v", err)
	}
	admission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireProjectionRefusal(t, err, responses.ProjectionReasonSemanticExtension, "SemanticExtensions")
	policyRequireZeroAdmission(t, admission, "mixed cache hint and unrelated semantic")

	strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
	policyRequireRefusal(t, err, responses.PolicyReasonCacheHint, "PromptCacheKey")
	policyRequireZeroAdmission(t, strictAdmission, "strict mixed cache hint and unrelated semantic")
}

// TestPreviewPolicyPropagatesInheritedProjectorRefusals pins the 4.2 ownership
// boundary through AdmitCall.
//
// The allowed-tools subset and the system non-text refusal are projector
// decisions, not preview-policy omissions: the policy propagates each
// ProjectionError unchanged and returns the zero Admission. In particular it
// never omits an allowed-tools restriction, which would widen tool access.
func TestPreviewPolicyPropagatesInheritedProjectorRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		call   aipapi.Call
		reason responses.ProjectionReason
		field  string
	}{
		{
			name: "allowed-tools subset",
			call: aipapi.Call{
				Tools:      []aipapi.ToolDef{{Name: "lookup"}},
				ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"lookup"}},
				Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
			},
			reason: responses.ProjectionReasonToolChoiceSubset,
			field:  "ToolChoice.AllowedTools",
		},
		{
			name: "system non-text content",
			call: aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo="}}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			}},
			reason: responses.ProjectionReasonSystemContent,
			field:  "Messages[0].Parts[0]",
		},
	}
	for _, tc := range cases {
		for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
			t.Run(tc.name+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				admission, err := responses.AdmitCall(responses.ProjectInput{Call: tc.call, Model: fixtureModel}, mode)
				policyRequireProjectionRefusal(t, err, tc.reason, tc.field)
				policyRequireZeroAdmission(t, admission, tc.name)
			})
		}
	}
}

// TestPreviewPolicyDecidesProtocolExtensions is the extension proof.
//
// A protocol extension has no public wire carrier. It is omitted with one
// recorded omission in interoperable posture and refused in strict posture.
// Proxy-internal bookkeeping (the "aip." prefix) and the frontend round-trip
// metadata the canonical authority classifies as non-protocol are not
// provider semantics and are not reported as degradations.
func TestPreviewPolicyDecidesProtocolExtensions(t *testing.T) {
	t.Parallel()
	call := policyMinimalCall()
	call.Extensions = map[string]json.RawMessage{
		"beta.feature":          json.RawMessage(`{"enabled":true}`),
		"acme.experimental":     json.RawMessage(`"on"`),
		"aip.continuity_marker": json.RawMessage(`"marker"`),
		// Frontend round-trip metadata must not add an omission, and a genuine
		// protocol key in the same call must still be decided on its own.
		"openairesponses.model":      json.RawMessage(`"gpt-x"`),
		"openrouter.upstream_flavor": json.RawMessage(`"openai"`),
	}
	admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireOmissions(t, admission, []responses.Omission{
		{Field: `Extensions["acme.experimental"]`, Reason: responses.PolicyReasonProtocolExtension},
		{Field: `Extensions["beta.feature"]`, Reason: responses.PolicyReasonProtocolExtension},
	})
	for _, key := range []string{"beta.feature", "acme.experimental", "aip.continuity_marker", "openairesponses.model", "openrouter.upstream_flavor"} {
		if bytes.Contains(admission.Request.Body, []byte(key)) {
			t.Fatalf("the admitted request must not carry the extension key %q: %s", key, admission.Request.Body)
		}
	}

	internalOnly := policyMinimalCall()
	internalOnly.Extensions = map[string]json.RawMessage{"aip.continuity_marker": json.RawMessage(`"marker"`)}
	internalAdmission := policyAdmit(t, responses.ProjectInput{Call: internalOnly, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireOmissions(t, internalAdmission, nil)

	strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
	policyRequireRefusal(t, err, responses.PolicyReasonProtocolExtension, `Extensions["acme.experimental"]`)
	policyRequireZeroAdmission(t, strictAdmission, "strict protocol extension")

	// The negative case on its own: one genuine protocol key is still omitted
	// in interoperable posture and refused in strict posture.
	genuine := policyMinimalCall()
	genuine.Extensions = map[string]json.RawMessage{
		"beta.feature":          json.RawMessage(`true`),
		"openairesponses.model": json.RawMessage(`"gpt-x"`),
	}
	genuineAdmission := policyAdmit(t, responses.ProjectInput{Call: genuine, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireOmissions(t, genuineAdmission, []responses.Omission{{
		Field: `Extensions["beta.feature"]`, Reason: responses.PolicyReasonProtocolExtension,
	}})
	genuineStrict, err := responses.AdmitCall(responses.ProjectInput{Call: genuine, Model: fixtureModel}, responses.PolicyModeStrict)
	policyRequireRefusal(t, err, responses.PolicyReasonProtocolExtension, `Extensions["beta.feature"]`)
	policyRequireZeroAdmission(t, genuineStrict, "strict genuine protocol extension")
}

func TestPreviewPolicyDoesNotAdmitCodexCompatExtensionKeys(t *testing.T) {
	t.Parallel()

	// Requirement 11.3: the legacy feature's canonical-call keys must not become
	// part of this connector's API. They are ordinary protocol extensions with no
	// public carrier, so the policy omits them observably or refuses them; it
	// never maps them onto SIWC fields.
	for _, key := range []string{
		"openai_codex.ignore_unsupported_gen_params",
		"openai_codex.tool_strict",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			call := policyMinimalCall()
			call.Extensions = map[string]json.RawMessage{key: json.RawMessage(`true`)}
			admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			policyRequireOmissions(t, admission, []responses.Omission{{
				Field:  `Extensions["` + key + `"]`,
				Reason: responses.PolicyReasonProtocolExtension,
			}})
			if bytes.Contains(admission.Request.Body, []byte(key)) {
				t.Fatalf("the admitted request must not carry the legacy key %q: %s", key, admission.Request.Body)
			}

			strictAdmission, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
			policyRequireRefusal(t, err, responses.PolicyReasonProtocolExtension, `Extensions["`+key+`"]`)
			policyRequireZeroAdmission(t, strictAdmission, "strict legacy codex compatibility extension")
		})
	}
}

// frontendRoundTripExtensionKeys is the frontend encode/decode metadata key set
// the canonical authority names (pkg/aipapi/extensions_internal.go). Every form
// - the exact keys and both documented prefixes - is exercised.
var frontendRoundTripExtensionKeys = []string{
	"openairesponses.model",
	"openailegacy.model",
	"gemini.model",
	"anthropic.model",
	"openailegacy.stream_options",
	"nvidia.extra_body.reasoning",
	"openrouter.upstream_flavor",
}

// TestPreviewPolicyAdmitsFrontendRoundTripMetadata is the J1 regression.
//
// A lawful frontend call carries its model and upstream flavor as round-trip
// metadata. The canonical authority classifies these keys as frontend
// encode/decode metadata that must not drive protocol requirement admission,
// and the managed Execute path restores them before the provider is opened, so
// they are normal reachable inputs: the policy must admit them with zero
// omissions in BOTH postures instead of reporting false degradation or
// refusing a lawful request in strict posture.
func TestPreviewPolicyAdmitsFrontendRoundTripMetadata(t *testing.T) {
	t.Parallel()
	for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			call := policyMinimalCall()
			call.Extensions = map[string]json.RawMessage{
				"openairesponses.model":      json.RawMessage(`"gpt-x"`),
				"openrouter.upstream_flavor": json.RawMessage(`"openai"`),
			}
			if err := call.Validate(); err != nil {
				t.Fatalf("the metadata fixture must be a valid canonical call, otherwise it can fail for the wrong reason: %v", err)
			}
			admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, mode)
			policyRequireOmissions(t, admission, nil)
			for key := range call.Extensions {
				if bytes.Contains(admission.Request.Body, []byte(key)) {
					t.Fatalf("round-trip metadata must not become wire content: %q in %s", key, admission.Request.Body)
				}
			}
			if len(call.Extensions) != 2 {
				t.Fatalf("the policy reads a clone and must not mutate the caller's metadata, got %v", call.Extensions)
			}
		})
	}
	for _, key := range frontendRoundTripExtensionKeys {
		for _, mode := range []responses.PolicyMode{responses.PolicyModeInteroperable, responses.PolicyModeStrict} {
			t.Run(string(mode)+"/"+key, func(t *testing.T) {
				t.Parallel()
				call := policyMinimalCall()
				call.Extensions = map[string]json.RawMessage{key: json.RawMessage(`"metadata"`)}
				admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, mode)
				policyRequireOmissions(t, admission, nil)
			})
		}
	}
}

// TestPreviewPolicyClassifiesExtensionsLikeTheCanonicalAuthority pins the
// local mirror to the canonical extension authority.
//
// The canonical DeriveProtocolRequirements names a call-scoped extension
// requirement exactly when a key must drive protocol admission. The policy's
// observable decision must agree: a key the authority ignores produces no
// omission and is admitted in strict posture, while a protocol key produces an
// omission and is refused. A drift between the two classifiers fails here.
func TestPreviewPolicyClassifiesExtensionsLikeTheCanonicalAuthority(t *testing.T) {
	t.Parallel()
	keys := append([]string{"beta.feature", "acme.experimental"}, frontendRoundTripExtensionKeys...)
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			call := policyMinimalCall()
			call.Extensions = map[string]json.RawMessage{key: json.RawMessage(`"metadata"`)}
			requirements := aipapi.DeriveProtocolRequirements(call)
			protocolFacing := false
			for _, requirement := range requirements.ExtensionTypes {
				if requirement.Namespace == "call" && requirement.Type == key {
					protocolFacing = true
				}
			}
			admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			if got := len(admission.Omissions) > 0; got != protocolFacing {
				t.Fatalf("the policy must classify %q like the canonical authority (protocol-facing=%t), got omissions %v",
					key, protocolFacing, admission.Omissions)
			}
			_, err := responses.AdmitCall(responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
			if strictRefused := err != nil; strictRefused != protocolFacing {
				t.Fatalf("strict posture must refuse %q exactly when the canonical authority treats it as protocol-facing (protocol-facing=%t): %v",
					key, protocolFacing, err)
			}
		})
	}
}

// TestPreviewPolicyAdmitsProjectedCustomToolDeclarations pins the supported
// projected tool types.
//
// The pinned preview contract supports function and custom tools; only the
// documented hosted classes are unsupported. The canonical ToolDef has no
// custom representation, so projectTools emits only function tools and this
// wire-side guarantee is not reachable from a canonical call yet. Admitting a
// custom declaration must not open the door to a hosted class that follows it.
func TestPreviewPolicyAdmitsProjectedCustomToolDeclarations(t *testing.T) {
	t.Parallel()
	function := map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}}
	custom := map[string]any{
		"type":        "custom",
		"name":        "shell",
		"description": "Run one shell fragment.",
		"format":      map[string]any{"type": "text"},
	}
	cases := []struct {
		name string
		body []byte
	}{
		{name: "additional_tools", body: policyDocumentWithTools(t, []any{function, custom})},
		{name: "top-level tools", body: policyDocumentWithTopLevelTools(t, []any{function, custom})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, tc.body))
			if err != nil {
				t.Fatalf("a projected custom tool declaration is supported by the pinned preview contract and must be admitted, got %v", err)
			}
			if !bytes.Equal(admission.Request.Body, tc.body) {
				t.Fatalf("the admitted request must be the projected request unchanged")
			}
		})
	}
	t.Run("hosted tool after a custom declaration", func(t *testing.T) {
		t.Parallel()
		body := policyDocumentWithTools(t, []any{custom, map[string]any{"type": "file_search"}})
		admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
		policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "input[0].tools[1].type")
		policyRequireZeroAdmission(t, admission, "hosted tool after a custom declaration")
	})
}

// TestPreviewPolicyDistinguishesMaterializedHistoryFromMissingHistory is the
// previous-response proof.
//
// A materialized call carries its full history and the materializer's
// proxy-owned provenance marker. The route takes history in input, so the id
// is omitted and the history is served. A call whose id names history no
// materializer assembled is refused: a nonempty slice is not proof, and the
// policy never invents the missing conversation.
func TestPreviewPolicyDistinguishesMaterializedHistoryFromMissingHistory(t *testing.T) {
	t.Parallel()
	itemHistory := []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "first"}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "answer"}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "second"}}},
	}
	messageHistory := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("first")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("answer")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}},
	}
	// The positive fixture is produced by the REAL materializer, so a policy
	// that only accepts a hand-set marker still fails the positive proof.
	materialized := materializeParentThenChild(t, itemHistory[:1], itemHistory[2:])
	if err := materialized.Validate(); err != nil {
		t.Fatalf("the materialized fixture must be a valid canonical call: %v", err)
	}
	assertMaterializedContinuationServed(t, materialized)

	t.Run("message authority with the materializer marker", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: messageHistory, PreviousResponseID: "resp_parent"}
		aipcont.MarkMaterialized(&call)
		assertMaterializedContinuationServed(t, call)
	})

	for _, tc := range []struct {
		name string
		call aipapi.Call
	}{
		// The reviewer's exploit: a valid parent id plus a full-looking
		// history that no materializer assembled. Length is not provenance.
		{name: "full history without materializer proof", call: aipapi.Call{Items: itemHistory, PreviousResponseID: "resp_parent"}},
		// The reviewer's minimal example: only the next user turn.
		{name: "unresolved parent with only new input", call: aipapi.Call{Items: itemHistory[2:], PreviousResponseID: "resp_parent"}},
		{name: "empty input", call: aipapi.Call{Items: []aipapi.Item{}, PreviousResponseID: "resp_parent"}},
		{name: "forged marker value", call: func() aipapi.Call {
			call := aipapi.Call{Items: itemHistory, PreviousResponseID: "resp_parent"}
			call.Extensions = map[string]json.RawMessage{aipcont.MaterializerProvenanceExtension: json.RawMessage(`1`)}
			return call
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call.Validate(); err != nil {
				t.Fatalf("the refusal fixture must be a valid canonical call: %v", err)
			}
			admission, err := responses.AdmitCall(responses.ProjectInput{Call: tc.call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			policyRequireRefusal(t, err, responses.PolicyReasonPreviousResponseMissingHistory, "PreviousResponseID")
			policyRequireZeroAdmission(t, admission, tc.name)
			if strings.Contains(err.Error(), "resp_parent") {
				t.Fatalf("the refusal diagnostic must not carry the proxy-owned id: %v", err)
			}
		})
	}
}

// assertMaterializedContinuationServed proves an admitted continuation carries
// its full history and omits only the proxy-owned id, in both postures.
func assertMaterializedContinuationServed(t *testing.T, call aipapi.Call) {
	t.Helper()
	admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireOmissions(t, admission, []responses.Omission{{
		Field: "PreviousResponseID", Wire: "previous_response_id", Reason: responses.PolicyReasonPreviousResponseOmitted,
	}})
	policyRequireBodyFieldAbsent(t, admission, "previous_response_id")
	body := decodedBody(t, admission.Request)
	requireSequence(t, "the materialized call must carry its full history in input",
		inputSequence(t, body), []string{"message:user", "message:assistant", "message:user"})

	strictAdmission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeStrict)
	if !sameRequest(strictAdmission.Request, admission.Request) {
		t.Fatalf("strict posture must serve a materialized continuation with the same full-history request, got %s", describeRequest(strictAdmission.Request))
	}
}

// materializeParentThenChild builds the positive fixture through the real
// continuation store and resolver: the parent's input and output are replayed,
// then the child's new input follows.
func materializeParentThenChild(t *testing.T, parentInput, newInput []aipapi.Item) aipapi.Call {
	t.Helper()
	ctx := context.Background()
	scope := aipcont.Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := aipcont.NewMemoryStore()
	id, err := store.Reserve(ctx, scope, aipcont.StoragePolicy{TTL: time.Hour})
	if err != nil {
		t.Fatalf("reserve the parent: %v", err)
	}
	if err := store.PutTerminal(ctx, aipcont.ContinuationRecord{
		ID: id, Scope: scope, Terminal: true,
		InputItems: parentInput,
		OutputItems: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "answer"}},
		}},
	}); err != nil {
		t.Fatalf("store the parent: %v", err)
	}
	resolver := aipcont.NewResolver(store, aipcont.Bounds{})
	call, _, err := resolver.ResolveParent(ctx, scope, id.String(), aipapi.Call{
		Items:              newInput,
		PreviousResponseID: id.String(),
	})
	if err != nil {
		t.Fatalf("ResolveParent: %v", err)
	}
	return call
}

// TestPreviewPolicySitsAroundTheProjectorRefusal pins the boundary: the 4.2
// projector still refuses a call carrying previous_response_id, and the policy
// is the layer that decides a materialized call may be served with the id
// omitted.
func TestPreviewPolicySitsAroundTheProjectorRefusal(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "first"}}},
		},
		PreviousResponseID: "resp_parent",
	}
	aipcont.MarkMaterialized(&call)
	if err := call.Validate(); err != nil {
		t.Fatalf("the fixture must be a valid canonical call: %v", err)
	}
	_, err := responses.ProjectCall(responses.ProjectInput{Call: call, Model: fixtureModel})
	if !errors.Is(err, responses.ErrCallNotRepresentable) {
		t.Fatalf("the projector must keep refusing a call carrying previous_response_id, got %v", err)
	}
	admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: fixtureModel}, responses.PolicyModeInteroperable)
	policyRequireBodyFieldAbsent(t, admission, "previous_response_id")
}

// TestPreviewPolicyRefusesPreviousResponseOnTheWire is the wire-side
// previous-response proof: a document that still carries the id is refused,
// so a policy that forwards it cannot pass.
func TestPreviewPolicyRefusesPreviousResponseOnTheWire(t *testing.T) {
	t.Parallel()
	document := policyBaseBody(t)
	document["previous_response_id"] = json.RawMessage(`"resp_parent"`)
	admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, policyMarshalBody(t, document)))
	policyRequireRefusal(t, err, responses.PolicyReasonPreviousResponseOnWire, "previous_response_id")
	policyRequireZeroAdmission(t, admission, "previous_response_id on the wire")
}

// TestPreviewPolicyRefusesEveryDocumentedUnsupportedField is the wire-field
// table.
//
// The table is the documented list from requirement 6.2, not a sample: a
// guard that drops one row admits that field and fails here for its name.
func TestPreviewPolicyRefusesEveryDocumentedUnsupportedField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		field string
		value json.RawMessage
	}{
		{field: "background", value: json.RawMessage(`true`)},
		{field: "conversation", value: json.RawMessage(`"conv_1"`)},
		{field: "max_output_tokens", value: json.RawMessage(`64`)},
		{field: "max_tool_calls", value: json.RawMessage(`2`)},
		{field: "metadata", value: json.RawMessage(`{"k":"v"}`)},
		{field: "moderation", value: json.RawMessage(`{"model":"m"}`)},
		{field: "multi_agent", value: json.RawMessage(`{"enabled":true}`)},
		{field: "prompt", value: json.RawMessage(`{"id":"p"}`)},
		{field: "prompt_cache_retention", value: json.RawMessage(`"24h"`)},
		{field: "safety_identifier", value: json.RawMessage(`"user_1"`)},
		{field: "temperature", value: json.RawMessage(`0.5`)},
		{field: "top_logprobs", value: json.RawMessage(`3`)},
		{field: "top_p", value: json.RawMessage(`0.9`)},
		{field: "truncation", value: json.RawMessage(`"auto"`)},
		{field: "user", value: json.RawMessage(`"u1"`)},
	}
	if len(cases) != 15 {
		t.Fatalf("the documented unsupported-field table must carry all 15 fields, got %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			document := policyBaseBody(t)
			document[tc.field] = tc.value
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, policyMarshalBody(t, document)))
			policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedField, tc.field)
			policyRequireZeroAdmission(t, admission, "unsupported field "+tc.field)
		})
	}
}

// TestPreviewPolicyRefusesEveryDocumentedHostedTool is the hosted-tool table.
//
// The table is the documented class list from requirement 6.4, not a sample.
// A hosted tool is refused wherever it sits in the catalogue: the proof puts a
// lawful function tool first and the hosted tool second, so a guard that
// samples the first declaration fails here.
func TestPreviewPolicyRefusesEveryDocumentedHostedTool(t *testing.T) {
	t.Parallel()
	hosted := []struct {
		class string
		wire  string
	}{
		{class: "image generation", wire: "image_generation"},
		{class: "file search", wire: "file_search"},
		{class: "Code Interpreter", wire: "code_interpreter"},
		{class: "native computer use", wire: "computer_use_preview"},
		{class: "hosted MCP/connectors", wire: "mcp"},
		{class: "Responses tool_search", wire: "tool_search"},
	}
	if len(hosted) != 6 {
		t.Fatalf("the documented hosted-tool table must carry all six classes, got %d", len(hosted))
	}
	for _, tc := range hosted {
		t.Run(tc.class, func(t *testing.T) {
			t.Parallel()
			body := policyDocumentWithTools(t, []any{
				map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
				map[string]any{"type": tc.wire},
			})
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
			policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "input[0].tools[1].type")
			policyRequireZeroAdmission(t, admission, tc.class)
			if !strings.Contains(err.Error(), tc.wire) {
				t.Fatalf("the refusal must name the hosted tool type %q: %v", tc.wire, err)
			}
		})
	}

	t.Run("top-level programmatic_tool_calling", func(t *testing.T) {
		t.Parallel()
		document := policyBaseBody(t)
		document["programmatic_tool_calling"] = json.RawMessage(`true`)
		admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, policyMarshalBody(t, document)))
		policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "programmatic_tool_calling")
		policyRequireZeroAdmission(t, admission, "programmatic_tool_calling")
	})

	// The top-level catalogue is the second tool carrier. A lawful first tool
	// followed by a hosted tool proves the guard walks the array instead of
	// sampling its first entry, so removing the top-level branch fails here.
	t.Run("top-level hosted tool after a lawful function", func(t *testing.T) {
		t.Parallel()
		body := policyDocumentWithTopLevelTools(t, []any{
			map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
			map[string]any{"type": "image_generation"},
		})
		admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
		policyRequireRefusal(t, err, responses.PolicyReasonUnsupportedTool, "tools[1].type")
		policyRequireZeroAdmission(t, admission, "top-level hosted tool")
		if !strings.Contains(err.Error(), "image_generation") {
			t.Fatalf("the refusal must name the hosted tool type: %v", err)
		}
	})

	t.Run("top-level function tools stay admissible", func(t *testing.T) {
		t.Parallel()
		body := policyDocumentWithTopLevelTools(t, []any{
			map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
		})
		admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
		if err != nil {
			t.Fatalf("a top-level function tool catalogue must stay admissible, got %v", err)
		}
		if !bytes.Equal(admission.Request.Body, body) {
			t.Fatalf("the admitted request must be the projected request unchanged")
		}
	})

	t.Run("function tools stay admissible", func(t *testing.T) {
		t.Parallel()
		body := policyDocumentWithTools(t, []any{
			map[string]any{"type": "function", "name": "lawful", "parameters": map[string]any{"type": "object"}},
		})
		admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
		if err != nil {
			t.Fatalf("a function tool catalogue must stay admissible, got %v", err)
		}
		if !bytes.Equal(admission.Request.Body, body) {
			t.Fatalf("the admitted request must be the projected request unchanged")
		}
	})
}

// TestPreviewPolicyAdmitsEverySupportedFixture is the positive proof.
//
// Every supported fixture from the 4.1 matrix is admitted with zero
// omissions and the exact documented body, in both downstream delivery modes.
// The policy therefore degrades nothing that the wire contract supports,
// including the minimal, tool and multimodal cases.
func TestPreviewPolicyAdmitsEverySupportedFixture(t *testing.T) {
	t.Parallel()
	cases := wireContractCases()
	if len(cases) == 0 {
		t.Fatal("the wire contract matrix is empty")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, mode := range deliveryModes {
				call := tc.call
				call.Invocation = aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: mode}
				admission := policyAdmit(t, responses.ProjectInput{Call: call, Model: tc.model}, responses.PolicyModeInteroperable)
				policyRequireOmissions(t, admission, nil)
				got := canonicalJSON(t, "admitted body", admission.Request.Body)
				want := canonicalJSON(t, "documented body", []byte(tc.wantBody))
				if got != want {
					t.Fatalf("mode %s: the policy must admit the supported fixture unchanged\n--- admitted ---\n%s\n--- documented ---\n%s", mode, got, want)
				}
			}
		})
	}
}

// TestPreviewPolicyAdmitsMinimalToolAndMultimodalRequests names the three
// positive shapes the task requires, independent of the fixture loop.
func TestPreviewPolicyAdmitsMinimalToolAndMultimodalRequests(t *testing.T) {
	t.Parallel()
	multimodal := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{
		aipapi.TextPart("Summarize the picture and the report."),
		{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,iVBORw0KGgo=", ImageMIME: "image/png"},
		aipapi.FilePart("data:application/pdf;base64,JVBERi0xLjQK", "application/pdf", "report.pdf"),
	}}}}
	cases := []struct {
		name  string
		call  aipapi.Call
		types []string
	}{
		{name: "minimal", call: policyMinimalCall(), types: []string{"message"}},
		{name: "tool", call: aipapi.Call{
			Tools:    []aipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("look it up")}}},
		}, types: []string{"additional_tools", "message"}},
		{name: "multimodal", call: multimodal, types: []string{"message"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.call.Validate(); err != nil {
				t.Fatalf("the positive fixture must be a valid canonical call: %v", err)
			}
			admission := policyAdmit(t, responses.ProjectInput{Call: tc.call, Model: fixtureModel}, responses.PolicyModeInteroperable)
			policyRequireOmissions(t, admission, nil)
			body := decodedBody(t, admission.Request)
			items := inputItems(t, body)
			gotTypes := make([]string, 0, len(items))
			for _, item := range items {
				typ, _ := item["type"].(string)
				gotTypes = append(gotTypes, typ)
			}
			if !slices.Equal(gotTypes, tc.types) {
				t.Fatalf("the admitted %s request must carry the supported input items %v, got %v", tc.name, tc.types, gotTypes)
			}
			policyRequireBodyFieldAbsent(t, admission, "temperature", "top_p", "max_output_tokens", "text", "parallel_tool_calls", "previous_response_id")
		})
	}
}

// TestPreviewPolicyRefusesMalformedProjectedBody pins the guard's fail-closed
// treatment of a document it cannot read. An arrived-but-unusable body is not
// an absent policy problem: it is refused.
func TestPreviewPolicyRefusesMalformedProjectedBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body []byte
	}{
		{name: "empty body"},
		{name: "not an object", body: []byte(`[]`)},
		{name: "null", body: []byte(`null`)},
		{name: "invalid json", body: []byte(`{`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			admission, err := responses.AdmitProjectedRequest(responses.WireRequest{Body: tc.body})
			policyRequireRefusal(t, err, responses.PolicyReasonMalformedRequest, "body")
			policyRequireZeroAdmission(t, admission, tc.name)
		})
	}
}

// TestPreviewPolicyRefusesMalformedToolCatalogue pins the tool guard's
// fail-closed treatment of a catalogue it cannot prove names a supported tool.
func TestPreviewPolicyRefusesMalformedToolCatalogue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		item       map[string]any
		wantReason responses.PolicyReason
		wantField  string
	}{
		{
			name:       "null catalogue",
			item:       map[string]any{"type": "additional_tools", "role": "developer", "tools": nil},
			wantReason: responses.PolicyReasonMalformedRequest,
			wantField:  "input[0].tools",
		},
		{
			name:       "tool is not an object",
			item:       map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{"image_generation"}},
			wantReason: responses.PolicyReasonUnsupportedTool,
			wantField:  "input[0].tools[0].type",
		},
		{
			name:       "tool type is missing",
			item:       map[string]any{"type": "additional_tools", "role": "developer", "tools": []any{map[string]any{"name": "untyped"}}},
			wantReason: responses.PolicyReasonUnsupportedTool,
			wantField:  "input[0].tools[0].type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(map[string]any{
				"model":  fixtureModel,
				"input":  []any{tc.item},
				"store":  false,
				"stream": true,
			})
			if err != nil {
				t.Fatalf("the crafted document cannot be encoded: %v", err)
			}
			admission, err := responses.AdmitProjectedRequest(policyRequestFromBody(t, body))
			policyRequireRefusal(t, err, tc.wantReason, tc.wantField)
			policyRequireZeroAdmission(t, admission, tc.name)
		})
	}
}

// TestPreviewPolicyRejectsUnknownMode pins the posture contract: an unknown
// posture is a configuration error, never a silent fallback to the
// interoperable default.
func TestPreviewPolicyRejectsUnknownMode(t *testing.T) {
	t.Parallel()
	admission, err := responses.AdmitCall(responses.ProjectInput{Call: policyMinimalCall(), Model: fixtureModel}, responses.PolicyMode("yolo"))
	if !errors.Is(err, responses.ErrUnknownPolicyMode) {
		t.Fatalf("an unknown posture must fail with ErrUnknownPolicyMode, got %v", err)
	}
	policyRequireZeroAdmission(t, admission, "unknown mode")

	empty := policyAdmit(t, responses.ProjectInput{Call: policyMinimalCall(), Model: fixtureModel}, "")
	policyRequireOmissions(t, empty, nil)
}

// TestPreviewPolicyRefusalDiagnosticsCarryNoContent pins the diagnostic
// boundary: a refusal names fields and reasons, never conversation content,
// a cache-hint value or an extension value.
func TestPreviewPolicyRefusalDiagnosticsCarryNoContent(t *testing.T) {
	t.Parallel()
	temperature := 0.5
	cacheCall := policyMinimalCall()
	cacheCall.PromptCacheKey = "cache-hint-value"
	extensionCall := policyMinimalCall()
	extensionCall.Extensions = map[string]json.RawMessage{"beta.feature": json.RawMessage(`"extension-value"`)}
	cases := []struct {
		name    string
		call    aipapi.Call
		markers []string
	}{
		{
			name: "generation option",
			call: func() aipapi.Call {
				call := policyMinimalCall()
				call.Messages[0].Parts = []aipapi.Part{aipapi.TextPart("SECRET-CONVERSATION-TEXT")}
				call.Options = aipapi.GenerationOptions{Temperature: &temperature}
				return call
			}(),
			markers: []string{"SECRET-CONVERSATION-TEXT"},
		},
		{name: "cache hint", call: cacheCall, markers: []string{"cache-hint-value"}},
		{name: "protocol extension", call: extensionCall, markers: []string{"extension-value"}},
		{
			name:    "missing history",
			call:    aipapi.Call{Items: []aipapi.Item{}, PreviousResponseID: "resp-parent-value"},
			markers: []string{"resp-parent-value"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := responses.AdmitCall(responses.ProjectInput{Call: tc.call, Model: fixtureModel}, responses.PolicyModeStrict)
			if err == nil {
				t.Fatal("the strict posture must refuse the unsupported control")
			}
			for _, marker := range tc.markers {
				if strings.Contains(err.Error(), marker) {
					t.Fatalf("the refusal diagnostic must not carry %q: %v", marker, err)
				}
			}
		})
	}
}
