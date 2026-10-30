package routing_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestBackendIDsReferenced proves every literal backend ID embedded across
// failover, weighted, and parallel alternatives is enumerated so a compiler
// can validate a route selector against the candidate backend set (req 9.2).
func TestBackendIDsReferenced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "simple primary", text: "openai:gpt-4", want: []string{"openai"}},
		{name: "failover chain", text: "a:m1|b:m2", want: []string{"a", "b"}},
		{name: "weighted branches", text: "[weight=1]a:m1^[weight=2]b:m2", want: []string{"a", "b"}},
		{name: "parallel branches", text: "a:m1!b:m2", want: []string{"a", "b"}},
		{name: "model-only has no backend", text: "gpt-4", want: nil},
		{name: "empty selector", text: "", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var sel *routing.Selector
			if tt.text != "" {
				parsed, err := routing.Parse(tt.text)
				if err != nil {
					t.Fatalf("parse %q: %v", tt.text, err)
				}
				sel = parsed
			}
			got := routing.BackendIDsReferenced(sel)
			if len(got) != len(tt.want) {
				t.Fatalf("BackendIDsReferenced(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("BackendIDsReferenced(%q)[%d] = %q, want %q", tt.text, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBackendIDsReferenced_NilSelector(t *testing.T) {
	t.Parallel()
	if got := routing.BackendIDsReferenced(nil); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
