package toolnamecorrection_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolnamecorrection"
	"gopkg.in/yaml.v3"
)

func TestFeatureID(t *testing.T) {
	t.Parallel()
	if toolnamecorrection.ID != "tool-name-correction" {
		t.Fatalf("ID=%q, want tool-name-correction", toolnamecorrection.ID)
	}
}

func TestDecodeConfig_Defaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty mapping", raw: "{}\n"},
		{name: "null config", raw: "null\n"},
		{name: "empty document", raw: ""},
		{name: "comment only", raw: "# defaults apply\n"},
		{
			name: "explicit nulls",
			raw:  "max_corrections_per_turn: null\ncorrect_after_reasoning: null\npass_through_names: null\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if cfg.MaxCorrectionsPerTurn != 1 {
				t.Fatalf("max_corrections_per_turn=%d, want 1", cfg.MaxCorrectionsPerTurn)
			}
			if cfg.SkipAfterReasoning {
				t.Fatal("SkipAfterReasoning=true, want false (correct_after_reasoning defaults true)")
			}
			if len(cfg.PassThroughNames) != 0 {
				t.Fatalf("pass_through_names=%v, want empty", cfg.PassThroughNames)
			}
		})
	}
}

func TestDecodeConfig_AcceptsConfiguredValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		raw          string
		wantBudget   uint8
		wantAfter    bool
		wantPatterns []string
	}{
		{
			name:       "budget at minimum",
			raw:        "max_corrections_per_turn: 1\n",
			wantBudget: 1,
			wantAfter:  true,
		},
		{
			name:       "budget in range",
			raw:        "max_corrections_per_turn: 2\n",
			wantBudget: 2,
			wantAfter:  true,
		},
		{
			name:       "budget at maximum",
			raw:        "max_corrections_per_turn: 3\n",
			wantBudget: 3,
			wantAfter:  true,
		},
		{
			name:       "reasoning correction disabled",
			raw:        "correct_after_reasoning: false\n",
			wantBudget: 1,
			wantAfter:  false,
		},
		{
			name:       "reasoning correction enabled explicitly",
			raw:        "correct_after_reasoning: true\n",
			wantBudget: 1,
			wantAfter:  true,
		},
		{
			name:         "pass-through patterns",
			raw:          "pass_through_names: [\"mcp__*\", \"search_*\"]\n",
			wantBudget:   1,
			wantAfter:    true,
			wantPatterns: []string{"mcp__*", "search_*"},
		},
		{
			name:         "operator example",
			raw:          "max_corrections_per_turn: 3\ncorrect_after_reasoning: false\npass_through_names: [\"mcp__*\"]\n",
			wantBudget:   3,
			wantAfter:    false,
			wantPatterns: []string{"mcp__*"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := decodeConfig(t, tc.raw)
			if err != nil {
				t.Fatalf("DecodeConfig: %v", err)
			}
			if cfg.MaxCorrectionsPerTurn != tc.wantBudget {
				t.Fatalf("max_corrections_per_turn=%d, want %d", cfg.MaxCorrectionsPerTurn, tc.wantBudget)
			}
			if cfg.SkipAfterReasoning == tc.wantAfter {
				t.Fatalf("SkipAfterReasoning=%v, want %v (correct_after_reasoning=%v)",
					cfg.SkipAfterReasoning, !tc.wantAfter, tc.wantAfter)
			}
			if !slices.Equal(cfg.PassThroughNames, tc.wantPatterns) {
				t.Fatalf("pass_through_names=%v, want %v", cfg.PassThroughNames, tc.wantPatterns)
			}
		})
	}
}

func TestDecodeConfig_PassThroughNamesBound(t *testing.T) {
	t.Parallel()
	cfg, err := decodeConfig(t, passThroughNamesYAML(32))
	if err != nil {
		t.Fatalf("32 patterns: DecodeConfig: %v", err)
	}
	if len(cfg.PassThroughNames) != 32 {
		t.Fatalf("pass_through_names has %d entries, want 32", len(cfg.PassThroughNames))
	}
	_, err = decodeConfig(t, passThroughNamesYAML(33))
	if err == nil {
		t.Fatal("33 patterns accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "pass_through_names") {
		t.Fatalf("error %q does not name pass_through_names", err)
	}
}

func TestDecodeConfig_RejectsInvalidValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		raw   string
		field string
	}{
		{
			name:  "budget explicitly zero",
			raw:   "max_corrections_per_turn: 0\n",
			field: "max_corrections_per_turn",
		},
		{
			name:  "budget above maximum",
			raw:   "max_corrections_per_turn: 4\n",
			field: "max_corrections_per_turn",
		},
		{
			name:  "budget beyond byte range",
			raw:   "max_corrections_per_turn: 300\n",
			field: "max_corrections_per_turn",
		},
		{
			name:  "malformed pattern",
			raw:   "pass_through_names: [\"mcp__*\", \"a[b\"]\n",
			field: "pass_through_names",
		},
		{
			name:  "unknown key",
			raw:   "max_corrections: 2\n",
			field: "max_corrections",
		},
		{
			name:  "row enabled flag inside config",
			raw:   "enabled: true\n",
			field: "enabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeConfig(t, tc.raw)
			if err == nil {
				t.Fatalf("DecodeConfig accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error %q does not name %s", err, tc.field)
			}
		})
	}
}

// passThroughNamesYAML builds a config subtree with n valid pass-through patterns.
func passThroughNamesYAML(n int) string {
	names := make([]string, 0, n)
	for i := range n {
		names = append(names, fmt.Sprintf("%q", "tool_"+strconv.Itoa(i)+"*"))
	}
	return "pass_through_names: [" + strings.Join(names, ", ") + "]\n"
}

// decodeConfig hands one feature-private YAML subtree to DecodeConfig the way
// the standard-distribution factory does.
func decodeConfig(t *testing.T, raw string) (toolnamecorrection.Config, error) {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml.Unmarshal(%q): %v", raw, err)
	}
	return toolnamecorrection.DecodeConfig(n)
}
