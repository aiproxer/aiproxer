package request_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type ordTransform struct {
	id  string
	ord int
}

func (o ordTransform) ID() string                        { return o.id }
func (o ordTransform) Order() int                        { return o.ord }
func (o ordTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (o ordTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

func TestMaterializeSorted_orderThenIDThenRegistration(t *testing.T) {
	t.Parallel()
	a := ordTransform{id: "b", ord: 1}
	b := ordTransform{id: "a", ord: 1}
	c := ordTransform{id: "a", ord: 2}
	in := []request.Transform{a, b, c}
	got := request.MaterializeSorted(in)
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	first, ok := got[0].(ordTransform)
	if !ok {
		t.Fatalf("want ordTransform at [0], got %T", got[0])
	}
	if first.id != "a" || first.ord != 1 {
		t.Fatalf("want first a ord1 got %#v", first)
	}
	second, ok2 := got[1].(ordTransform)
	if !ok2 {
		t.Fatalf("want ordTransform at [1], got %T", got[1])
	}
	if second.id != "b" {
		t.Fatalf("want second b got %#v", second)
	}
	third, ok3 := got[2].(ordTransform)
	if !ok3 {
		t.Fatalf("want ordTransform at [2], got %T", got[2])
	}
	if third.ord != 2 {
		t.Fatalf("want third ord2 got %#v", third)
	}
}
