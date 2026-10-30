package request_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

func TestMaterializeSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if request.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if request.MaterializeSorted([]request.Transform{}) != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestMaterializeSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := ordTransform{id: "same", ord: 0}
	first := ordTransform{id: "same", ord: 0}
	got := request.MaterializeSorted([]request.Transform{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok := got[0].(ordTransform)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if g0 != second {
		t.Fatalf("expected first registered transform first when Order+ID match")
	}
}

func TestMaterializeSorted_singleReturnsSingleton(t *testing.T) {
	t.Parallel()
	p := ordTransform{id: "one", ord: 3}
	got := request.MaterializeSorted([]request.Transform{p})
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	one, ok := got[0].(ordTransform)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if one.id != "one" {
		t.Fatalf("got %#v", got)
	}
}
