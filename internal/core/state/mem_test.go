package state_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func steadyClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestMem_globalRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := state.NewMem(steadyClock(time.Unix(1700, 0)))
	if err := s.Put(ctx, aipstate.ScopeGlobal, "ns", "k", "v", 0); err != nil {
		t.Fatal(err)
	}
	var out string
	found, err := s.Get(ctx, aipstate.ScopeGlobal, "ns", "k", &out)
	if err != nil || !found || out != "v" {
		t.Fatalf("get found=%v out=%q err=%v", found, out, err)
	}
	if err := s.Delete(ctx, aipstate.ScopeGlobal, "ns", "k"); err != nil {
		t.Fatal(err)
	}
	found, err = s.Get(ctx, aipstate.ScopeGlobal, "ns", "k", &out)
	if err != nil || found {
		t.Fatalf("after delete found=%v err=%v", found, err)
	}
}

func TestMem_requestPartitionIsolation(t *testing.T) {
	t.Parallel()
	clk := time.Unix(2000, 0)
	s := state.NewMem(steadyClock(clk))
	ctxA := execctx.WithViews(context.Background(), execctx.Views{
		Attempt: execview.AttemptView{TraceID: "trace-a"},
	})
	ctxB := execctx.WithViews(context.Background(), execctx.Views{
		Attempt: execview.AttemptView{TraceID: "trace-b"},
	})
	if err := s.Put(ctxA, aipstate.ScopeRequest, "ns", "k", "a", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctxB, aipstate.ScopeRequest, "ns", "k", "b", 0); err != nil {
		t.Fatal(err)
	}
	var out string
	found, err := s.Get(ctxA, aipstate.ScopeRequest, "ns", "k", &out)
	if err != nil || !found || out != "a" {
		t.Fatalf("ctxA %+v", out)
	}
	found, err = s.Get(ctxB, aipstate.ScopeRequest, "ns", "k", &out)
	if err != nil || !found || out != "b" {
		t.Fatalf("ctxB %+v", out)
	}
}

func TestMem_sessionPartitionIsolation(t *testing.T) {
	t.Parallel()
	s := state.NewMem(steadyClock(time.Unix(2100, 0)))
	ctxA := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{ClientSessionHint: "sess-a"},
	})
	ctxB := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{ClientSessionHint: "sess-b"},
	})
	_ = s.Put(ctxA, aipstate.ScopeSession, "ns", "flag", "1", 0)
	_ = s.Put(ctxB, aipstate.ScopeSession, "ns", "flag", "2", 0)
	var out string
	_, _ = s.Get(ctxA, aipstate.ScopeSession, "ns", "flag", &out)
	if out != "1" {
		t.Fatalf("sess-a want 1 got %q", out)
	}
	_, _ = s.Get(ctxB, aipstate.ScopeSession, "ns", "flag", &out)
	if out != "2" {
		t.Fatalf("sess-b want 2 got %q", out)
	}
}

func TestMem_principalRequiresID(t *testing.T) {
	t.Parallel()
	s := state.NewMem(time.Now)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Principal: execview.PrincipalView{ID: ""},
	})
	err := s.Put(ctx, aipstate.ScopePrincipal, "ns", "k", "v", 0)
	if !errors.Is(err, aipstate.ErrMissingPrincipal) {
		t.Fatalf("put: %v", err)
	}
}

func TestMem_missingViewsForRequestScope(t *testing.T) {
	t.Parallel()
	s := state.NewMem(time.Now)
	ctx := context.Background()
	err := s.Put(ctx, aipstate.ScopeRequest, "ns", "k", "v", 0)
	if !errors.Is(err, aipstate.ErrMissingExecutionContext) {
		t.Fatalf("put: %v", err)
	}
}

func TestMem_unknownScope(t *testing.T) {
	t.Parallel()
	s := state.NewMem(time.Now)
	ctx := context.Background()
	err := s.Put(ctx, aipstate.Scope("nope"), "ns", "k", "v", 0)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestMem_TTLGetAndInspectTTL(t *testing.T) {
	t.Parallel()
	start := time.Unix(3000, 0)
	clk := &mutableClock{t: start}
	s := state.NewMem(clk.Now)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Attempt: execview.AttemptView{TraceID: "tr"},
	})
	if err := s.Put(ctx, aipstate.ScopeRequest, "ns", "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	ttl, found, err := s.InspectTTL(ctx, aipstate.ScopeRequest, "ns", "k")
	if err != nil || !found || ttl != time.Minute {
		t.Fatalf("inspect start: ttl=%v found=%v err=%v", ttl, found, err)
	}
	var out string
	foundG, err := s.Get(ctx, aipstate.ScopeRequest, "ns", "k", &out)
	if err != nil || !foundG || out != "v" {
		t.Fatalf("get before advance: %v", err)
	}
	clk.t = start.Add(30 * time.Second)
	ttl, found, err = s.InspectTTL(ctx, aipstate.ScopeRequest, "ns", "k")
	if err != nil || !found || ttl != 30*time.Second {
		t.Fatalf("inspect mid: ttl=%v found=%v err=%v", ttl, found, err)
	}
	clk.t = start.Add(time.Minute)
	foundG, err = s.Get(ctx, aipstate.ScopeRequest, "ns", "k", &out)
	if err != nil || foundG {
		t.Fatalf("get at expiry want miss found=%v err=%v", foundG, err)
	}
	ttl, found, err = s.InspectTTL(ctx, aipstate.ScopeRequest, "ns", "k")
	if err != nil || found {
		t.Fatalf("inspect after expiry want not found: ttl=%v found=%v err=%v", ttl, found, err)
	}
}

func TestMem_InspectTTLDeletesExpiredEntry(t *testing.T) {
	t.Parallel()
	start := time.Unix(4000, 0)
	clk := &mutableClock{t: start}
	s := state.NewMem(clk.Now)
	ctx := context.Background()
	if err := s.Put(ctx, aipstate.ScopeGlobal, "ns", "k", "v", time.Second); err != nil {
		t.Fatal(err)
	}
	clk.t = start.Add(2 * time.Second)
	_, found, err := s.InspectTTL(ctx, aipstate.ScopeGlobal, "ns", "k")
	if err != nil || found {
		t.Fatalf("inspect: found=%v err=%v", found, err)
	}
	clk.t = start.Add(3 * time.Second)
	var out string
	foundG, err := s.Get(ctx, aipstate.ScopeGlobal, "ns", "k", &out)
	if err != nil || foundG {
		t.Fatalf("get after inspect purge: found=%v", foundG)
	}
}

func TestMem_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	s := state.NewMem(steadyClock(time.Unix(5000, 0)))
	ctx := context.Background()
	if err := s.Delete(ctx, aipstate.ScopeGlobal, "ns", "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestMem_ConcurrentGlobal(t *testing.T) {
	t.Parallel()
	s := state.NewMem(time.Now)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.Put(ctx, aipstate.ScopeGlobal, "ns", "k", "x", 0)
			var out string
			_, _ = s.Get(ctx, aipstate.ScopeGlobal, "ns", "k", &out)
		}(i)
	}
	wg.Wait()
}

func TestMem_flowSessionAndWorkspaceCorrelation(t *testing.T) {
	t.Parallel()
	clk := &mutableClock{t: time.Unix(6000, 0)}
	s := state.NewMem(clk.Now)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session:   session.SessionView{ClientSessionHint: "sess-ws", Labels: map[string]string{"tier": "a"}},
		Attempt:   execview.AttemptView{TraceID: "req-ws"},
		Workspace: workspace.WorkspaceView{ProjectRoot: "/proj/x", Labels: map[string]string{"kind": "repo"}},
	})
	if err := s.Put(ctx, aipstate.ScopeSession, "guard", "lastRoot", "/proj/x", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, aipstate.ScopeRequest, "guard", "once", "1", 0); err != nil {
		t.Fatal(err)
	}
	var root, once string
	_, _ = s.Get(ctx, aipstate.ScopeSession, "guard", "lastRoot", &root)
	_, _ = s.Get(ctx, aipstate.ScopeRequest, "guard", "once", &once)
	if root != "/proj/x" || once != "1" {
		t.Fatalf("root=%q once=%q", root, once)
	}
	ctxOther := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{ClientSessionHint: "other"},
		Attempt: execview.AttemptView{TraceID: "req-other"},
	})
	found, err := s.Get(ctxOther, aipstate.ScopeSession, "guard", "lastRoot", &root)
	if err != nil || found {
		t.Fatalf("other session should miss session scope: found=%v", found)
	}
}

func TestMem_flowTTLExpirySession(t *testing.T) {
	t.Parallel()
	start := time.Unix(7000, 0)
	clk := &mutableClock{t: start}
	s := state.NewMem(clk.Now)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{ClientSessionHint: "ttl-sess"},
	})
	if err := s.Put(ctx, aipstate.ScopeSession, "p", "k", "v", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	clk.t = start.Add(5 * time.Second)
	d, ok, err := s.InspectTTL(ctx, aipstate.ScopeSession, "p", "k")
	if err != nil || !ok || d != 5*time.Second {
		t.Fatalf("mid ttl d=%v ok=%v err=%v", d, ok, err)
	}
	clk.t = start.Add(11 * time.Second)
	var out string
	found, err := s.Get(ctx, aipstate.ScopeSession, "p", "k", &out)
	if err != nil || found {
		t.Fatalf("expired get found=%v", found)
	}
}

type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
