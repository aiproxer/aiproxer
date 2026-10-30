package routehint_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

func TestMaterializeSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if routehint.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if routehint.MaterializeSorted([]routehint.Provider{}) != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestMaterializeSorted_singleReturnsSingleton(t *testing.T) {
	t.Parallel()
	p := stubProv{id: "only", order: 0}
	got := routehint.MaterializeSorted([]routehint.Provider{p})
	if len(got) != 1 {
		t.Fatalf("len=%d", len(got))
	}
	only, ok := got[0].(stubProv)
	if !ok {
		t.Fatalf("unexpected type %T", got[0])
	}
	if only.id != "only" {
		t.Fatalf("got %#v", got[0])
	}
}

func TestMaterializeSorted_nilProviderSortsFirst(t *testing.T) {
	t.Parallel()
	a := stubProv{id: "a", order: 0}
	got := routehint.MaterializeSorted([]routehint.Provider{&a, nil})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0] != nil {
		t.Fatalf("expected nil first, got %v", got[0])
	}
}
