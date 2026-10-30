package prerequest_test

import (
	"reflect"
	"testing"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestMaterializeSorted(t *testing.T) {
	t.Parallel()
	in := []prerequest.Handler{
		sortHandler{id: "b", order: 10},
		sortHandler{id: "a", order: 10},
		sortHandler{id: "z", order: 1},
		sortHandler{id: "a", order: 10},
	}
	got := prerequest.MaterializeSorted(in)
	ids := make([]string, 0, len(got))
	for _, h := range got {
		ids = append(ids, h.ID())
	}
	want := []string{"z", "a", "a", "b"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %#v want %#v", ids, want)
	}
}

type sortHandler struct {
	id    string
	order int
	handlerFunc
}

func (h sortHandler) ID() string                        { return h.id }
func (h sortHandler) Order() int                        { return h.order }
func (h sortHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
