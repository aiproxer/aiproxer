package extensions_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestRunPreRequestStage_denialStopsChain(t *testing.T) {
	t.Parallel()
	call := validCall()
	var seen []string
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "b", order: 20, seen: &seen},
		preReqHandler{id: "deny", order: 30, seen: &seen, decision: prerequest.Deny("blocked")},
		preReqHandler{id: "after", order: 40, seen: &seen},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if !prerequest.IsRejected(err) {
		t.Fatalf("expected prerequest rejection, got %v", err)
	}
	if !reflect.DeepEqual(seen, []string{"b", "deny"}) {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestRunPreRequestStage_sortsAndMergesAnnotations(t *testing.T) {
	t.Parallel()
	call := validCall()
	var seen []string
	meta := prerequest.Meta{Annotations: map[string]string{"in": "keep"}}
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "b", order: 10, seen: &seen},
		preReqHandler{id: "a", order: 10, seen: &seen, decision: prerequest.Decision{Annotations: map[string]string{"out": "set"}}},
		preReqHandler{id: "z", order: 1, seen: &seen},
	}, &call, meta, prerequest.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"z", "a", "b"}) {
		t.Fatalf("seen = %#v", seen)
	}
	if meta.Annotations["in"] != "keep" || meta.Annotations["out"] != "set" {
		t.Fatalf("annotations = %#v", meta.Annotations)
	}
}

func TestRunPreRequestStage_failOpenContinues(t *testing.T) {
	t.Parallel()
	call := validCall()
	var seen []string
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "bad", order: 1, seen: &seen, err: errors.New("boom"), mode: sdkhooks.FailOpen},
		preReqHandler{id: "next", order: 2, seen: &seen},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"bad", "next"}) {
		t.Fatalf("seen = %#v", seen)
	}
}

func TestRunPreRequestStage_failClosedStops(t *testing.T) {
	t.Parallel()
	call := validCall()
	cause := errors.New("boom")
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "bad", order: 1, err: cause, mode: sdkhooks.FailClosed},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !aipapi.IsPolicyFailure(err) {
		t.Fatalf("fail-closed must surface a stable policy failure, got %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("fail-closed must preserve cause, got %v", err)
	}
}

func TestRunPreRequestStage_skipsAuxiliaryDepth(t *testing.T) {
	t.Parallel()
	call := validCall()
	ctx := execctx.WithAuxiliaryDepth(context.Background(), 1)
	var seen []string
	err := extensions.RunPreRequestStage(ctx, nil, nil, []prerequest.Handler{
		preReqHandler{id: "skip", order: 1, seen: &seen, decision: prerequest.Deny("blocked")},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("auxiliary path should skip pre-request handlers, saw %#v", seen)
	}
}

func TestRunPreRequestStage_validatesAfterChain(t *testing.T) {
	t.Parallel()
	call := validCall()
	err := extensions.RunPreRequestStage(context.Background(), nil, nil, []prerequest.Handler{
		preReqHandler{id: "mutate-bad", order: 1, mutate: func(c *aipapi.Call) { c.Messages = nil }},
	}, &call, prerequest.Meta{}, prerequest.Services{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !aipapi.IsPolicyMalformed(err) {
		t.Fatalf("post-chain validation failure must surface a stable policy malformed error, got %v", err)
	}
}

type preReqHandler struct {
	id       string
	order    int
	mode     sdkhooks.FailureMode
	seen     *[]string
	decision prerequest.Decision
	err      error
	mutate   func(*aipapi.Call)
}

func (h preReqHandler) ID() string { return h.id }
func (h preReqHandler) Order() int { return h.order }
func (h preReqHandler) FailureMode() sdkhooks.FailureMode {
	if h.mode == sdkhooks.FailureModeUnspecified {
		return sdkhooks.FailClosed
	}
	return h.mode
}

func (h preReqHandler) Handle(_ context.Context, call *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	if h.seen != nil {
		*h.seen = append(*h.seen, h.id)
	}
	if h.mutate != nil {
		h.mutate(call)
	}
	return h.decision, h.err
}

func validCall() aipapi.Call {
	return aipapi.Call{
		ID: "call",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}
