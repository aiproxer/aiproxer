package modelsystemprompt

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_StrictRulesOnly(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{"", true},
		{"null", true},
		{"{}", true},
		{"rules: []", true},
		{"rules:\n  - id: x\n    model_pattern: '.*'\n    append: ' text '", true},
		{"enabled: true", false},
		{"unknown: secret", false},
		{"[]", false},
		{"scalar", false},
		{"rules: {}", false},
		{"rules: null", false},
		{"rules: [null]", false},
		{"rules: [{id: x, model_pattern: '.*', append: text, extra: secret}]", false},
		{"rules: [{id: 1, model_pattern: '.*', append: text}]", false},
		{"rules: [{id: x, model_pattern: true, append: text}]", false},
		{"rules: [{id: x, model_pattern: '.*', append: null}]", false},
		{"rules: []\nrules: []", false},
		{"rules: [{id: x, id: y, model_pattern: '.*', append: text}]", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
				t.Fatal(err)
			}
			cfg, err := DecodeConfig(node)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			if err == nil && len(cfg.Rules) > 0 && cfg.Rules[0].Append != " text " {
				t.Fatalf("append changed: %q", cfg.Rules[0].Append)
			}
		})
	}
}
