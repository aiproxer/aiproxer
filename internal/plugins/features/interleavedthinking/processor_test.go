package interleavedthinking

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestProcessor_TurnLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := Config{
		Enabled:               true,
		Instructions:          "Custom Thinker Instructions",
		StreamToClient:        "visible",
		RegularTurnsRemaining: 2,
		MaxMemoBytes:          4096,
	}
	store := NewMemoStore(4096)
	proc, err := NewProcessor(cfg, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	turnInput := TurnInput{
		ALegID:    "aleg-1",
		Selector:  "openai:gpt-4o",
		Backend:   "openai",
		Model:     "gpt-4o",
		RequestID: "req-123",
	}

	turn, err := proc.BeginTurn(ctx, turnInput)
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	// 1. Shape Thinker
	c := baseCall()
	shapedThinker, err := turn.ShapeThinker(c)
	if err != nil {
		t.Fatalf("ShapeThinker: %v", err)
	}
	if len(shapedThinker.Tools) != 0 {
		t.Fatalf("expected thinker tools stripped, got %d", len(shapedThinker.Tools))
	}
	if shapedThinker.Instructions[0].Parts[0].Text != "Custom Thinker Instructions" {
		t.Fatalf("expected thinker instructions prepended, got %q", shapedThinker.Instructions[0].Parts[0].Text)
	}

	// 2. Observe Thinker events
	memoBody := "## Session Steering Memo\n- **Goal**: Build tests\n- **Current state**: done\n- **Constraints and risks**: none\n- **Considered next steps**:\n  1. step1\n- **Recommended next step**: step1\n- **Reason**: optimal"
	ev := aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: memoBody,
	}
	emitted, err := turn.ObserveThinkerEvent(ev)
	if err != nil {
		t.Fatalf("ObserveThinkerEvent: %v", err)
	}
	if len(emitted) == 0 {
		t.Fatalf("expected emitted events, got none")
	}

	// 3. Finalize Thinker
	memoRes, err := turn.FinalizeThinker(ctx)
	if err != nil {
		t.Fatalf("FinalizeThinker: %v", err)
	}
	if memoRes.Reference == "" {
		t.Fatalf("expected non-empty memo reference, got empty")
	}
	if memoRes.Text != memoBody {
		t.Fatalf("expected memo text %q, got %q", memoBody, memoRes.Text)
	}

	// Verify store
	ref := state.MemoRef{Key: memoRes.Reference, Version: memoRes.Version}
	stored, ok, err := store.Get(ctx, Scope("aleg-1"), ref)
	if err != nil || !ok {
		t.Fatalf("store.Get failed: ok=%v, err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 2 {
		t.Fatalf("expected 2 turns remaining, got %d", stored.RegularTurnsRemaining)
	}

	// 4. Shape Executor (first turn)
	execCall := baseCall()
	shapedExec1, err := turn.ShapeExecutor(ctx, execCall, memoRes)
	if err != nil {
		t.Fatalf("ShapeExecutor turn 1: %v", err)
	}
	if err := shapedExec1.Validate(); err != nil {
		t.Fatalf("shapedExec1 invalid: %v", err)
	}
	if _, err := turn.CommitExecutor(ctx); err != nil {
		t.Fatalf("CommitExecutor turn 1: %v", err)
	}

	// Check store budget decremented to 1
	stored, ok, err = store.Get(ctx, Scope("aleg-1"), ref)
	if err != nil || !ok {
		t.Fatalf("store.Get failed: ok=%v, err=%v", ok, err)
	}
	if stored.RegularTurnsRemaining != 1 {
		t.Fatalf("expected 1 turn remaining after turn 1, got %d", stored.RegularTurnsRemaining)
	}
	if stored.InjectedCount != 1 {
		t.Fatalf("expected InjectedCount 1 after turn 1, got %d", stored.InjectedCount)
	}

	// 5. Shape Executor (second turn)
	shapedExec2, err := turn.ShapeExecutor(ctx, execCall, memoRes)
	if err != nil {
		t.Fatalf("ShapeExecutor turn 2: %v", err)
	}
	if err := shapedExec2.Validate(); err != nil {
		t.Fatalf("shapedExec2 invalid: %v", err)
	}
	if _, err := turn.CommitExecutor(ctx); err != nil {
		t.Fatalf("CommitExecutor turn 2: %v", err)
	}
	stored, _, _ = store.Get(ctx, Scope("aleg-1"), ref)
	if stored.RegularTurnsRemaining != 0 {
		t.Fatalf("expected 0 turns remaining after turn 2, got %d", stored.RegularTurnsRemaining)
	}

	// 6. Shape Executor (third turn - expired)
	shapedExec3, err := turn.ShapeExecutor(ctx, execCall, memoRes)
	if err != nil {
		t.Fatalf("ShapeExecutor turn 3: %v", err)
	}
	if err := shapedExec3.Validate(); err != nil {
		t.Fatalf("shapedExec3 invalid: %v", err)
	}
	stored, _, _ = store.Get(ctx, Scope("aleg-1"), ref)
	if stored.InjectedCount != 2 {
		t.Fatalf("expected InjectedCount unchanged at 2 after expired attempt, got %d", stored.InjectedCount)
	}
}

func TestProcessor_ShapeExecutor_RespectsCanceledContext(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Enabled:               true,
		Instructions:          "instructions",
		StreamToClient:        "visible",
		RegularTurnsRemaining: 2,
		MaxMemoBytes:          4096,
	}
	store := NewMemoStore(4096)
	proc, err := NewProcessor(cfg, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	turn, err := proc.BeginTurn(context.Background(), TurnInput{
		ALegID: "aleg-cancel",
	})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	// Put a memo into store so ShapeExecutor reaches store I/O
	_, err = store.Put(context.Background(), Scope("aleg-cancel"), state.MemoState{
		Memo:                  "test memo",
		RegularTurnsRemaining: 2,
	})
	if err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = turn.ShapeExecutor(canceledCtx, baseCall(), MemoResult{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ShapeExecutor with canceled context must return context.Canceled, got %v", err)
	}
}

func TestProcessor_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx := canceledCtx()
	cfg := Config{Enabled: true}
	proc, err := NewProcessor(cfg, nil)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}

	_, err = proc.BeginTurn(ctx, TurnInput{ALegID: "a1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on BeginTurn, got %v", err)
	}

	turn, err := proc.BeginTurn(context.Background(), TurnInput{ALegID: "a1"})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	_, err = turn.FinalizeThinker(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on FinalizeThinker, got %v", err)
	}
}
