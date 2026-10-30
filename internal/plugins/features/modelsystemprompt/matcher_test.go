package modelsystemprompt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

func TestMatcher_OrderedExactImmutableRequests(t *testing.T) {
	text := " \n你好 ${model}\n\t"
	cfg := Config{Rules: []Rule{{ID: "first", ModelPattern: "^logical-", Append: text}, {ID: "second", ModelPattern: "special$", Append: "second"}}}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rules[0] = Rule{ID: "changed", ModelPattern: ".*", Append: "changed"}
	first := steering.PutRequest{OverlayID: "model-system-prompt.first", Message: steering.Message{Role: aipapi.RoleSystem, Text: text}, Placement: steering.StablePrefix, AnchorMissingPolicy: steering.StablePrefixFallback, Reason: "model_system_prompt"}
	second := first
	second.OverlayID, second.Message.Text = "model-system-prompt.second", "second"
	for _, tc := range []struct {
		model string
		want  []steering.PutRequest
	}{
		{"none", nil}, {"logical-normal", []steering.PutRequest{first}}, {"logical-special", []steering.PutRequest{first, second}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			got := m.Match(tc.model)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
			for _, req := range got {
				if err := req.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			if len(got) > 0 {
				got[0].Message.Text = "changed result"
			}
			if !reflect.DeepEqual(m.Match(tc.model), tc.want) {
				t.Fatal("result mutation changed matcher")
			}
		})
	}
	for _, cfg := range []Config{{}, {Rules: []Rule{}}} {
		m, err := New(cfg)
		if err != nil || len(m.Match("logical-special")) != 0 {
			t.Fatalf("empty rules: %v", err)
		}
	}
}

func TestNew_RejectsInvalidRulesAndAcceptsLimits(t *testing.T) {
	valid := Rule{ID: "rule", ModelPattern: ".*", Append: "valid"}
	rules := func(n, size int) []Rule {
		out := make([]Rule, n)
		for i := range out {
			out[i] = Rule{ID: fmt.Sprintf("r%d", i), ModelPattern: ".*", Append: strings.Repeat("x", size)}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		rule Rule
	}{
		{"missing id", Rule{ModelPattern: ".*", Append: "x"}},
		{"non ASCII id", Rule{ID: "é", ModelPattern: ".*", Append: "x"}},
		{"invalid id", Rule{ID: "x/y", ModelPattern: ".*", Append: "x"}},
		{"derived id too long", Rule{ID: strings.Repeat("x", 109), ModelPattern: ".*", Append: "x"}},
		{"missing pattern", Rule{ID: "x", Append: "x"}},
		{"invalid pattern", Rule{ID: "x", ModelPattern: "[", Append: "x"}},
		{"empty text", Rule{ID: "x", ModelPattern: ".*"}},
		{"whitespace text", Rule{ID: "x", ModelPattern: ".*", Append: " \t\n\u2003"}},
		{"invalid UTF8 text", Rule{ID: "x", ModelPattern: ".*", Append: string([]byte{0xff})}},
		{"NUL text", Rule{ID: "x", ModelPattern: ".*", Append: "secret\x00text"}},
		{"oversized text", Rule{ID: "x", ModelPattern: ".*", Append: strings.Repeat("x", 65537)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(Config{Rules: []Rule{tc.rule}}); err == nil {
				t.Fatal("accepted invalid rule")
			} else if strings.Contains(err.Error(), tc.rule.Append) && len(tc.rule.Append) > 3 {
				t.Fatal("error disclosed append text")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		rules []Rule
		valid bool
	}{
		{"duplicate", []Rule{valid, valid}, false},
		{"64 rules", rules(64, 1), true},
		{"65 rules", rules(65, 1), false},
		{"256KiB total", rules(4, 65536), true},
		{"over total", append(rules(4, 65536), Rule{ID: "extra", ModelPattern: ".*", Append: "x"}), false},
		{"derived ID limit", []Rule{{ID: strings.Repeat("x", 108), ModelPattern: ".*", Append: "x"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Rules: tc.rules})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}
