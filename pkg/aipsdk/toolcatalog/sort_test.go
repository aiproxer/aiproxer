package toolcatalog_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type ordFilter struct {
	id  string
	ord int
}

func (o ordFilter) ID() string                        { return o.id }
func (o ordFilter) Order() int                        { return o.ord }
func (o ordFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (o ordFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

func TestMaterializeSorted_filtersStableOrder(t *testing.T) {
	t.Parallel()
	in := []toolcatalog.Filter{
		ordFilter{id: "b", ord: 1},
		ordFilter{id: "a", ord: 1},
		ordFilter{id: "a", ord: 2},
	}
	got := toolcatalog.MaterializeSorted(in)
	first, ok := got[0].(ordFilter)
	if !ok {
		t.Fatalf("want ordFilter at [0], got %T", got[0])
	}
	if first.id != "a" || first.ord != 1 {
		t.Fatalf("first: %#v", first)
	}
}
