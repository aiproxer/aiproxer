package interleavedthinking

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
)

func newTestMemoState(body string) state.MemoState {
	return state.MemoState{
		Memo:                  body,
		SourceSelector:        "openai-responses:gpt-4o[thinker]",
		Backend:               "openai-responses",
		Model:                 "gpt-4o",
		RequestID:             "req-1",
		RegularTurnsRemaining: 2,
		ExtractionSource:      "block",
	}
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestMemoStore_PutGetRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	st := newTestMemoState("plan A")

	ref, err := store.Put(ctx, "session-1", st)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if ref.Key == "" || ref.Version == 0 {
		t.Fatalf("put returned invalid ref %+v", ref)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected memo to be found")
	}
	if got.Memo != "plan A" || got.SourceSelector != st.SourceSelector {
		t.Fatalf("get returned wrong state: %+v", got)
	}
	if got.InjectedCount != 0 {
		t.Fatalf("expected injected count 0, got %d", got.InjectedCount)
	}
}

func TestMemoStore_GetMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	if _, ok, err := store.Get(ctx, "session-1", state.MemoRef{Key: "nope"}); err != nil {
		t.Fatalf("get missing: %v", err)
	} else if ok {
		t.Fatal("expected not found for missing key")
	}
}

func TestMemoStore_Update(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newTestMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	updated := newTestMemoState("plan B")
	gotRef, err := store.Update(ctx, "session-1", ref, updated)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotRef.Key != ref.Key {
		t.Fatalf("update returned wrong key: %q want %q", gotRef.Key, ref.Key)
	}
	if gotRef.Version != ref.Version+1 {
		t.Fatalf("update must bump version: got %d want %d", gotRef.Version, ref.Version+1)
	}
	got, ok, err := store.Get(ctx, "session-1", ref)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok || got.Memo != "plan B" {
		t.Fatalf("update did not persist: %+v", got)
	}
}

func TestMemoStore_UpdateMissingFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	_, err := store.Update(ctx, "session-1", state.MemoRef{Key: "nope"}, newTestMemoState("b"))
	if !errors.Is(err, ErrMemoNotFound) {
		t.Fatalf("expected ErrMemoNotFound, got %v", err)
	}
}

func TestMemoStore_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newTestMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete(ctx, "session-1", ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := store.Get(ctx, "session-1", ref); err != nil {
		t.Fatalf("get after delete: %v", err)
	} else if ok {
		t.Fatal("expected memo to be deleted")
	}
}

func TestMemoStore_DeleteMissingIsNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	if err := store.Delete(ctx, "session-1", state.MemoRef{Key: "nope"}); err != nil {
		t.Fatalf("delete missing should succeed, got %v", err)
	}
}

func TestMemoStore_ScopeIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	ref, err := store.Put(ctx, "session-1", newTestMemoState("plan A"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok, err := store.Get(ctx, "session-2", ref); err != nil {
		t.Fatalf("get other scope: %v", err)
	} else if ok {
		t.Fatal("memo from session-1 must not be visible in session-2")
	}
}

func TestMemoStore_SizeLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(10)
	tooLarge := newTestMemoState(strings.Repeat("a", 20))
	if _, err := store.Put(ctx, "session-1", tooLarge); !errors.Is(err, ErrMemoTooLarge) {
		t.Fatalf("put expected ErrMemoTooLarge, got %v", err)
	}
	ref, err := store.Put(ctx, "session-1", newTestMemoState("short"))
	if err != nil {
		t.Fatalf("put short: %v", err)
	}
	if _, err := store.Update(ctx, "session-1", ref, tooLarge); !errors.Is(err, ErrMemoTooLarge) {
		t.Fatalf("update expected ErrMemoTooLarge, got %v", err)
	}
}

func TestMemoStore_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx := canceledCtx()
	store := NewMemoStore(4096)
	st := newTestMemoState("plan")
	ref := state.MemoRef{Key: "k1"}

	if _, err := store.Put(ctx, "s1", st); !errors.Is(err, context.Canceled) {
		t.Fatalf("put ctx: got %v want context.Canceled", err)
	}
	if _, _, err := store.Get(ctx, "s1", ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("get ctx: got %v want context.Canceled", err)
	}
	if _, err := store.Update(ctx, "s1", ref, st); !errors.Is(err, context.Canceled) {
		t.Fatalf("update ctx: got %v want context.Canceled", err)
	}
	if err := store.Delete(ctx, "s1", ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete ctx: got %v want context.Canceled", err)
	}
}

func TestMemoStore_EmptyScopeOrRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoStore(4096)
	st := newTestMemoState("plan")

	if _, err := store.Put(ctx, "", st); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("put empty scope: got %v want ErrEmptyScope", err)
	}
	if _, _, err := store.Get(ctx, "", state.MemoRef{Key: "k"}); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("get empty scope: got %v want ErrEmptyScope", err)
	}
	if _, _, err := store.Get(ctx, "s", state.MemoRef{}); !errors.Is(err, ErrEmptyScope) && !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("get empty ref: got %v want ErrEmptyMemoRef", err)
	}
	if _, err := store.Update(ctx, "", state.MemoRef{Key: "k"}, st); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("update empty scope: got %v want ErrEmptyScope", err)
	}
	if _, err := store.Update(ctx, "s", state.MemoRef{}, st); !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("update empty ref: got %v want ErrEmptyMemoRef", err)
	}
	if err := store.Delete(ctx, "", state.MemoRef{Key: "k"}); !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("delete empty scope: got %v want ErrEmptyScope", err)
	}
	if err := store.Delete(ctx, "s", state.MemoRef{}); !errors.Is(err, ErrEmptyMemoRef) {
		t.Fatalf("delete empty ref: got %v want ErrEmptyMemoRef", err)
	}
}
