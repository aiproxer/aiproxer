package extensions_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

type auxCollectOK struct {
	calls int
}

func (a *auxCollectOK) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	a.calls++
	return aipapi.Collected{}, nil
}

func (a *auxCollectOK) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, fmt.Errorf("unused")
}

type gateUsesAux struct{}

func (gateUsesAux) ID() string                        { return "aux-gate" }
func (gateUsesAux) Order() int                        { return 0 }
func (gateUsesAux) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateUsesAux) Handle(ctx context.Context, _ completion.Meta, _ completion.Buffered, svc completion.Services) (completion.Outcome, error) {
	_, err := svc.Aux.Collect(ctx, auxiliary.Request{
		Role:          "verifier",
		ParentTraceID: "trace-1",
		Call:          &aipapi.Call{},
	})
	if err != nil {
		return completion.PassOriginalOutcome(), nil
	}
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "aux-steered"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func TestApplyCompletionGateChain_auxInfluencedReplace(t *testing.T) {
	t.Parallel()
	aux := &auxCollectOK{}
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "upstream"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateUsesAux{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   aux,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if aux.calls != 1 {
		t.Fatalf("aux calls=%d", aux.calls)
	}
	if len(result.Events) != 4 || result.Events[2].Delta != "aux-steered" {
		t.Fatalf("got %#v", result.Events)
	}
	if !result.Replaced {
		t.Fatal("expected Replaced=true")
	}
}

func TestApplyCompletionGateChain_auxDisabledPassThrough(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "upstream"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateUsesAux{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].Delta != "upstream" {
		t.Fatalf("got %#v", result.Events)
	}
}
