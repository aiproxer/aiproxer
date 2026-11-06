package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// hostedDeclarationCall is one admissible request carrying the preserved
// hosted web-search declaration alongside a caller-executed function tool and
// a complete legal message trajectory. The trajectory is the no-fabrication
// control: consuming the declaration must not drop or invent a history item.
func hostedDeclarationCall() aipapi.Call {
	return aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "find the weather"}},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-2",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleAssistant,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "I will look it up."}},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-3",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "thank you"}},
			},
		},
		Tools: []aipapi.ToolDef{{
			Name:       "lookup",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
		Extensions: map[string]json.RawMessage{
			declaredWebSearchExtension: json.RawMessage(`["web_search_preview","web_search"]`),
		},
	}
}

// hostedDeclarationInvocation projects the canonical call through the same
// backend-plugin metadata bridge the host uses. The carrier is carried under
// the documented "ext." metadata prefix and restored by the connector.
func hostedDeclarationInvocation(t *testing.T, call aipapi.Call) backendplugin.Invocation {
	t.Helper()
	inv := backendplugin.Invocation{
		RequestID:        "req-exec",
		AttemptID:        "att-exec",
		ALegID:           "aleg-exec",
		BLegID:           "bleg-exec",
		CanonicalModelID: FactoryKind + "/gpt-5",
		NativeModelID:    "gpt-5",
		DeliveryMode:     string(aipapi.DeliveryModeNonStreaming),
		Operation:        string(aipapi.OperationOpenAIResponses),
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, call, nil); err != nil {
		t.Fatalf("project the canonical call onto the invocation ABI: %v", err)
	}
	for _, tool := range call.Tools {
		inv.Tools = append(inv.Tools, backendplugin.ToolDef{
			Name:           tool.Name,
			Description:    tool.Description,
			ParametersJSON: backendplugin.RawJSONFromBytes(tool.Parameters),
		})
	}
	if !inv.ItemAuthority || len(inv.Items) != len(call.Items) {
		t.Fatalf("the invocation bridge must carry every history item: authority=%t items=%d, want %d",
			inv.ItemAuthority, len(inv.Items), len(call.Items))
	}
	if _, ok := inv.SafeMetadata[backendplugin.MetaExtensionPrefix+declaredWebSearchExtension]; !ok {
		t.Fatalf("the invocation bridge lost the hosted-declaration carrier: %+v", inv.SafeMetadata)
	}
	return inv
}

// TestExecute_InteroperableHostedDeclarationOmissionIsLoggedAndAbsentUpstream
// is the connector-side end-to-end proof of requirement 6.4: the default
// posture serves the request, records one operator-visible omission per
// declaration, sends no hosted declaration upstream, and preserves every
// history item exactly.
func TestExecute_InteroperableHostedDeclarationOmissionIsLoggedAndAbsentUpstream(t *testing.T) {
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	frames, err := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, hostedDeclarationCall()))
	if err != nil {
		t.Fatalf("interoperable Execute: %v", err)
	}
	terminal := terminalFrameOf(t, frames)
	if terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal status = %q, want success", terminal.Status)
	}
	if got := eventDeltas(frames); got != "Hello from the plan." {
		t.Fatalf("assistant text = %q, want the complete provider answer", got)
	}

	requests := provider.captured()
	if len(requests) != 1 {
		t.Fatalf("the interoperable posture must serve exactly one provider request, got %d", len(requests))
	}
	body := requests[0].Body
	if bytes.Contains(body, []byte("web_search")) {
		t.Fatalf("the provider request must not carry a hosted web-search declaration:\n%s", body)
	}
	var document struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the provider request: %v", err)
	}
	// Item-authority calls carry their catalogue in one additional_tools input
	// item; a message-authority call would carry the same declarations at the
	// top level. Collect both positions so the assertion is about the
	// declaration, not the authority form.
	var tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	tools = append(tools, document.Tools...)
	for _, item := range document.Input {
		if item.Type == "additional_tools" {
			tools = append(tools, item.Tools...)
		}
	}
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Name != "lookup" {
		t.Fatalf("the caller-executed function tool must stay canonical and alone, got %+v", tools)
	}

	var messageRoles, messageTexts []string
	for _, item := range document.Input {
		if item.Type != "message" {
			continue
		}
		messageRoles = append(messageRoles, item.Role)
		for _, content := range item.Content {
			if content.Type == "input_text" {
				messageTexts = append(messageTexts, content.Text)
			}
		}
	}
	wantRoles := []string{"user", "assistant", "user"}
	if !slices.Equal(messageRoles, wantRoles) {
		t.Fatalf("provider history roles = %v, want the exact authored order %v", messageRoles, wantRoles)
	}
	wantTexts := []string{"find the weather", "I will look it up.", "thank you"}
	if !slices.Equal(messageTexts, wantTexts) {
		t.Fatalf("provider history texts = %v, want the exact authored trajectory %v", messageTexts, wantTexts)
	}

	// One omission per declaration, with the carrier path, the declared type,
	// the wire field and the dedicated stable reason. The record carries no
	// conversation content.
	recorded := logs.String()
	for _, want := range []string{
		string(responses.PolicyReasonWebSearchDeclaration),
		`Extensions[\"openairesponses.web_search_declarations\"][0]`,
		`(type \"web_search_preview\")`,
		`"wire_field":"tools"`,
	} {
		if !strings.Contains(recorded, want) {
			t.Fatalf("the hosted-declaration omission record is missing %q; log=%s", want, recorded)
		}
	}
	if strings.Contains(recorded, "find the weather") {
		t.Fatalf("the omission record must not carry conversation content; log=%s", recorded)
	}
}

// hostedSearchHistoryCall is one canonical trajectory carrying a
// model-authored hosted web-search item. It has no legal public projection, so
// the connector must refuse it rather than drop or synthesize history.
func hostedSearchHistoryCall(carrier json.RawMessage) aipapi.Call {
	return aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "find the weather"}},
			},
			{
				Kind:   aipapi.ItemKindExtension,
				ID:     "ws-1",
				Status: aipapi.ItemStatusCompleted,
				Extension: &aipapi.OpaqueExtension{
					Namespace: "openai",
					Type:      "web_search_call",
					Direction: "response",
					Data:      json.RawMessage(`{"action":{"type":"search","query":"weather"}}`),
				},
			},
		},
		Extensions: map[string]json.RawMessage{declaredWebSearchExtension: carrier},
	}
}

// TestExecute_InteroperableOmissionDoesNotApplyToHistory proves the boundary of
// the omission: consuming the carrier removes only the declaration. A
// model-authored hosted-search history item still has no canonical equivalent,
// so the interoperable posture refuses it before provider I/O instead of
// dropping or synthesizing it.
func TestExecute_InteroperableOmissionDoesNotApplyToHistory(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
	})

	call := hostedSearchHistoryCall(json.RawMessage(`["web_search_preview"]`))
	if err := call.Validate(); err != nil {
		t.Fatalf("the history fixture must be a valid canonical call: %v", err)
	}

	frames, err := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, call))
	if !errors.Is(err, responses.ErrCallNotRepresentable) {
		t.Fatalf("interoperable history Execute error %v, want the projector's refusal", err)
	}
	var projection *responses.ProjectionError
	if !errors.As(err, &projection) || projection.Reason != responses.ProjectionReasonItemKind || projection.Field != "Items[1]" {
		t.Fatalf("the refusal must be %q at Items[1], got %+v", responses.ProjectionReasonItemKind, projection)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a refused hosted-search history item reached the provider: %d request(s)", len(requests))
	}
	events, evidence, _, failures := countFrames(frames)
	if events != 0 || evidence != 0 {
		t.Fatalf("events=%d evidence=%d, want no fabricated output or usage", events, evidence)
	}
	if failures != 1 {
		t.Fatalf("the history refusal must publish exactly one failure terminal, got %d", failures)
	}
}

// TestExecute_StrictHostedDeclarationRefusalPrecedesProjectionAndNetwork pins
// requirement 6.4's strict posture and the policy ordering: the carrier is
// consumed before projection, so a request that carries both the carrier and
// a projection-invalid history item is refused for the declaration, not for
// projection, and no provider request is opened.
func TestExecute_StrictHostedDeclarationRefusalPrecedesProjectionAndNetwork(t *testing.T) {
	t.Parallel()
	provider := newPlanReference(t, completeScript)
	storePath, record := stageProfileStore(t)
	selectProfile(t, storePath, record.ID)
	inst := configureForExecution(t, provider.baseURL(), map[string]string{
		"profile_store_path": storePath,
		"policy_mode":        string(responses.PolicyModeStrict),
	})

	call := hostedSearchHistoryCall(json.RawMessage(`["web_search_preview"]`))
	if err := call.Validate(); err != nil {
		t.Fatalf("the ordering fixture must be a valid canonical call: %v", err)
	}

	frames, err := runExecute(context.Background(), inst, hostedDeclarationInvocation(t, call))
	if !errors.Is(err, responses.ErrRequestNotAdmissible) {
		t.Fatalf("strict Execute error %v, want the preview-policy refusal", err)
	}
	var policyErr *responses.PolicyError
	if !errors.As(err, &policyErr) || policyErr.Reason != responses.PolicyReasonWebSearchDeclaration {
		t.Fatalf("strict Execute error %v must be the hosted-declaration refusal, got %+v", err, policyErr)
	}
	if requests := provider.captured(); len(requests) != 0 {
		t.Fatalf("a strict hosted-declaration refusal reached the provider: %d request(s)", len(requests))
	}
	events, evidence, _, failures := countFrames(frames)
	if events != 0 || evidence != 0 {
		t.Fatalf("events=%d evidence=%d, want no fabricated output or usage", events, evidence)
	}
	if failures != 1 {
		t.Fatalf("the strict refusal must publish exactly one failure terminal, got %d", failures)
	}
}
