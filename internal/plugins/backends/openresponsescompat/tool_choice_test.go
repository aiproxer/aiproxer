package openresponsescompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBackendToolChoice_OmittedWhenToolsEmpty(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Tools: nil,
		ToolChoice: aipapi.ToolChoice{
			Mode: aipapi.ToolChoiceAuto,
		},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				Role:   aipapi.RoleUser,
				Status: aipapi.ItemStatusCompleted,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "hello"},
				},
			},
		},
	}
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("buildCreateRequest failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := got["tool_choice"]; ok {
		t.Errorf("expected tool_choice to be omitted when call.Tools is empty")
	}
}

func TestBackendToolChoice_EncodingCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tc   aipapi.ToolChoice
		want map[string]any
	}{
		{
			name: "auto subset",
			tc:   aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"fn1", "fn2"}},
			want: map[string]any{
				"type": "allowed_tools",
				"mode": "auto",
				"tools": []any{
					map[string]any{"type": "function", "name": "fn1"},
					map[string]any{"type": "function", "name": "fn2"},
				},
			},
		},
		{
			name: "required subset maps to mode required",
			tc:   aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny, AllowedTools: []string{"fn1"}},
			want: map[string]any{
				"type":  "allowed_tools",
				"mode":  "required",
				"tools": []any{map[string]any{"type": "function", "name": "fn1"}},
			},
		},
		{
			name: "none subset",
			tc:   aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone, AllowedTools: []string{"fn1"}},
			want: map[string]any{
				"type":  "allowed_tools",
				"mode":  "none",
				"tools": []any{map[string]any{"type": "function", "name": "fn1"}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := aipapi.Call{
				Tools: []aipapi.ToolDef{
					{Name: "fn1", Description: "desc"},
					{Name: "fn2", Description: "desc"},
				},
				ToolChoice: tc.tc,
				Items: []aipapi.Item{
					{
						Kind:    aipapi.ItemKindMessage,
						Role:    aipapi.RoleUser,
						Status:  aipapi.ItemStatusCompleted,
						Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}},
					},
				},
			}
			body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatalf("buildCreateRequest failed: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if !reflect.DeepEqual(got["tool_choice"], tc.want) {
				t.Errorf("tool_choice mismatch\ngot:  %+v\nwant: %+v", got["tool_choice"], tc.want)
			}
		})
	}
}

func TestBackendToolChoice_PreservesOrder(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Tools: []aipapi.ToolDef{
			{Name: "z", Description: "desc"},
			{Name: "a", Description: "desc"},
			{Name: "m", Description: "desc"},
		},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"z", "a", "m"},
		},
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Status:  aipapi.ItemStatusCompleted,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}},
			},
		},
	}
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("buildCreateRequest failed: %v", err)
	}
	var got struct {
		ToolChoice struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	tools := got.ToolChoice.Tools
	if len(tools) != 3 || tools[0].Name != "z" || tools[1].Name != "a" || tools[2].Name != "m" {
		t.Fatalf("subset order lost: %+v", tools)
	}
}
