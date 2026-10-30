package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestDefaultToolCallFinalizationMaxArgsBytesMatchCore(t *testing.T) {
	t.Parallel()
	if defaultToolCallFinalizationMaxArgsBytes != repair.DefaultMaxArgsBytes {
		t.Fatalf("assembler default=%d != repair DefaultMaxArgsBytes=%d",
			defaultToolCallFinalizationMaxArgsBytes, repair.DefaultMaxArgsBytes)
	}
}

type mutFin struct{}

func (mutFin) ID() string { return "mut" }
func (mutFin) Order() int { return 0 }
func (mutFin) Finalize(_ context.Context, call toolcall.CompletedCall, _ aipapi.ToolDef, _ []aipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	if len(call.ArgsJSON) > 0 {
		call.ArgsJSON[0] = 'X'
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func TestToolCallAssembler_defensiveArgsCopy(t *testing.T) {
	t.Parallel()
	a := newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, 1024, []aipapi.ToolDef{{Name: "t"}})
	if a == nil {
		t.Fatal("assembler")
	}
	meta := toolcall.Meta{}
	held, err := a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	if err != nil || !held {
		t.Fatalf("started held=%v err=%v", held, err)
	}
	held, err = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"a":1}`}, meta, nil)
	if err != nil || !held {
		t.Fatalf("delta held=%v err=%v", held, err)
	}
	held, err = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	if err != nil || !held {
		t.Fatalf("finished held=%v err=%v", held, err)
	}
	var args strings.Builder
	for {
		ev, ok := a.popDrain()
		if !ok {
			break
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if args.String() != `{"a":1}` {
		t.Fatalf("exact replay corrupted: %q", args.String())
	}
}

func TestNewToolCallAssembler_noWorkWithoutToolsOrFinalizers(t *testing.T) {
	t.Parallel()
	if newToolCallAssembler(nil, 64, []aipapi.ToolDef{{Name: "t"}}) != nil {
		t.Fatal("nil finalizers")
	}
	if newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, 64, nil) != nil {
		t.Fatal("nil catalog")
	}
}

type catalogMutFin struct{}

func (catalogMutFin) ID() string { return "cat-mut" }
func (catalogMutFin) Order() int { return 0 }
func (catalogMutFin) Finalize(_ context.Context, _ toolcall.CompletedCall, tool aipapi.ToolDef, catalog []aipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	if len(tool.Parameters) > 0 {
		tool.Parameters[0] = 'X'
	}
	if len(catalog) > 0 && len(catalog[0].Parameters) > 0 {
		catalog[0].Parameters[0] = 'Y'
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func TestToolCallAssembler_catalogDeepClone(t *testing.T) {
	t.Parallel()
	params := []byte(`{"type":"object"}`)
	catalog := []aipapi.ToolDef{{Name: "t", Parameters: params}}
	a := newToolCallAssembler([]toolcall.Finalizer{catalogMutFin{}}, 1024, catalog)
	if a == nil {
		t.Fatal("assembler")
	}
	meta := toolcall.Meta{}
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`}, meta, nil)
	_, err := a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	if err != nil {
		t.Fatalf("finished: %v", err)
	}
	if string(a.catalog[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("assembler catalog mutated: %s", a.catalog[0].Parameters)
	}
	if string(params) != `{"type":"object"}` {
		t.Fatalf("caller catalog mutated: %s", params)
	}
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c2", ToolName: "t"}, meta, nil)
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c2", Delta: `{}`}, meta, nil)
	_, err = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c2", ToolName: "t"}, meta, nil)
	if err != nil {
		t.Fatalf("second finished: %v", err)
	}
	if string(a.catalog[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("second call saw mutated catalog: %s", a.catalog[0].Parameters)
	}
}

func TestToolCallAssembler_popDrainReleasesBacking(t *testing.T) {
	t.Parallel()
	a := newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, 1024, []aipapi.ToolDef{{Name: "t"}})
	if a == nil {
		t.Fatal("assembler")
	}
	meta := toolcall.Meta{}
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	big := strings.Repeat("x", 512)
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{"a":"` + big + `"}`}, meta, nil)
	_, err := a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	if err != nil {
		t.Fatalf("finished: %v", err)
	}
	n := 0
	for {
		_, ok := a.popDrain()
		if !ok {
			break
		}
		n++
	}
	if n == 0 {
		t.Fatal("expected drained events")
	}
	if a.drain != nil {
		t.Fatal("drain slice must be nil after full pop to release backing array")
	}
}

func TestToolCallAssembler_argsOverflowSafeWhenNearCap(t *testing.T) {
	t.Parallel()
	const maxCap = 16
	a := newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, maxCap, []aipapi.ToolDef{{Name: "t"}})
	if a == nil {
		t.Fatal("assembler")
	}
	meta := toolcall.Meta{}
	_, _ = a.ingest(context.Background(), aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: "t"}, meta, nil)
	held, err := a.ingest(context.Background(), aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "c1",
		Delta:      strings.Repeat("a", maxCap+1),
	}, meta, nil)
	if err != nil {
		t.Fatalf("delta: %v", err)
	}
	if !held {
		t.Fatal("oversized delta must be held for overflow replay")
	}
	// Overflow immediately replays originals onto drain, then pass-through for that ID.
	ev, ok := a.popDrain()
	if !ok || ev.Kind != aipapi.EventToolCallStarted {
		t.Fatalf("expected started replay, got ok=%v kind=%v", ok, ev.Kind)
	}
	ev, ok = a.popDrain()
	if !ok || ev.Kind != aipapi.EventToolCallArgsDelta {
		t.Fatalf("expected delta replay, got ok=%v kind=%v", ok, ev.Kind)
	}
	held, err = a.ingest(context.Background(), aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "c1",
		Delta:      "more",
	}, meta, nil)
	if err != nil {
		t.Fatalf("post-overflow delta: %v", err)
	}
	if held {
		t.Fatal("post-overflow same ID must pass through (not held)")
	}
	if _, ok := a.popDrain(); ok {
		t.Fatal("pass-through must not enqueue onto drain")
	}
}

func TestClampToolCallFinalizationMaxArgsBytes(t *testing.T) {
	t.Parallel()
	if got := clampToolCallFinalizationMaxArgsBytes(0); got != defaultToolCallFinalizationMaxArgsBytes {
		t.Fatalf("zero -> default: got %d", got)
	}
	if got := clampToolCallFinalizationMaxArgsBytes(-1); got != defaultToolCallFinalizationMaxArgsBytes {
		t.Fatalf("negative -> default: got %d", got)
	}
	if got := clampToolCallFinalizationMaxArgsBytes(aipapi.MaxEventDeltaBytes + 1); got != aipapi.MaxEventDeltaBytes {
		t.Fatalf("over max clamped: got %d", got)
	}
	if got := clampToolCallFinalizationMaxArgsBytes(1024); got != 1024 {
		t.Fatalf("valid: got %d", got)
	}
}

func TestResolveToolCallFinalizers_MaxArgsPassthrough(t *testing.T) {
	t.Parallel()
	e := &Executor{}
	e.SetToolCallFinalizers(nil, 12345)
	_, maxArgs := e.resolveToolCallFinalizers()
	if maxArgs != 12345 {
		t.Fatalf("resolve after set: got %d want 12345", maxArgs)
	}
	// Clamp happens at assembler construction, not resolve time.
	a := newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, aipapi.MaxEventDeltaBytes+1, []aipapi.ToolDef{{Name: "t"}})
	if a == nil || a.maxArgsBytes != aipapi.MaxEventDeltaBytes {
		t.Fatalf("assembler clamp: %#v", a)
	}
}
