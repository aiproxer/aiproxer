package refverifier_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refverifier"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

type collectOK struct{ calls int }

func (a *collectOK) Collect(_ context.Context, r auxiliary.Request) (aipapi.Collected, error) {
	a.calls++
	if r.Role != "verifier" {
		return aipapi.Collected{}, fmt.Errorf("role")
	}
	return aipapi.Collected{}, nil
}

func (a *collectOK) Stream(_ context.Context, _ auxiliary.Request) (aipapi.EventStream, error) {
	return nil, fmt.Errorf("unused")
}

func TestCompletionGate_replaceWhenAuxWired(t *testing.T) {
	t.Parallel()
	g := refverifier.NewCompletionGate(refverifier.Config{SteerText: "pivot"})
	aux := &collectOK{}
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "upstream"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{g},
		completion.Meta{TraceID: "tr1"}, orig, false, completion.Services{
			State: state.DisabledStore{},
			Aux:   aux,
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if aux.calls != 1 {
		t.Fatalf("aux calls %d", aux.calls)
	}
	if len(result.Events) < 1 || result.Events[len(result.Events)-2].Delta != "pivot" {
		t.Fatalf("out %#v", result.Events)
	}
}

func TestCompletionGate_passesWhenAuxDisabled(t *testing.T) {
	t.Parallel()
	g := refverifier.NewCompletionGate(refverifier.Config{})
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "u"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{g},
		completion.Meta{TraceID: "tr1"}, orig, false, completion.Services{
			State: state.DisabledStore{},
			Aux:   auxiliary.DisabledClient{},
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].Delta != "u" {
		t.Fatalf("out %#v", result.Events)
	}
}
