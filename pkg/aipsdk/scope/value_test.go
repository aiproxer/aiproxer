package scope_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestValue_UnknownByDefault(t *testing.T) {
	t.Parallel()
	var v scope.Value
	if v.IsKnown() {
		t.Fatal("zero Value must be unknown, not known-empty")
	}
	if !v.IsUnknown() {
		t.Fatal("zero Value must report unknown")
	}
	if v.String() != "" {
		t.Fatalf("unknown Value String must be empty, got %q", v.String())
	}
}

func TestValue_KnownPopulated(t *testing.T) {
	t.Parallel()
	v := scope.Known("alice")
	if !v.IsKnown() {
		t.Fatal("expected known")
	}
	if v.IsUnknown() {
		t.Fatal("expected not unknown")
	}
	if v.IsKnownEmpty() {
		t.Fatal("expected not known-empty")
	}
	if v.String() != "alice" {
		t.Fatalf("String: got %q want %q", v.String(), "alice")
	}
}

func TestValue_KnownEmptyDistinctFromUnknown(t *testing.T) {
	t.Parallel()
	v := scope.Known("")
	if !v.IsKnown() {
		t.Fatal("known-empty must report known")
	}
	if v.IsUnknown() {
		t.Fatal("known-empty must not report unknown")
	}
	if !v.IsKnownEmpty() {
		t.Fatal("expected known-empty")
	}
	if v.String() != "" {
		t.Fatalf("String: got %q want empty", v.String())
	}

	u := scope.Unknown()
	if u.IsKnown() || !u.IsUnknown() {
		t.Fatal("Unknown() must be unknown")
	}
	if u.IsKnownEmpty() {
		t.Fatal("unknown must not report known-empty")
	}
}

func TestValue_Equal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b scope.Value
		want bool
	}{
		{"both unknown", scope.Unknown(), scope.Value{}, true},
		{"unknown vs known-empty", scope.Unknown(), scope.Known(""), false},
		{"known-empty vs known-empty", scope.Known(""), scope.Known(""), true},
		{"same populated", scope.Known("a"), scope.Known("a"), true},
		{"different populated", scope.Known("a"), scope.Known("b"), false},
		{"populated vs unknown", scope.Known("a"), scope.Unknown(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.a.Equal(c.b) != c.want {
				t.Fatalf("Equal(%+v,%+v) want %v", c.a, c.b, c.want)
			}
		})
	}
}
