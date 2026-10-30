package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type terminalDecisionAuxiliaryProjectionClient struct{ id string }

func (terminalDecisionAuxiliaryProjectionClient) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (terminalDecisionAuxiliaryProjectionClient) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

func TestTerminalDecisionInput_CapturesImmutableAuxiliaryClientAndCopiesToChild(t *testing.T) {
	t.Parallel()

	parentAux := terminalDecisionAuxiliaryProjectionClient{id: "parent"}
	replacementAux := terminalDecisionAuxiliaryProjectionClient{id: "replacement"}
	parentSnapshot := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{Aux: parentAux})
	replacementSnapshot := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{Aux: replacementAux})
	executor := &Executor{ExtensionRuntime: ExtensionRuntime{RuntimeSnapshot: parentSnapshot}}
	parent := newTurnTerminal()
	bindTurnTerminalRuntime(parent, executor)

	request := requestTerminalFacts{call: aipapi.Call{ID: "request"}, traceID: "trace", aLegID: "a-leg"}
	input := parent.terminalDecisionInput(terminal.CommandNormalFinish, request, nil, nil, coreterm.NewAccumulatorSnapshot(nil, false))
	if input.Auxiliary != parentAux {
		t.Fatalf("input auxiliary=%T/%v, want parent snapshot client", input.Auxiliary, input.Auxiliary)
	}

	executor.RuntimeSnapshot = replacementSnapshot
	inputAfterReload := parent.terminalDecisionInput(terminal.CommandNormalFinish, request, nil, nil, coreterm.NewAccumulatorSnapshot(nil, false))
	if inputAfterReload.Auxiliary != parentAux {
		t.Fatalf("input auxiliary changed after snapshot replacement: %T/%v", inputAfterReload.Auxiliary, inputAfterReload.Auxiliary)
	}

	child := newTurnTerminalWithSharedALeg(parent)
	bindTurnTerminalRuntime(child, executor)
	childInput := child.terminalDecisionInput(terminal.CommandNormalFinish, request, nil, nil, coreterm.NewAccumulatorSnapshot(nil, false))
	if childInput.Auxiliary != parentAux {
		t.Fatalf("child input auxiliary=%T/%v, want parent snapshot client", childInput.Auxiliary, childInput.Auxiliary)
	}
}
