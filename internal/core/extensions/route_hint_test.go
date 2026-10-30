package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
)

type rhFailOpen struct{}

func (rhFailOpen) ID() string                        { return "fail-open-hint" }
func (rhFailOpen) Order() int                        { return 0 }
func (rhFailOpen) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rhFailOpen) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, errors.New("boom")
}

type rhPrefer struct{}

func (rhPrefer) ID() string                        { return "prefer" }
func (rhPrefer) Order() int                        { return 1 }
func (rhPrefer) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rhPrefer) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{PreferredCandidateKeys: []string{"x:m"}}, nil
}

func TestRunRouteHintStage_failOpenSkipsError(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "a:m"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	in := routehint.Input{TraceID: "t1", Call: call}
	got, err := extensions.RunRouteHintStage(context.Background(), nil, []routehint.Provider{
		rhFailOpen{},
		rhPrefer{},
	}, call, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "x:m" {
		t.Fatalf("got %#v", got)
	}
}

func TestRunRouteHintStage_suppressionSkipsProvider(t *testing.T) {
	t.Parallel()
	ctx := execctx.WithSuppressedPluginIDs(context.Background(), []string{"prefer"})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "a:m"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	in := routehint.Input{TraceID: "t1", Call: call}
	got, err := extensions.RunRouteHintStage(ctx, nil, []routehint.Provider{rhPrefer{}}, call, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want no prefs got %#v", got)
	}
}
