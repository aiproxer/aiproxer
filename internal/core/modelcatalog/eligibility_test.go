package modelcatalog_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEligibilityResolver_noLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitUnknown},
		},
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible || got.Reason != modelcatalog.EligibilityEligible {
		t.Fatalf("got %+v", got)
	}
}

func TestEligibilityResolver_noEstimate_eligible(t *testing.T) {
	t.Parallel()
	// Estimator returns unavailable when session hints without contribution
	est := modelcatalog.DefaultSizeEstimator{}
	r := modelcatalog.NewEligibilityResolver(est)
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 10},
		},
	}
	call := aipapi.Call{
		Session:  aipapi.SessionRef{ResumeToken: "t"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("manybyteshere")}}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible {
		t.Fatalf("expected eligible when estimate unavailable, got %+v", got)
	}
}

func TestEligibilityResolver_underLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1000},
		},
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible || got.Reason != modelcatalog.EligibilityEligible {
		t.Fatalf("got %+v", got)
	}
	if !got.Estimate.Available {
		t.Fatalf("estimate should be available: %+v", got.Estimate)
	}
}

func TestEligibilityResolver_overLimit_ineligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 1},
		},
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ab")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if got.IsEligible || got.Reason != modelcatalog.EligibilityContextLimitExceeded {
		t.Fatalf("got %+v", got)
	}
}

func TestEligibilityResolver_toolResultTextCountsTowardLimit(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 10},
		},
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: "c",
		ToolName:   "t",
		Text:       "a text-only tool result well over the limit",
	}}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if got.IsEligible || got.Reason != modelcatalog.EligibilityContextLimitExceeded {
		t.Fatalf("got %+v; a text-only tool result must count toward the context limit", got)
	}
}

func TestEligibilityResolver_exactlyAtLimit_eligible(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 2},
		},
	}
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ab")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible {
		t.Fatalf("at limit should be eligible, got %+v", got)
	}
}

func TestEligibilityResolver_itemAuthorityCountsToolResult(t *testing.T) {
	t.Parallel()
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 100},
		},
	}
	call := aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "find the widget"}},
		},
		{
			Kind: aipapi.ItemKindToolCall, ID: "tc-1", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"widget"}`)},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "tr-1", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: "lookup", Output: strings.Repeat("x", 4096)},
		},
	}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if got.IsEligible || got.Reason != modelcatalog.EligibilityContextLimitExceeded {
		t.Fatalf("got %+v; an item-authority tool result must count toward the context limit", got)
	}
}

func TestEligibilityResolver_manySmallItemAuthorityItemsRemainEligible(t *testing.T) {
	t.Parallel()
	items := make([]aipapi.Item, 600)
	for i := range items {
		items[i] = aipapi.Item{
			Kind: aipapi.ItemKindMessage, ID: "m-" + strconv.Itoa(i), Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "x"}},
		}
	}
	r := modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	facts := modelcatalog.EffectiveFacts{Facts: modelcatalog.ModelFacts{
		ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 131072},
	}}
	call := aipapi.Call{Items: items}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "b", Model: "m"}}
	got := r.Check(context.Background(), cand, call, facts)
	if !got.IsEligible {
		t.Fatalf("600 one-character items must remain eligible under 131072 bytes: %+v", got)
	}
}
