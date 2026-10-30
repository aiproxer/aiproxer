package openairesponses_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestMount_RouteConflictPanicsOnDuplicatePattern(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	opts := aipsdk.FrontendMountOptions{}
	if err := openairesponses.Mount(mux, opts); err != nil {
		t.Fatal(err)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected ServeMux route conflict panic on second mount")
		}
		msg := ""
		switch v := r.(type) {
		case string:
			msg = v
		case error:
			msg = v.Error()
		default:
			msg = "recovered"
		}
		if msg != "recovered" && !strings.Contains(msg, "conflicts") {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	_ = openairesponses.Mount(mux, opts)
}
