package responses_test

// The baseline half of the honest reduced reasoning-replay capability (spec
// task 8.2).
//
// Requirements 10.4 and 10.5 allow exact public Responses reasoning replay only
// after a live SIWC proof. Until that proof exists the connector keeps the
// baseline public canonical history and advertises no exact replay, so the
// production projector must REFUSE reasoning history with the stable
// `reasoning_replay` reason instead of dropping it, converting it into a
// different dialect, or asking the route for encrypted reasoning content.
//
// The capability side of the same contract is pinned in
// ../reasoning_capability_test.go. The opt-in live probe is a separate RAW
// provider probe and is not the production path.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReasoningReplayStaysReducedByDefault(t *testing.T) {
	t.Parallel()
	exact := aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
		Opaque: json.RawMessage(
			`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"analysis"}],"encrypted_content":"cipher"}`,
		),
	}
	cases := []struct {
		name string
		call aipapi.Call
	}{
		{
			name: "message authority assistant reasoning part",
			call: aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &exact},
					aipapi.TextPart("answer"),
				}},
			}},
		},
		{
			name: "item authority reasoning item",
			call: aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindReasoning, ID: "rs_1", Reasoning: &aipapi.ReasoningItem{
					Reasoning: &exact,
				}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := responses.ProjectCall(responses.ProjectInput{Call: tc.call, Model: fixtureModel})
			if err == nil {
				t.Fatal("the projector must refuse reasoning history while exact replay is not advertised")
			}
			var projection *responses.ProjectionError
			if !errors.As(err, &projection) {
				t.Fatalf("projection error = %T (%v), want *responses.ProjectionError", err, err)
			}
			if projection.Reason != responses.ProjectionReasonReasoningReplay {
				t.Fatalf("projection reason = %q, want %q", projection.Reason, responses.ProjectionReasonReasoningReplay)
			}
		})
	}
}

func TestBaselineRequestDoesNotAskForEncryptedReasoning(t *testing.T) {
	t.Parallel()
	req := projectedRequest(t, responses.ProjectInput{
		Model: fixtureModel,
		Call: aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
		}},
	})
	body := decodedBody(t, req)
	if _, ok := body["include"]; ok {
		t.Fatalf("the baseline request must not ask for encrypted reasoning content: %v", body["include"])
	}
	wire := string(req.Body)
	for _, forbidden := range []string{
		"reasoning.encrypted_content",
		"previous_response_id",
		"/responses/compact",
		"codex.responses.compaction.v2",
		"/backend-api",
		"chatgpt.com",
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("the baseline request must not carry %q: %s", forbidden, wire)
		}
	}
}
