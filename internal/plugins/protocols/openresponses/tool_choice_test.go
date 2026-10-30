package openresponses

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestToolChoiceWire_allowedToolsNativeEncoding(t *testing.T) {
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
			t.Parallel()
			raw, err := encodeToolChoice(tc.tc)
			if err != nil {
				t.Fatalf("encodeToolChoice failed: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("invalid wire JSON %s: %v", raw, err)
			}
			if !jsonEqual(got, tc.want) {
				t.Fatalf("wire=%s want=%v", raw, tc.want)
			}
		})
	}
}

func TestToolChoiceWire_allowedToolsPreservesOrder(t *testing.T) {
	t.Parallel()
	tc := aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{"z", "a", "m"}}
	raw, err := encodeToolChoice(tc)
	if err != nil {
		t.Fatalf("encodeToolChoice failed: %v", err)
	}
	var got struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid wire JSON: %v", err)
	}
	if len(got.Tools) != 3 || got.Tools[0].Name != "z" || got.Tools[1].Name != "a" || got.Tools[2].Name != "m" {
		t.Fatalf("subset order lost: %+v", got.Tools)
	}
}

func jsonEqual(a, b map[string]any) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}
