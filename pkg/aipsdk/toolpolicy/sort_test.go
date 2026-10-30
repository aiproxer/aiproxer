package toolpolicy_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)

type ordPolicy struct {
	id    string
	order int
}

func (o ordPolicy) ID() string                     { return o.id }
func (o ordPolicy) Order() int                     { return o.order }
func (o ordPolicy) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (o ordPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

func TestMaterializeSorted_singleElement(t *testing.T) {
	t.Parallel()
	a := ordPolicy{id: "only", order: 1}
	got := toolpolicy.MaterializeSorted([]toolpolicy.Policy{a})
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0] != a {
		t.Fatalf("got %#v want %#v", got[0], a)
	}
}

func TestMaterializeSorted_orderThenIDThenRegistration(t *testing.T) {
	t.Parallel()
	a := ordPolicy{id: "b", order: 1}
	b := ordPolicy{id: "a", order: 1}
	c := ordPolicy{id: "a", order: 2}
	got := toolpolicy.MaterializeSorted([]toolpolicy.Policy{c, a, b})
	want := []toolpolicy.Policy{b, a, c}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %#v want %#v", i, got[i], want[i])
		}
	}
}

func TestMaterializeSorted_edgeCases(t *testing.T) {
	t.Parallel()
	p0 := ordPolicy{id: "a", order: 0}
	p1 := ordPolicy{id: "b", order: 1}
	tests := []struct {
		name  string
		input []toolpolicy.Policy
		want  []toolpolicy.Policy
	}{
		{
			name:  "nil_slice_returns_nil",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty_slice_returns_nil",
			input: []toolpolicy.Policy{},
			want:  nil,
		},
		{
			name:  "all_nil_stable_by_registration_index",
			input: []toolpolicy.Policy{nil, nil},
			want:  []toolpolicy.Policy{nil, nil},
		},
		{
			name:  "non_nil_before_nil_same_order",
			input: []toolpolicy.Policy{p0, nil},
			want:  []toolpolicy.Policy{p0, nil},
		},
		{
			name:  "nil_first_input_non_nil_sorts_before_nil_by_order",
			input: []toolpolicy.Policy{nil, p1, p0},
			want:  []toolpolicy.Policy{p0, p1, nil},
		},
		{
			name:  "multiple_nil_after_sorted_non_nil",
			input: []toolpolicy.Policy{nil, p0, nil, p1},
			want:  []toolpolicy.Policy{p0, p1, nil, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := toolpolicy.MaterializeSorted(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("len got %d want %d got %#v want %#v", len(got), len(tt.want), got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("idx %d got %#v want %#v full got %#v want %#v", i, got[i], tt.want[i], got, tt.want)
				}
			}
			if len(tt.want) == 0 && got != nil {
				t.Fatalf("empty result must be nil slice, got %#v", got)
			}
			if tt.name == "nil_slice_returns_nil" && got != nil {
				t.Fatalf("nil input must yield nil, got %#v", got)
			}
			if tt.name == "empty_slice_returns_nil" && got != nil {
				t.Fatalf("empty input must yield nil, got %#v", got)
			}
		})
	}
}

func TestMaterializeSorted_non_nil_sorted_before_nil_tie_break_by_registration_index(t *testing.T) {
	t.Parallel()
	x := ordPolicy{id: "tie", order: 0}
	y := ordPolicy{id: "tie", order: 0}
	got := toolpolicy.MaterializeSorted([]toolpolicy.Policy{nil, y, nil, x})
	// y registered before x; same order and id -> lower index first among non-nil; nils last in index order.
	want := []toolpolicy.Policy{y, x, nil, nil}
	if len(got) != len(want) {
		t.Fatalf("len got %d want %d got %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d got %#v want %#v full %#v", i, got[i], want[i], got)
		}
	}
}
