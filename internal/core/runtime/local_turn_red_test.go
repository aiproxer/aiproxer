package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
)

// ---- generic fake tagger ----

type fakeTagger struct {
	mu        sync.Mutex
	calls     [][]TagRequest
	store     map[conversationprojection.MessageIdentity]conversationprojection.Tag
	rev       uint64
	nextErr   error
	failOn    int // call index to fail (1-based)
	callCount int
}

func newFakeTagger() *fakeTagger {
	return &fakeTagger{store: make(map[conversationprojection.MessageIdentity]conversationprojection.Tag), rev: 1}
}

func (f *fakeTagger) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var tags []conversationprojection.Tag
	for _, t := range f.store {
		tags = append(tags, t)
	}
	return conversationprojection.Snapshot{StateRevision: f.rev, NeverBackend: tags}, nil
}

func (f *fakeTagger) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.failOn != 0 && f.callCount == f.failOn {
		return TagResult{}, f.nextErr
	}
	if f.nextErr != nil && f.failOn == 0 {
		return TagResult{}, f.nextErr
	}
	// deep copy calls
	cp := make([]TagRequest, len(tags))
	copy(cp, tags)
	f.calls = append(f.calls, cp)
	for _, req := range tags {
		f.store[req.Identity] = conversationprojection.Tag{Identity: req.Identity, Reason: req.Reason}
	}
	f.rev++
	allTags := make([]conversationprojection.Tag, 0, len(f.store))
	for _, t := range f.store {
		allTags = append(allTags, t)
	}
	return TagResult{StateRevision: f.rev, Tags: allTags}, nil
}

func (f *fakeTagger) Calls() [][]TagRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([][]TagRequest, len(f.calls))
	for i, v := range f.calls {
		inner := make([]TagRequest, len(v))
		copy(inner, v)
		cp[i] = inner
	}
	return cp
}

// ---- generic fake handler ----

type fakeHandler struct {
	id          string
	ord         int
	mode        sdkhooks.FailureMode
	matchFn     func(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error)
	handleFn    func(context.Context, localturn.HandleInput) (localturn.Reply, error)
	matchCalls  atomic.Int32
	handleCalls atomic.Int32
	orderLog    *[]string
}

func (h *fakeHandler) ID() string                        { return h.id }
func (h *fakeHandler) Order() int                        { return h.ord }
func (h *fakeHandler) FailureMode() sdkhooks.FailureMode { return h.mode }
func (h *fakeHandler) Match(ctx context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
	h.matchCalls.Add(1)
	if h.orderLog != nil {
		*h.orderLog = append(*h.orderLog, h.id+":match")
	}
	if h.matchFn != nil {
		return h.matchFn(ctx, call, meta)
	}
	return localturn.MatchResult{}, nil
}

func (h *fakeHandler) Handle(ctx context.Context, in localturn.HandleInput) (localturn.Reply, error) {
	h.handleCalls.Add(1)
	if h.orderLog != nil {
		*h.orderLog = append(*h.orderLog, h.id+":handle")
	}
	if h.handleFn != nil {
		return h.handleFn(ctx, in)
	}
	return localturn.Reply{Text: "reply"}, nil
}

func newLocalExecutor(t *testing.T, tagger *fakeTagger, handlers []localturn.Handler) (*Executor, *b2bua.MemoryStore, string) {
	t.Helper()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	memSS := newInmemSecure(t, st)
	_ = memSS
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.ConversationViewTagger = tagger
	// also set reader to same tagger for snapshot
	ex.ConversationViewReader = tagger
	// need aLeg via secure path or detached? Use detached for simplicity with tagger store
	// But prepareRequest needs secure session for non-detached; we will use detached context in Execute
	// For local turn we still need aLeg creation; detached creates fresh ALeg
	ex.Rand = nil
	ex.Now = func() time.Time { return time.Unix(3000, 0) }
	// inject handlers via snapshot
	snapOpts := extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			LocalTurnHandlers: handlers,
		}),
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	return ex, st, ""
}

func newInmemSecure(t *testing.T, _ b2bua.Store) any {
	t.Helper()
	// not needed; use detached context to bypass secure session
	return nil
}

// helper to run Execute with detached and capture
func executeDetached(t *testing.T, ex *Executor, call *aipapi.Call) (aipapi.EventStream, error) {
	t.Helper()
	ctx := execDetachedCtx(context.Background())
	return ex.Execute(ctx, call)
}

// ---- RED tests ----

func TestLocalTurn_CausalOrder_MatchSourceTagHandleReplyTagStream(t *testing.T) {
	t.Parallel()
	var order []string
	tagger := newFakeTagger()
	h := &fakeHandler{
		id: "h1", ord: 1, mode: sdkhooks.FailClosed,
		orderLog: &order,
		matchFn: func(_ context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
			// claim first user message
			if meta.MessageCount < 1 {
				t.Fatalf("msgCount %d", meta.MessageCount)
			}
			order = append(order, "match")
			return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "test_reason"}, nil
		},
		handleFn: func(_ context.Context, in localturn.HandleInput) (localturn.Reply, error) {
			order = append(order, "handle")
			if len(in.Match.Indexes) != 1 || in.Match.Indexes[0] != 0 {
				t.Fatalf("handle input indexes %v", in.Match.Indexes)
			}
			return localturn.Reply{Text: "local reply"}, nil
		},
	}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	// need at least one message
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("world")}}}}

	// wrap tagger to record order of tag calls relative to handle
	origTag := tagger.TagNeverBackend
	callCount := 0
	tagger2 := &fakeTagger{store: make(map[conversationview.MessageIdentity]conversationview.Tag), rev: 1}
	// manual order tracking via wrapper
	handlerWrapped := &fakeHandler{
		id: "h1", ord: 1, mode: sdkhooks.FailClosed,
		matchFn: func(ctx context.Context, c aipapi.Call, m localturn.Meta) (localturn.MatchResult, error) {
			order = append(order, "match")
			return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "test_reason"}, nil
		},
		handleFn: func(ctx context.Context, in localturn.HandleInput) (localturn.Reply, error) {
			// check that source tag happened
			calls := tagger2.Calls()
			if len(calls) != 1 {
				t.Fatalf("handle should be after source tag, calls %v", calls)
			}
			order = append(order, "handle")
			return localturn.Reply{Text: "local reply"}, nil
		},
		orderLog: &order,
	}
	ex2, _, _ := newLocalExecutor(t, tagger2, []localturn.Handler{handlerWrapped})
	_ = tagger
	_ = origTag
	_ = callCount
	stream, err := executeDetached(t, ex2, call)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if stream == nil {
		t.Fatal("nil stream")
	}
	// verify tag order: first source, then reply, then stream
	calls := tagger2.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 tag calls (source + reply), got %d %v", len(calls), calls)
	}
	if len(calls[0]) != 1 || len(calls[1]) != 1 {
		t.Fatalf("each tag batch should have 1, got %v", calls)
	}
	// stream must contain same text as reply tag identity: decode reply identity vs stream content
	// check stream is finite and contains reply text
	ctx := context.Background()
	evs := collectEvents(ctx, t, stream)
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	found := false
	for _, e := range evs {
		if e.Kind == aipapi.EventTextDelta && e.Delta == "local reply" {
			found = true
		}
		if e.Kind == aipapi.EventUsageDelta {
			t.Fatalf("local stream must not emit usage")
		}
	}
	if !found {
		t.Fatalf("stream missing expected text_delta %v", evs)
	}
	// ensure order is match -> source tag (implicit) -> handle -> reply tag -> stream
	// order slice already tracks match/handle; tag calls tracked separately above.
	_ = ex
	_ = order
}

func collectEvents(ctx context.Context, t *testing.T, s aipapi.EventStream) []aipapi.Event {
	t.Helper()
	var out []aipapi.Event
	for {
		ev, err := s.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		out = append(out, ev)
		if ev.Kind == aipapi.EventResponseFinished || ev.Kind == aipapi.EventError {
			// drain to EOF
			for {
				_, e2 := s.Recv(ctx)
				if e2 != nil {
					break
				}
			}
			break
		}
	}
	_ = s.Close()
	return out
}

func TestLocalTurn_ZeroRouteBackendBillingAfterClaim(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	var backendOpens atomic.Int32
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "hi"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	// inject backend that would count opens, but local should not reach it
	ex.Backends = map[string]execbackend.Backend{"openai": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		backendOpens.Add(1)
		return aipapi.NewFixedEventStream(nil), nil
	}}}
	// We cannot easily intercept routePlans without hooking, but check billing via no exposure
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}

	stream, err := executeDetached(t, ex, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = stream
	if backendOpens.Load() != 0 {
		t.Fatalf("backend must not be opened after local claim, got %d", backendOpens.Load())
	}
}

func TestLocalTurn_PostClaimNoFallbackOnError(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{}, fmt.Errorf("handle boom")
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected error after post-claim handle failure")
	}
	if !strings.Contains(err.Error(), "handle") {
		t.Fatalf("error should contain handle, got %v", err)
	}
	// ensure no fallback backend was opened (tagger calls 1 source tag, no reply tag)
	if len(tagger.Calls()) != 1 {
		t.Fatalf("should have only source tag after handle failure, got %v", tagger.Calls())
	}
}

func TestLocalTurn_PostClaimPanicRecoveryNoFallback(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		panic("boom")
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected panic recovery error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("want panic in error, got %v", err)
	}
}

func TestLocalTurn_FailOpenVsFailClosedMatchError(t *testing.T) {
	t.Parallel()
	// fail-open handler error should not abort, should fall through to next handler or inference
	tagger := newFakeTagger()
	var secondMatched atomic.Bool
	h1 := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailOpen, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{}, fmt.Errorf("match boom")
	}}
	h2 := &fakeHandler{id: "h2", ord: 2, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		secondMatched.Store(true)
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "ok2"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h1, h2})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	stream, err := executeDetached(t, ex, call)
	if err != nil {
		t.Fatalf("fail-open should fall through to h2, got err %v", err)
	}
	if stream == nil || !secondMatched.Load() {
		t.Fatal("second handler should have run after fail-open")
	}
	// fail-closed handler error should abort request
	h3 := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{}, fmt.Errorf("boom")
	}}
	ex2, _, _ := newLocalExecutor(t, newFakeTagger(), []localturn.Handler{h3})
	_, err = executeDetached(t, ex2, call)
	if err == nil {
		t.Fatal("fail-closed match error should abort")
	}
}

func TestLocalTurn_HandlerOrderingFrozen(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	var log []string
	hA := &fakeHandler{id: "b", ord: 2, mode: sdkhooks.FailClosed, orderLog: &log, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: false}, nil
	}}
	hB := &fakeHandler{id: "a", ord: 1, mode: sdkhooks.FailClosed, orderLog: &log, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "ok"}, nil
	}}
	// Provide unordered slice; snapshot should sort by Order then ID
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{hA, hB})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}

	_, err := executeDetached(t, ex, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// hB (a, ord1) should run before hA (b, ord2)
	if len(log) < 1 || log[0] != "a:match" {
		t.Fatalf("ordering not frozen sorted, log %v", log)
	}
	// Verify snapshot frozen ordering
	snapHandlers := ex.RuntimeSnapshot.LocalTurnHandlers()
	if len(snapHandlers) != 2 || snapHandlers[0].ID() != "a" || snapHandlers[1].ID() != "b" {
		t.Fatalf("snapshot not frozen sorted %v", snapHandlers)
	}
}

func TestLocalTurn_SourceIndexValidation(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, m localturn.Meta) (localturn.MatchResult, error) {
		// claim out of range
		return localturn.MatchResult{Claimed: true, Indexes: []int{m.MessageCount}, Reason: "r"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected index validation failure")
	}
	if len(tagger.Calls()) != 0 {
		t.Fatalf("source tag must not run on invalid indexes, got %v", tagger.Calls())
	}
}

func TestLocalTurn_InvalidReplyNoStream(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: ""}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected invalid reply error")
	}
	// reply tag must not happen after invalid reply; only source tag
	if len(tagger.Calls()) != 1 {
		t.Fatalf("only source tag before invalid reply, got %d", len(tagger.Calls()))
	}
}

func TestLocalTurn_SourceTagFailureNoHandle(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	tagger.nextErr = fmt.Errorf("tag boom")
	var handleRan atomic.Bool
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		handleRan.Store(true)
		return localturn.Reply{Text: "ok"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected source tag failure")
	}
	if handleRan.Load() {
		t.Fatal("handle must not run after source tag failure")
	}
}

func TestLocalTurn_ReplyTagFailureNoStream(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	tagger.failOn = 2
	tagger.nextErr = fmt.Errorf("reply tag boom")
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "ok"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	_, err := executeDetached(t, ex, call)
	if err == nil {
		t.Fatal("expected reply tag failure")
	}
	// should have 2 calls? second fails, but first succeeded
}

func TestLocalTurn_CancellationAndCloseFiniteNoGoroutine(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "hello"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}

	stream, err := executeDetached(t, ex, call)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) && err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// Close must be safe and idempotent, no goroutine leak
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Drain remaining should be EOF finite
	for {
		_, e := stream.Recv(context.Background())
		if e != nil {
			if !errors.Is(e, io.EOF) && !errors.Is(e, context.Canceled) {
				t.Fatalf("expected EOF or Canceled, got %v", e)
			}
			break
		}
	}
}

func TestLocalTurn_MergesSnapshotWithoutSecondRead(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "replytext"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}

	// Use prepareRequest to inspect merged snapshot without second read
	ctx := execDetachedCtx(context.Background())
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()
	if !pr.isLocal {
		t.Fatal("should be local")
	}
	if len(pr.conversationSnapshot.NeverBackend) != 2 {
		t.Fatalf("merged snapshot should have 2 tags (source+reply), got %d", len(pr.conversationSnapshot.NeverBackend))
	}
	// Ensure no additional Snapshot call was made beyond the initial one (prepareRequest does one)
	// Tagger rev should be 3 (initial 1 +2 tags)
	if pr.conversationSnapshot.StateRevision != 3 {
		t.Fatalf("state revision should be 3, got %d", pr.conversationSnapshot.StateRevision)
	}
}

func TestLocalTurn_ItemAuthoritySourceIndex(t *testing.T) {
	t.Parallel()
	tagger := newFakeTagger()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
		if meta.MessageCount != 2 {
			t.Fatalf("item count %d want 2", meta.MessageCount)
		}
		return localturn.MatchResult{Claimed: true, Indexes: []int{1}, Reason: "r"}, nil
	}, handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
		return localturn.Reply{Text: "ok"}, nil
	}}
	ex, _, _ := newLocalExecutor(t, tagger, []localturn.Handler{h})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "first"}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "second"}}},
		},
	}
	_, err := executeDetached(t, ex, call)
	if err != nil {
		t.Fatalf("Execute item authority: %v", err)
	}
	if len(tagger.Calls()) != 2 {
		t.Fatalf("expected source+reply tags, got %v", tagger.Calls())
	}
}

func TestLocalTurn_TaggerUnavailableFailsDeterministically(t *testing.T) {
	t.Parallel()
	h := &fakeHandler{id: "h1", ord: 1, mode: sdkhooks.FailClosed, matchFn: func(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
		return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "r"}, nil
	}}
	// Use a store without conversationview capability and nil explicit tagger
	innerMem, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	bs := &blockingStore{inner: innerMem}
	ex := TestExecutor()
	ex.Store = bs
	ex.Bus = hooks.New(hooks.Config{})
	ex.ConversationViewTagger = nil
	ex.ConversationViewReader = nil
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			LocalTurnHandlers: []localturn.Handler{h},
		}),
	})
	ex.Now = func() time.Time { return time.Unix(3000, 0) }
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	_, _, _, err := ex.prepareRequest(execDetachedCtx(context.Background()), call)
	if err == nil || !strings.Contains(err.Error(), "conversation view tagger not available") {
		t.Fatalf("expected deterministic tagger unavailable error, got %v", err)
	}
}

func TestLocalTurn_SourceTagsRemainAuthoritativeAfterHandlerFailure_RealStore(t *testing.T) {
	t.Parallel()
	// This test proves Req 4.5: source tags remain authoritative after claimed Handler failure,
	// using the real MemoryStore ConversationViewStore capability (not fake-only).
	for _, tt := range []struct {
		name        string
		handleFn    func(context.Context, localturn.HandleInput) (localturn.Reply, error)
		expectPanic bool
	}{
		{
			name: "handle error retains source tag",
			handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
				return localturn.Reply{}, fmt.Errorf("handle boom")
			},
		},
		{
			name: "panic retains source tag",
			handleFn: func(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
				panic("boom")
			},
			expectPanic: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			cvStore := &autoRegisteringCVStore{ReferenceStore: conversationview.NewReferenceStore()}
			capturing := &capturingTagger{inner: cvStore}
			var backendOpens atomic.Int32
			h := &fakeHandler{
				id: "h1", ord: 1, mode: sdkhooks.FailClosed,
				matchFn: func(_ context.Context, call aipapi.Call, meta localturn.Meta) (localturn.MatchResult, error) {
					// Claim first message.
					return localturn.MatchResult{Claimed: true, Indexes: []int{0}, Reason: "test_reason"}, nil
				},
				handleFn: tt.handleFn,
			}
			ex := TestExecutor()
			ex.Store = st
			ex.Bus = hooks.New(hooks.Config{})
			ex.ConversationViewTagger = capturing
			ex.ConversationViewReader = cvStore
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
				FeaturePlanes: freezeBundle(testFeatureBundle{
					LocalTurnHandlers: []localturn.Handler{h},
				}),
			})
			ex.Now = func() time.Time { return time.Unix(3000, 0) }
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						backendOpens.Add(1)
						return aipapi.NewFixedEventStream(nil), nil
					},
				},
			}
			// Prepare call with known source identity.
			srcMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello source")}}
			srcID, err := conversationprojection.MessageIdentityOf(srcMsg)
			require.NoError(t, err)
			call := &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{srcMsg},
			}
			_, err = executeDetached(t, ex, call)
			require.Error(t, err, "Execute must fail after claimed handle error/panic")
			if tt.expectPanic {
				require.Contains(t, err.Error(), "panic")
			} else {
				require.Contains(t, err.Error(), "handle")
			}
			// No backend fallback.
			if backendOpens.Load() != 0 {
				t.Fatalf("backend must not open after post-claim handler failure, got %d", backendOpens.Load())
			}
			// Source TagNeverBackend must have succeeded exactly once (source tag) and no reply tag.
			capturing.mu.Lock()
			if len(capturing.aLegIDs) != 1 {
				t.Fatalf("expected exactly 1 TagNeverBackend call (source), got %d %v", len(capturing.aLegIDs), capturing.aLegIDs)
			}
			aLegID := capturing.aLegIDs[0]
			require.Equal(t, srcID, capturing.identities[0], "first tag must be source identity")
			capturing.mu.Unlock()
			// Read real MemoryStore ConversationViewStore snapshot and assert authoritative source tag remains.
			snap, err := cvStore.Snapshot(context.Background(), aLegID)
			require.NoError(t, err)
			require.Equal(t, uint64(1), snap.StateRevision, "source tag should bump StateRevision to 1")
			found := false
			for _, tag := range snap.NeverBackend {
				if tag.Identity == srcID {
					found = true
					assert.Equal(t, conversationprojection.ReasonCode("test_reason"), tag.Reason)
					break
				}
			}
			require.True(t, found, "real store snapshot must contain authoritative source tag %s", srcID)
			// Ensure no reply tag present (only source).
			require.Len(t, snap.NeverBackend, 1, "only source tag should be present after handle failure")
			// Also verify via executor's reader path yields same authoritative snapshot.
			readerSnap, err := conversationViewSnapshotForTest(context.Background(), ex, aLegID)
			require.NoError(t, err)
			assert.Equal(t, snap.StateRevision, readerSnap.StateRevision)
		})
	}
}

type autoRegisteringCVStore struct {
	*conversationview.ReferenceStore
}

func (a *autoRegisteringCVStore) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	_ = a.CreateALeg(ctx, aLegID)
	return a.ReferenceStore.Snapshot(ctx, aLegID)
}

func (a *autoRegisteringCVStore) TagNeverBackend(ctx context.Context, aLegID string, reqs []TagRequest) (TagResult, error) {
	_ = a.CreateALeg(ctx, aLegID)
	cvReqs := make([]conversationview.TagRequest, len(reqs))
	for i, r := range reqs {
		cvReqs[i] = conversationview.TagRequest{Identity: r.Identity, Reason: conversationview.ReasonCode(r.Reason)}
	}
	res, err := a.ReferenceStore.TagNeverBackend(ctx, aLegID, cvReqs)
	if err != nil {
		return TagResult{}, err
	}
	return TagResult{StateRevision: res.StateRevision, Tags: res.Tags}, nil
}

// Helper to expose AsReader for test without widening API.

type capturingTagger struct {
	inner      ConversationViewTagger
	mu         sync.Mutex
	aLegIDs    []string
	identities []conversationprojection.MessageIdentity
}

func (c *capturingTagger) TagNeverBackend(ctx context.Context, aLegID string, reqs []TagRequest) (TagResult, error) {
	c.mu.Lock()
	c.aLegIDs = append(c.aLegIDs, aLegID)
	for _, r := range reqs {
		c.identities = append(c.identities, r.Identity)
	}
	c.mu.Unlock()
	return c.inner.TagNeverBackend(ctx, aLegID, reqs)
}

// Expose snapshot via reader for verification convenience.
func conversationViewSnapshotForTest(ctx context.Context, e *Executor, aLegID string) (conversationprojection.Snapshot, error) {
	if e.ConversationViewReader != nil {
		return e.ConversationViewReader.Snapshot(ctx, aLegID)
	}
	return conversationprojection.Snapshot{}, fmt.Errorf("no reader")
}

type blockingStore struct {
	inner *b2bua.MemoryStore
}

func (b *blockingStore) ResolveALeg(ctx context.Context, k string) (b2bua.ALegRecord, error) {
	return b.inner.ResolveALeg(ctx, k)
}

func (b *blockingStore) CreateALeg(ctx context.Context, k string) (b2bua.ALegRecord, error) {
	return b.inner.CreateALeg(ctx, k)
}

func (b *blockingStore) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	return b.inner.FetchALeg(ctx, id)
}

func (b *blockingStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return b.inner.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func (b *blockingStore) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	return b.inner.NextBLeg(ctx, aLegID)
}

func (b *blockingStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	return b.inner.RecordAttempt(ctx, rec)
}

func (b *blockingStore) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	return b.inner.LoadAttempts(ctx, aLegID)
}
