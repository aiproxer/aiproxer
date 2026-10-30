package hooks_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

type ordSubmit struct {
	id  string
	ord int
}

func (o ordSubmit) ID() string                   { return o.id }
func (o ordSubmit) Order() int                   { return o.ord }
func (o ordSubmit) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (ordSubmit) Handle(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	return sdk.SubmitDecision{}, nil
}

func TestMaterializeSorted_submitOrderMatchesStableRule(t *testing.T) {
	t.Parallel()
	cfg := hooks.Config{
		SubmitHooks: []sdk.SubmitHook{
			ordSubmit{id: "b", ord: 1},
			ordSubmit{id: "a", ord: 1},
			ordSubmit{id: "c", ord: 0},
		},
	}
	sorted := hooks.MaterializeSorted(cfg)
	if len(sorted.SubmitHooks) != 3 {
		t.Fatalf("len %d", len(sorted.SubmitHooks))
	}
	h0, ok := sorted.SubmitHooks[0].(ordSubmit)
	if !ok {
		t.Fatalf("want ordSubmit at [0], got %T", sorted.SubmitHooks[0])
	}
	if h0.id != "c" {
		t.Fatalf("first want c (order 0), got %q", h0.id)
	}
	h1, ok1 := sorted.SubmitHooks[1].(ordSubmit)
	if !ok1 {
		t.Fatalf("want ordSubmit at [1], got %T", sorted.SubmitHooks[1])
	}
	if h1.id != "a" {
		t.Fatalf("second want a (order 1, id a), got %q", h1.id)
	}
	h2, ok2 := sorted.SubmitHooks[2].(ordSubmit)
	if !ok2 {
		t.Fatalf("want ordSubmit at [2], got %T", sorted.SubmitHooks[2])
	}
	if h2.id != "b" {
		t.Fatalf("third want b, got %q", h2.id)
	}
}
