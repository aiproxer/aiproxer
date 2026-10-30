package adapter_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestInvocationFromCall_mapsToolChoiceBothDirections(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		ID:      "req-tc",
		Session: aipapi.SessionRef{ALegID: "aleg"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Tools:      []aipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "fn"},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "m"},
		Key:     "fake:m",
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: 0}

	inv, err := adapter.InvocationFromCall(call, cand, neg)
	if err != nil {
		t.Fatal(err)
	}
	if inv.ToolChoice == nil || *inv.ToolChoice != "required:fn" {
		t.Fatalf("inv tool_choice=%v", inv.ToolChoice)
	}

	back, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if back.ToolChoice.Mode != aipapi.ToolChoiceRequired || back.ToolChoice.Name != "fn" {
		t.Fatalf("back tool_choice=%+v", back.ToolChoice)
	}
}

func TestInvocationFromCall_rejectsNamedNonRequiredToolChoiceBeforeExecute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		tc    aipapi.ToolChoice
		tools []aipapi.ToolDef
	}{
		{name: "auto named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, Name: "fn"}, tools: []aipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}},
		{name: "any named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, Name: "fn"}, tools: []aipapi.ToolDef{{Name: "fn", Parameters: []byte(`{"type":"object"}`)}}},
		{name: "none named", tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone, Name: "fn"}, tools: nil},
	}
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "m"},
		Key:     "fake:m",
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: 0}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				ID:      "req-tc-bad",
				Session: aipapi.SessionRef{ALegID: "aleg"},
				Messages: []aipapi.Message{{
					Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
				Tools:      tc.tools,
				ToolChoice: tc.tc,
			}
			_, err := adapter.InvocationFromCall(call, cand, neg)
			if err == nil {
				t.Fatal("expected InvocationFromCall rejection before plugin Execute")
			}
		})
	}
}
