package openresponses_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// seqIDs issues deterministic, cryptographically valid proxy response IDs so
// continuation tests can reference concrete parent IDs across turns. Each turn
// receives the next ID in the sequence.
type seqIDs struct {
	mu  sync.Mutex
	n   int
	now time.Time
}

func (s *seqIDs) NewResponseID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return validProxyID(fmt.Sprintf("turn-%03d", s.n))
}

func (s *seqIDs) Now() time.Time { return s.now }

// validProxyID builds a proxy-safe response ID (resp_ + >= 16 bytes of base64
// payload) that the continuation contracts accept.
func validProxyID(seed string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte("ws-continuation-" + seed + "-0123456789abcdef"))
	return "resp_" + payload
}

func defaultLocalContinuation() openresponses.WSLocalContinuationConfig {
	return openresponses.DefaultWSLocalContinuation(openresponses.Config{})
}

// trackingLocalStore records Delete/PutTerminal/Reserve/Close on the wrapped
// connection-local store so tests can observe eviction and cleanup precisely.
type trackingLocalStore struct {
	aipcont.Store
	mu      sync.Mutex
	deletes []aipcont.ResponseID
	puts    []aipcont.ContinuationRecord
	reserve int
	closed  bool
}

func (s *trackingLocalStore) Delete(ctx context.Context, scope aipcont.Scope, id aipcont.ResponseID) error {
	s.mu.Lock()
	s.deletes = append(s.deletes, id)
	s.mu.Unlock()
	return s.Store.Delete(ctx, scope, id)
}

func (s *trackingLocalStore) PutTerminal(ctx context.Context, record aipcont.ContinuationRecord) error {
	s.mu.Lock()
	s.puts = append(s.puts, aipcont.CloneRecord(record))
	s.mu.Unlock()
	return s.Store.PutTerminal(ctx, record)
}

func (s *trackingLocalStore) Reserve(ctx context.Context, scope aipcont.Scope, policy aipcont.StoragePolicy) (aipcont.ResponseID, error) {
	s.mu.Lock()
	s.reserve++
	s.mu.Unlock()
	return s.Store.Reserve(ctx, scope, policy)
}

func (s *trackingLocalStore) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if closer, ok := s.Store.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (s *trackingLocalStore) deleted(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.deletes {
		if d.String() == id {
			return true
		}
	}
	return false
}

func (s *trackingLocalStore) deleteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deletes)
}

func (s *trackingLocalStore) reserveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserve
}

func (s *trackingLocalStore) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// newWSContinuationServer wires a real upgrade handler to a SessionRunner with
// connection-local continuation enabled using the supplied bounds/store factory.
func newWSContinuationServer(t *testing.T, exec *wsTurnExecutor, ids interface {
	openresponses.ResponseIDSource
	openresponses.ResponseClock
}, lc openresponses.WSLocalContinuationConfig,
) (*httptest.Server, *openresponses.WSCounters) {
	t.Helper()
	var execView openresponses.ExecutorView
	if exec != nil {
		execView = exec
	}
	runner := openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{
		Executor:          execView,
		ResponseIDSource:  ids,
		ResponseClock:     ids,
		MaterializeBounds: lc.MaterializeBounds,
	})
	hcfg := openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               runner,
		LocalContinuation:    &lc,
	}
	handler := openresponses.NewWebSocketHandler(hcfg)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, handler.Counters()
}

func successStream(delta string) aipapi.EventStream {
	return fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
}

// wsResponseID returns the response resource id from the terminal frame.
func wsResponseID(t *testing.T, frames []wsTextFrame) string {
	t.Helper()
	for _, f := range frames {
		typ, _ := f.data["type"].(string)
		if !isWSTerminalType(typ) {
			continue
		}
		res, _ := f.data["response"].(map[string]any)
		if res == nil {
			t.Fatalf("terminal frame missing response resource: %s", f.raw)
		}
		id, _ := res["id"].(string)
		if id == "" {
			t.Fatalf("terminal response missing id: %s", f.raw)
		}
		return id
	}
	t.Fatalf("no terminal frame with response resource in %v", frameTypes(frames))
	return ""
}

// wsResponsePreviousID returns the echoed previous_response_id of the terminal frame.
func wsResponsePreviousID(t *testing.T, frames []wsTextFrame) string {
	t.Helper()
	for _, f := range frames {
		typ, _ := f.data["type"].(string)
		if !isWSTerminalType(typ) {
			continue
		}
		res, _ := f.data["response"].(map[string]any)
		if res == nil {
			t.Fatalf("terminal frame missing response resource: %s", f.raw)
		}
		prev, _ := res["previous_response_id"].(string)
		return prev
	}
	t.Fatalf("no terminal frame in %v", frameTypes(frames))
	return ""
}

func wsResponseResource(t *testing.T, frames []wsTextFrame) map[string]any {
	t.Helper()
	for _, f := range frames {
		typ, _ := f.data["type"].(string)
		if !isWSTerminalType(typ) {
			continue
		}
		res, _ := f.data["response"].(map[string]any)
		if res == nil {
			t.Fatalf("terminal frame missing response resource: %s", f.raw)
		}
		return res
	}
	t.Fatalf("no terminal frame in %v", frameTypes(frames))
	return nil
}

func TestWebSocketContinuation_MaterializesLocalParentAndNewInput(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("answer-1"),
		successStream("answer-2"),
	}}
	var captured aipcont.Store
	var capturedScope aipcont.Scope
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		capturedScope = scope
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = inner
		return inner
	}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_000, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	frames1 := wsReadUntilTerminal(t, conn, 3*time.Second)
	id1 := wsResponseID(t, frames1)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"second"}`)
	frames2 := wsReadUntilTerminal(t, conn, 3*time.Second)

	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
	items := exec.callAt(1).Items
	if len(items) != 3 {
		t.Fatalf("materialized items=%d, want 3 (parent-in, parent-out, new-in)", len(items))
	}
	if got := items[0].Content[0].Text; got != "first" {
		t.Errorf("materialized[0]=%q, want first", got)
	}
	if got := items[1].Content[0].Text; got != "answer-1" {
		t.Errorf("materialized[1]=%q, want answer-1", got)
	}
	if got := items[2].Content[0].Text; got != "second" {
		t.Errorf("materialized[2]=%q, want second", got)
	}
	if prev := wsResponsePreviousID(t, frames2); prev != id1 {
		t.Errorf("echoed previous_response_id=%q, want %q", prev, id1)
	}
	if captured == nil {
		t.Fatal("connection-local store was not allocated")
	}
	rec, err := captured.Get(context.Background(), capturedScope, aipcont.ResponseID(id1))
	if err != nil {
		t.Fatalf("parent record missing from local store: %v", err)
	}
	if len(rec.OutputItems) == 0 {
		t.Fatalf("parent record has no output trajectory")
	}
	if len(rec.InputItems) != 1 || rec.InputItems[0].Content[0].Text != "first" {
		t.Fatalf("parent record input items=%+v, want [first]", rec.InputItems)
	}

	_ = conn.Close()
}

func TestWebSocketContinuation_SuccessChainStaysContinuable(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("a1"),
		successStream("a2"),
		successStream("a3"),
	}}
	ids := &seqIDs{now: time.Unix(1_700_003_010, 0)}
	srv, _ := newWSContinuationServer(t, exec, ids, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"second"}`)
	id2 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id2+`","input":"third"}`)
	frames3 := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	if exec.count() != 3 {
		t.Fatalf("executor calls=%d, want 3", exec.count())
	}
	if prev := wsResponsePreviousID(t, frames3); prev != id2 {
		t.Errorf("turn 3 echoed previous_response_id=%q, want %q", prev, id2)
	}
	// Depth-2 materialization: id2 (input+output) then id1 (input+output) then new input.
	items := exec.callAt(2).Items
	if len(items) != 5 {
		t.Fatalf("depth-2 materialized items=%d, want 5", len(items))
	}
	want := []string{"first", "a1", "second", "a2", "third"}
	for i, w := range want {
		if got := items[i].Content[0].Text; got != w {
			t.Errorf("materialized[%d]=%q, want %q", i, got, w)
		}
	}
}

func TestWebSocketContinuation_ReconnectLosesLocalState(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		successStream("recovered"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_100, 0)}, defaultLocalContinuation())

	connA := wsDial(t, srv, nil)
	wsText(t, connA, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	idA := wsResponseID(t, wsReadUntilTerminal(t, connA, 3*time.Second))
	_ = connA.Close()

	// A new connection allocates a fresh connection-scoped store: the earlier
	// store:false ID is indistinguishable from a missing parent.
	connB := wsDial(t, srv, nil)
	wsText(t, connB, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+idA+`","input":"reconnect"}`)
	f := wsReadTextFrame(t, connB, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "previous_response_not_found" {
		t.Fatalf("reconnect continuation code=%q, want previous_response_not_found", code)
	}
	if exec.count() != 1 {
		t.Fatalf("reconnect continuation reached the executor: calls=%d", exec.count())
	}

	// The new connection still serves fresh turns.
	wsText(t, connB, `{"type":"response.create","model":"gpt-4o","input":"fresh"}`)
	framesB := wsReadUntilTerminal(t, connB, 3*time.Second)
	if !containsDelta(framesB, "recovered") {
		t.Fatalf("reconnected connection did not serve a fresh turn: %v", frameTypes(framesB))
	}
	_ = connB.Close()
	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
}

func TestWebSocketContinuation_MissingParentReturnsNotFound(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("alive")}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_200, 0)}, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	missing := validProxyID("missing-parent")
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+missing+`","input":"hi"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, param := wsErrorEnvelope(t, f)
	if code != "previous_response_not_found" {
		t.Fatalf("error code=%q, want previous_response_not_found (frame: %s)", code, f.raw)
	}
	if param != "previous_response_id" {
		t.Fatalf("error param=%q, want previous_response_id", param)
	}
	if exec.count() != 0 {
		t.Fatalf("missing parent reached the executor: calls=%d", exec.count())
	}

	// The connection survives and serves a fresh turn.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"next"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames, "alive") {
		t.Fatalf("connection did not survive missing parent: %v", frameTypes(frames))
	}
	_ = conn.Close()
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}
}

func TestWebSocketContinuation_ModelResolvedFromParentLineage(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("first-answer"),
		successStream("second-answer"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_300, 0)}, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"first"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// Model omitted: resolved from the parent lineage.
	wsText(t, conn, `{"type":"response.create","previous_response_id":"`+id1+`","input":"second"}`)
	frames2 := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	call2 := exec.callAt(1)
	if call2 == nil {
		t.Fatal("executor received no continuation call")
	}
	if call2.Route.Selector != "gpt-4o" {
		t.Errorf("route selector=%q, want gpt-4o (resolved from lineage)", call2.Route.Selector)
	}
	if model, _ := wsResponseResource(t, frames2)["model"].(string); model != "gpt-4o" {
		t.Errorf("echoed model=%q, want gpt-4o", model)
	}
}

func TestWebSocketContinuation_ClassifiedFailureEvictsParent(t *testing.T) {
	exec := &wsTurnExecutor{
		errs: []error{nil, errors.New("backend exploded"), nil},
		streams: []aipapi.EventStream{
			successStream("one"),
			nil,
			successStream("three"),
		},
	}
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_400, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// Classified 5xx continuation failure (backend_error) must evict id1.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"two"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "backend_error" {
		t.Fatalf("error code=%q, want backend_error (frame: %s)", code, f.raw)
	}
	if captured == nil || !captured.deleted(id1) {
		t.Fatalf("parent %q was not evicted after classified failure", id1)
	}

	// The evicted parent is indistinguishable from a missing response.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"three"}`)
	f2 := wsReadTextFrame(t, conn, 3*time.Second)
	code2, _ := wsErrorEnvelope(t, f2)
	if code2 != "previous_response_not_found" {
		t.Fatalf("post-eviction code=%q, want previous_response_not_found", code2)
	}
	_ = conn.Close()
	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2 (evicted turn never reaches the executor)", exec.count())
	}
}

func TestWebSocketContinuation_DecodeFailureEvictsReferencedParent(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		successStream("later"),
	}}
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_410, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// A rejected turn that referenced the parent is a classified 4xx continuation
	// failure: store:true is not supported on WebSocket turns.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","store":true,"previous_response_id":"`+id1+`","input":"two"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	if code, _ := wsErrorEnvelope(t, f); code != "unsupported_parameter" {
		t.Fatalf("error code=%q, want unsupported_parameter", code)
	}
	if captured == nil || !captured.deleted(id1) {
		t.Fatalf("parent %q was not evicted after a classified decode failure", id1)
	}
	_ = conn.Close()
}

func TestWebSocketContinuation_DisconnectRetainsParent(t *testing.T) {
	blocked := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "slow"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: make(chan struct{}),
	}
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		blocked,
	}}
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, counters := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_500, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"slow"}`)
	eventually(t, 3*time.Second, func() bool { return exec.count() == 2 })

	// Cancellation on disconnect must not evict the referenced parent.
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
	if captured == nil || captured.deleteCount() != 0 {
		t.Fatalf("disconnect evicted the parent: deletes=%v", captured.deletes)
	}
}

func TestWebSocketContinuation_TransportFailureRetainsParent(t *testing.T) {
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
	for i := range 2000 {
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: fmt.Sprintf("d%05d-%s", i, strings.Repeat("x", 40))})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		fixedStream(events...),
	}}
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, counters := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_600, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// Abruptly kill the socket mid-stream so the server's data writes fail. The
	// failure is a transport failure, never a classified continuation failure,
	// so the referenced parent must not be evicted.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"burst"}`)
	if tc, ok := conn.UnderlyingConn().(interface{ SetLinger(int) error }); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
	if captured == nil || captured.deleteCount() != 0 {
		t.Fatalf("transport failure evicted the parent: deletes=%v", captured.deletes)
	}
}

func TestWebSocketContinuation_ChainDepthBoundRejectsDeepChains(t *testing.T) {
	lc := defaultLocalContinuation()
	lc.MaterializeBounds = aipcont.Bounds{
		MaxChainDepth:        2,
		MaxMaterializedItems: 100_000,
		MaxMaterializedBytes: 64 << 20,
	}
	lc.Limits.MaxChainDepth = 10
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("a"),
		successStream("b"),
		successStream("c"),
		successStream("d"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_700, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"two"}`)
	id2 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id2+`","input":"three"}`)
	id3 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// Referencing id3 walks a depth-3 chain, exceeding MaxChainDepth=2.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id3+`","input":"four"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, _ := wsErrorEnvelope(t, f)
	if code != "invalid_request" {
		t.Fatalf("deep-chain code=%q, want invalid_request", code)
	}
	if exec.count() != 3 {
		t.Fatalf("executor calls=%d, want 3 (deep continuation rejected before execution)", exec.count())
	}
	_ = conn.Close()
}

func TestWebSocketContinuation_LocalStoreRecordBoundEvictsOldest(t *testing.T) {
	lc := defaultLocalContinuation()
	lc.Limits.MaxRecords = 1
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("a"),
		successStream("b"),
		successStream("c"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_003_800, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"two"}`)
	id2 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// id1 was evicted to make room for id2.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"old"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	if code, _ := wsErrorEnvelope(t, f); code != "previous_response_not_found" {
		t.Fatalf("evicted parent %q did not return previous_response_not_found: %s", id1, f.raw)
	}

	// id2 is still retained within the bounded store.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id2+`","input":"current"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if !containsDelta(frames, "c") {
		t.Fatalf("retained parent %q did not continue: %v", id2, frameTypes(frames))
	}
	_ = conn.Close()
	if exec.count() != 3 {
		t.Fatalf("executor calls=%d, want 3", exec.count())
	}
}

func TestWebSocketContinuation_CloseClearsLocalState(t *testing.T) {
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, counters := newWSContinuationServer(t, execWith(successStream("one")), &seqIDs{now: time.Unix(1_700_003_900, 0)}, lc)
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()
	eventually(t, 3*time.Second, func() bool { return counters.Snapshot().SessionsClosed == 1 })
	if captured == nil || !captured.isClosed() {
		t.Fatalf("connection-local store was not closed on session close")
	}
}

func TestWebSocketContinuation_CompactionItemStartsNewChain(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("compact-answer"),
		successStream("follow-answer"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_004_000, 0)}, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	// Compacted window used directly as the base input of a new response: no
	// previous_response_id, so it starts a fresh chain.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":[{"type":"compaction","encapsulated_id":"c_1","dialect":"x/v1","implementor":"me"},{"type":"message","role":"user","content":[{"type":"text","text":"continue"}]}]}`)
	frames1 := wsReadUntilTerminal(t, conn, 3*time.Second)
	id1 := wsResponseID(t, frames1)
	if prev := wsResponsePreviousID(t, frames1); prev != "" {
		t.Errorf("compaction base echoed previous_response_id=%q, want empty new chain", prev)
	}

	// The compaction base is continuable: materialize walks only the base window.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"next"}`)
	frames2 := wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
	items := exec.callAt(1).Items
	if len(items) != 4 {
		t.Fatalf("materialized items=%d, want 4 (compaction, message, base-output, new-input)", len(items))
	}
	if items[0].Kind != aipapi.ItemKindCompaction {
		t.Errorf("materialized[0].kind=%q, want compaction", items[0].Kind)
	}
	if prev := wsResponsePreviousID(t, frames2); prev != id1 {
		t.Errorf("continuation from compaction base echoed previous_response_id=%q, want %q", prev, id1)
	}
}

func TestWebSocketContinuation_CompactionInputWithParentRejected(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		successStream("later"),
	}}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_004_100, 0)}, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// A compaction window combined with previous_response_id is contradictory:
	// compaction always starts a new chain without the old response ID.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":[{"type":"compaction","dialect":"x/v1"}]}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	code, param := wsErrorEnvelope(t, f)
	if code != "invalid_request" {
		t.Fatalf("error code=%q, want invalid_request", code)
	}
	if param != "previous_response_id" {
		t.Fatalf("error param=%q, want previous_response_id", param)
	}
	if exec.count() != 1 {
		t.Fatalf("rejected compaction continuation reached the executor: calls=%d", exec.count())
	}
	_ = conn.Close()
}

func TestWebSocketContinuation_PostOutputFailedTerminalEvictsParent(t *testing.T) {
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{
		successStream("one"),
		fixedStream(
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventError},
		),
		successStream("three"),
	}}
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_004_150, 0)}, lc)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	id1 := wsResponseID(t, wsReadUntilTerminal(t, conn, 3*time.Second))

	// A post-output failure emits response.failed and is a classified failure:
	// the referenced parent must be evicted.
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"two"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	if got := frames[len(frames)-1].data["type"]; got != "response.failed" {
		t.Fatalf("terminal type=%v, want response.failed", got)
	}
	// Eviction is a server-side store write that lands after the terminal frame is
	// emitted, so poll for it like the other session-lifecycle assertions.
	eventually(t, 3*time.Second, func() bool {
		return captured != nil && captured.deleted(id1)
	})

	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","previous_response_id":"`+id1+`","input":"three"}`)
	f := wsReadTextFrame(t, conn, 3*time.Second)
	if code, _ := wsErrorEnvelope(t, f); code != "previous_response_not_found" {
		t.Fatalf("post-failed-terminal code=%q, want previous_response_not_found", code)
	}
	_ = conn.Close()
	if exec.count() != 2 {
		t.Fatalf("executor calls=%d, want 2", exec.count())
	}
}

func TestWebSocketContinuation_SequentialChainStress(t *testing.T) {
	const turns = 10
	streams := make([]aipapi.EventStream, turns)
	for i := range streams {
		streams[i] = successStream(fmt.Sprintf("answer-%d", i))
	}
	exec := &wsTurnExecutor{streams: streams}
	srv, _ := newWSContinuationServer(t, exec, &seqIDs{now: time.Unix(1_700_004_300, 0)}, defaultLocalContinuation())
	conn := wsDial(t, srv, nil)

	prev := ""
	for i := range turns {
		body := `{"type":"response.create","model":"gpt-4o","input":"turn `
		if prev == "" {
			body += fmt.Sprintf(`%d"}`, i)
		} else {
			body += fmt.Sprintf(`%d","previous_response_id":"`+prev+`"}`, i)
		}
		wsText(t, conn, body)
		frames := wsReadUntilTerminal(t, conn, 5*time.Second)
		if !containsDelta(frames, fmt.Sprintf("answer-%d", i)) {
			t.Fatalf("turn %d produced wrong output: %v", i, frameTypes(frames))
		}
		prev = wsResponseID(t, frames)
	}
	_ = conn.Close()

	if exec.count() != turns {
		t.Fatalf("executor calls=%d, want %d", exec.count(), turns)
	}
	// Final turn materializes the full prior chain plus its own input.
	items := exec.callAt(turns - 1).Items
	if want := turns*2 - 1; len(items) != want {
		t.Fatalf("final materialized items=%d, want %d", len(items), want)
	}
}

func TestWebSocketContinuation_NeverUsesDurableStoreReservation(t *testing.T) {
	var captured *trackingLocalStore
	lc := defaultLocalContinuation()
	lc.StoreFactory = func(scope aipcont.Scope) aipcont.Store {
		inner := openresponses.NewWSLocalStore(scope, lc.Limits)
		captured = &trackingLocalStore{Store: inner}
		return captured
	}
	srv, _ := newWSContinuationServer(t, execWith(successStream("one")), &seqIDs{now: time.Unix(1_700_004_200, 0)}, lc)
	conn := wsDial(t, srv, nil)
	wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"one"}`)
	frames := wsReadUntilTerminal(t, conn, 3*time.Second)
	id1 := wsResponseID(t, frames)
	_ = conn.Close()

	if captured == nil {
		t.Fatal("local store was not allocated")
	}
	// The runner never reserves from any store: turn records are written directly
	// under the proxy response ID and no durable-store reservation is involved.
	if captured.reserveCount() != 0 {
		t.Fatalf("reserve calls=%d, want 0 (connection-local records are not reserved/durable)", captured.reserveCount())
	}
	if len(captured.puts) != 1 {
		t.Fatalf("puts=%d, want 1 completed local record", len(captured.puts))
	}
	if captured.puts[0].ID.String() != id1 {
		t.Errorf("recorded ID=%q, want %q", captured.puts[0].ID.String(), id1)
	}
}

// execWith adapts streams into a wsTurnExecutor for concise setup.
func execWith(streams ...aipapi.EventStream) *wsTurnExecutor {
	return &wsTurnExecutor{streams: streams}
}
