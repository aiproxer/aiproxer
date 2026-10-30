package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: b-leg-path-virtualization Task 7.1. Requirements 4.5, 4.6, 8.4 and
// design.md "6. Mandatory Buffering / Completeness Contract": "finalizers
// without this interface retain existing behavior" and "existing
// PlaneToolCallFinalizationMaxArgsBytes retains legacy/default behavior for
// ordinary finalizers".
//
// Task 7.1 adds only the SDK capability contract; it must not change what the
// assembler does. This file pins the observable behavior a finalizer that does
// not implement toolcall.BufferingRequirement keeps, so the optional capability
// is provably inert at the assembler chokepoint and Task 7.2 can extend only the
// branch where a requirement is declared.
//
// The complementary characterization for a finalizer that DOES declare a
// requirement is deliberately RED and owned by Task 7.2; this file must stay
// green across both tasks.

const legacyOptOutToolName = "legacy_opt_out_tool"

// legacyOptOutArgs is an opaque argument document with no declared requirement
// attached to it: only its size matters here.
func legacyOptOutArgs(minBytes int) string {
	const (
		prefix = `{"value":"`
		suffix = `"}`
	)
	pad := max(minBytes-len(prefix)-len(suffix), 0)
	return prefix + strings.Repeat("y", pad) + suffix
}

// legacyOptOutFin implements exactly the pre-existing Finalizer method set and
// nothing else, so it cannot carry a buffering declaration of any kind.
type legacyOptOutFin struct {
	calls int
	seen  []byte
}

func (*legacyOptOutFin) ID() string { return "legacy-opt-out" }

func (*legacyOptOutFin) Order() int { return 0 }

func (f *legacyOptOutFin) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	_ aipapi.ToolDef,
	_ []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	f.calls++
	f.seen = append(f.seen[:0], call.ArgsJSON...)
	return toolcall.Result{
		Action:     toolcall.ActionRewrite,
		ToolName:   call.ToolName,
		ArgsJSON:   []byte(`{"value":"rewritten"}`),
		ReasonCode: toolcall.ReasonValidPassThrough,
	}, nil
}

// legacyOptOutStreamCall drives one completed tool call through the assembler as
// started / args-delta* / finished and returns the argument bytes the client
// observes, in release order, covering both fragments the assembler held and
// fragments replayed from the drain queue.
func legacyOptOutStreamCall(t *testing.T, a *toolCallAssembler, id, argsJSON string) (string, error) {
	t.Helper()
	ctx := context.Background()
	meta := toolcall.Meta{}
	var released strings.Builder

	started := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: legacyOptOutToolName}
	if held, err := a.ingest(ctx, started, meta, nil); err != nil || !held {
		t.Fatalf("started: held=%v err=%v", held, err)
	}
	for _, fragment := range []string{argsJSON[:len(argsJSON)/2], argsJSON[len(argsJSON)/2:]} {
		delta := aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			ToolName:   legacyOptOutToolName,
			Delta:      fragment,
		}
		held, err := a.ingest(ctx, delta, meta, nil)
		if err != nil {
			t.Fatalf("args delta: %v", err)
		}
		if !held {
			released.WriteString(fragment)
		}
	}
	_, err := a.ingest(ctx, aipapi.Event{
		Kind:       aipapi.EventToolCallFinished,
		ToolCallID: id,
		ToolName:   legacyOptOutToolName,
	}, meta, nil)
	for {
		ev, ok := a.popDrain()
		if !ok {
			return released.String(), err
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			released.WriteString(ev.Delta)
		}
	}
}

// TestToolCallAssembler_FinalizerWithoutBufferingDeclarationIsUnchanged proves
// the optional capability is inert for a finalizer that does not declare one:
// the shared assembly cap still decides, the finalizer still runs exactly once
// below it, and above it the call is still released as the original fragments
// without the finalizer running.
func TestToolCallAssembler_FinalizerWithoutBufferingDeclarationIsUnchanged(t *testing.T) {
	t.Parallel()

	catalog := []aipapi.ToolDef{{Name: legacyOptOutToolName, Parameters: []byte(`{"type":"object"}`)}}

	t.Run("finalizer_without_declaration_is_not_a_buffering_requirement", func(t *testing.T) {
		t.Parallel()
		var fin toolcall.Finalizer = &legacyOptOutFin{}
		if _, ok := fin.(toolcall.BufferingRequirement); ok {
			t.Fatal("a finalizer that does not implement the optional capability must not satisfy its type assertion")
		}
	})

	t.Run("legacy_shared_cap_still_applies", func(t *testing.T) {
		t.Parallel()
		a := newToolCallAssembler([]toolcall.Finalizer{&legacyOptOutFin{}}, 0, catalog)
		if a == nil {
			t.Fatal("assembler must be constructed for a non-empty finalizer list and catalog")
		}
		if a.maxArgsBytes != defaultToolCallFinalizationMaxArgsBytes {
			t.Fatalf("shared cap = %d, want the pre-existing default %d", a.maxArgsBytes, defaultToolCallFinalizationMaxArgsBytes)
		}
	})

	t.Run("under_shared_cap_finalizer_runs_with_complete_args", func(t *testing.T) {
		t.Parallel()
		fin := &legacyOptOutFin{}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		args := legacyOptOutArgs(8 * 1024)
		if len(args) > a.maxArgsBytes {
			t.Fatalf("fixture must stay under the shared cap: %d > %d", len(args), a.maxArgsBytes)
		}
		released, err := legacyOptOutStreamCall(t, a, "legacy-under", args)
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
		if fin.calls != 1 {
			t.Fatalf("finalizer invocations = %d, want 1", fin.calls)
		}
		if string(fin.seen) != args {
			t.Fatalf("finalizer saw %d argument bytes, want the complete %d-byte document", len(fin.seen), len(args))
		}
		if released != `{"value":"rewritten"}` {
			t.Fatalf("released argument bytes = %q, want the rewritten document", released)
		}
	})

	t.Run("above_shared_cap_call_is_released_unchanged_without_running_finalizer", func(t *testing.T) {
		t.Parallel()
		fin := &legacyOptOutFin{}
		a := newToolCallAssembler([]toolcall.Finalizer{fin}, 0, catalog)
		args := legacyOptOutArgs(a.maxArgsBytes + 4096)

		released, err := legacyOptOutStreamCall(t, a, "legacy-over", args)
		if err != nil {
			t.Fatalf("finished: %v", err)
		}
		if fin.calls != 0 {
			t.Fatalf("finalizer invocations = %d, want 0: a finalizer without a declared requirement must not run past the shared cap", fin.calls)
		}
		if released != args {
			t.Fatalf("released %d argument bytes, want the %d original bytes replayed unchanged", len(released), len(args))
		}
	})
}
