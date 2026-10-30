package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// wireReplayCapture records upstream attempts for credential-replay tests.
type wireReplayCapture struct {
	mu      sync.Mutex
	auths   []string
	bodies  [][]byte
	lengths []int64
}

func (c *wireReplayCapture) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	c.bodies = append(c.bodies, body)
	c.lengths = append(c.lengths, r.ContentLength)
}

func (c *wireReplayCapture) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.auths)
}

func wireReplayPool(t *testing.T) *credpool.Pool {
	t.Helper()
	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "sk-wire-key-one"},
		{ID: "k2", Secret: "sk-wire-key-two"},
	})
	if err != nil {
		t.Fatalf("credpool.New failed: %v", err)
	}
	return pool
}

func wireReplayPrims(baseURL string, pool *credpool.Pool, threshold time.Duration) openaicompat.WireOpenPrimitives {
	return openaicompat.WireOpenPrimitives{
		ProviderID:           "replay-provider",
		BaseURL:              baseURL,
		Flavor:               openaicompat.FlavorChat,
		Pool:                 pool,
		HTTPClient:           httpclient.Standard(),
		RateLimitFallback:    time.Minute,
		QuotaSwitchThreshold: threshold,
		MaxPending:           50,
	}
}

func wireReplayRequest(t *testing.T, body []byte, factory largebody.WireReplayFactory) largebody.WireOpenRequest {
	t.Helper()
	return largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{Backend: "replay-provider", Model: "gpt-4o"},
		},
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
		WireRequest:   makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "trace-replay-test",
		ALegID:        "aleg-replay-test",
		BLegID:        "bleg-replay-test",
		Replay:        factory,
	}
}

func wireReplayBytesFactory(t *testing.T, body []byte) largebody.WireReplayFactory {
	t.Helper()
	factory, err := largebody.NewWireReplayFromBytes(body, 1<<20)
	if err != nil {
		t.Fatalf("NewWireReplayFromBytes failed: %v", err)
	}
	return factory
}

func drainWireReplayStream(t *testing.T, stream aipapi.ManagedEventStream) string {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var delta strings.Builder
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
		if ev.Kind == aipapi.EventTextDelta {
			delta.WriteString(ev.Delta)
		}
		if ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}
	return delta.String()
}

func wireReplaySnapshot(t *testing.T, pool *credpool.Pool) map[string]credpool.State {
	t.Helper()
	out := make(map[string]credpool.State)
	for _, st := range pool.Snapshot(time.Now()) {
		out[st.ID] = st.State
	}
	return out
}

func TestWireOpenReplay_RetriesNextKeyOnLongQuota429(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var capture wireReplayCapture
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7200")
			http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testChatSSEStream)
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	stream, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, wireReplayBytesFactory(t, body)))
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	if got := drainWireReplayStream(t, stream); !strings.Contains(got, "Hello") {
		t.Fatalf("delta = %q, want streamed content after credential rotation", got)
	}

	if n := capture.attempts(); n != 2 {
		t.Fatalf("upstream attempts = %d, want exactly 2 (one per key)", n)
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.auths[0] != "Bearer sk-wire-key-one" {
		t.Fatalf("attempt 1 auth = %q, want first key", capture.auths[0])
	}
	if capture.auths[1] != "Bearer sk-wire-key-two" {
		t.Fatalf("attempt 2 auth = %q, want second key after quota rotation", capture.auths[1])
	}
	if string(capture.bodies[0]) != string(body) || string(capture.bodies[1]) != string(body) {
		t.Fatalf("replayed bytes differ from exact input: %q vs %q", capture.bodies[0], capture.bodies[1])
	}
	if capture.lengths[0] != int64(len(body)) || capture.lengths[1] != int64(len(body)) {
		t.Fatalf("content lengths = %v, want exact %d on both attempts", capture.lengths, len(body))
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateCooldown {
		t.Fatalf("k1 state = %q, want cooldown for future calls", states["k1"])
	}
	if states["k2"] != credpool.StateUsable {
		t.Fatalf("k2 state = %q, want usable after success", states["k2"])
	}
}

func TestWireOpenReplay_AdvancesOnAuthInvalid(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testChatSSEStream)
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	stream, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, wireReplayBytesFactory(t, body)))
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	drainWireReplayStream(t, stream)

	if n := calls.Load(); n != 2 {
		t.Fatalf("upstream attempts = %d, want 2 after auth-invalid rotation", n)
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateAuthInvalid {
		t.Fatalf("k1 state = %q, want auth_invalid", states["k1"])
	}
	if states["k2"] != credpool.StateUsable {
		t.Fatalf("k2 state = %q, want usable", states["k2"])
	}
}

func TestWireOpenReplay_ShortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	_, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, wireReplayBytesFactory(t, body)))
	if err == nil {
		t.Fatal("expected a classified short-throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("short throttle must stay a recoverable pre-output error, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want exactly 1 (no secondary consumed)", n)
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateUsable || states["k2"] != credpool.StateUsable {
		t.Fatalf("short throttle must not cool credentials, got %v", states)
	}
}

func TestWireOpenReplay_WithoutReplayFactorySingleAttemptMarksCooldown(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "7200")
		http.Error(w, `{"error":{"message":"quota exhausted"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	req := wireReplayRequest(t, body, largebody.WireReplayFactory{})
	_, err := prims.OpenWire(context.Background(), req)
	if err == nil {
		t.Fatal("expected a classified quota error without replay")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("non-replayable quota failure must stay recoverable pre-output, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("non-replayable input must produce exactly 1 provider request, got %d", n)
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateCooldown {
		t.Fatalf("k1 state = %q, want cooldown for future calls", states["k1"])
	}
	if states["k2"] != credpool.StateUsable {
		t.Fatalf("k2 state = %q, want usable", states["k2"])
	}
}

func TestWireOpenReplay_NoSecondAttemptAfterOutputStarted(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Empty 200 body: the stream fails only after the provider already
		// accepted the full request, so no second provider execution is safe.
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	_, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, wireReplayBytesFactory(t, body)))
	if err == nil {
		t.Fatal("expected the empty-stream failure")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("uncertain post-200 output must never trigger a second provider request, got %d", n)
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateUsable || states["k2"] != credpool.StateUsable {
		t.Fatalf("uncertain output must not mutate credential state, got %v", states)
	}
}

func TestWireOpenReplay_CanceledContextPerformsNoAttempt(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testChatSSEStream)
	}))
	defer srv.Close()

	pool := wireReplayPool(t)
	prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prims.OpenWire(ctx, wireReplayRequest(t, body, wireReplayBytesFactory(t, body)))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("canceled context must produce zero provider requests, got %d", n)
	}
	states := wireReplaySnapshot(t, pool)
	if states["k1"] != credpool.StateUsable || states["k2"] != credpool.StateUsable {
		t.Fatalf("cancellation must not mutate credential state, got %v", states)
	}
}

func TestWireOpenReplay_AttemptReadersClosedOnAllPaths(t *testing.T) {
	t.Parallel()

	t.Run("retry_success_closes_both_readers", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","messages":[]}`)
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "7200")
				http.Error(w, "quota", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testChatSSEStream)
		}))
		defer srv.Close()

		base, err := largebody.NewWireReplayFromBytes(body, 1<<20)
		if err != nil {
			t.Fatalf("NewWireReplayFromBytes failed: %v", err)
		}
		var opens, closes atomic.Int32
		tracked := largebody.NewWireReplayFactory(func() (io.ReadCloser, int64, error) {
			rc, length, err := base.NewReader()
			if err != nil {
				return nil, 0, err
			}
			opens.Add(1)
			return &wireReplayCloseTracker{ReadCloser: rc, onClose: closes.Add}, length, nil
		})

		pool := wireReplayPool(t)
		prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
		stream, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, tracked))
		if err != nil {
			t.Fatalf("OpenWire failed: %v", err)
		}
		drainWireReplayStream(t, stream)
		if opens.Load() != 2 || closes.Load() != 2 {
			t.Fatalf("opens = %d closes = %d, want every attempt reader opened once and closed once", opens.Load(), closes.Load())
		}
	})

	t.Run("terminal_failure_closes_attempt_reader", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","messages":[]}`)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			w.Header().Set("Retry-After", "5")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		}))
		defer srv.Close()

		base, err := largebody.NewWireReplayFromBytes(body, 1<<20)
		if err != nil {
			t.Fatalf("NewWireReplayFromBytes failed: %v", err)
		}
		var opens, closes atomic.Int32
		tracked := largebody.NewWireReplayFactory(func() (io.ReadCloser, int64, error) {
			rc, length, err := base.NewReader()
			if err != nil {
				return nil, 0, err
			}
			opens.Add(1)
			return &wireReplayCloseTracker{ReadCloser: rc, onClose: closes.Add}, length, nil
		})

		pool := wireReplayPool(t)
		prims := wireReplayPrims(srv.URL+"/v1", pool, time.Minute)
		if _, err := prims.OpenWire(context.Background(), wireReplayRequest(t, body, tracked)); err == nil {
			t.Fatal("expected short-throttle error")
		}
		if opens.Load() != 1 || closes.Load() != 1 {
			t.Fatalf("opens = %d closes = %d, want the single attempt reader closed", opens.Load(), closes.Load())
		}
	})
}

// wireReplayCloseTracker counts reader closes for cleanup proof.
type wireReplayCloseTracker struct {
	io.ReadCloser
	onClose func(int32) int32
	once    atomic.Bool
}

func (c *wireReplayCloseTracker) Close() error {
	err := c.ReadCloser.Close()
	if c.once.CompareAndSwap(false, true) {
		c.onClose(1)
	}
	return err
}

func TestWireOpenReplay_BackendSpecThresholdPropagatesToWire(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "120")
			http.Error(w, "quota", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testChatSSEStream)
	}))
	defer srv.Close()

	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:      "threshold-wire-test",
		BaseURL: srv.URL + "/v1",
		Credentials: []credpool.Credential{
			{ID: "k1", Secret: "sk-wire-key-one"},
			{ID: "k2", Secret: "sk-wire-key-two"},
		},
		Flavor:               openaicompat.FlavorChat,
		RateLimitFallback:    time.Minute,
		QuotaSwitchThreshold: time.Minute,
	})
	if be.OpenWire == nil {
		t.Fatal("expected OpenWire on chat backend")
	}
	req := wireReplayRequest(t, body, wireReplayBytesFactory(t, body))
	req.Candidate = routing.AttemptCandidate{Primary: routing.Primary{Backend: "threshold-wire-test", Model: "gpt-4o"}}
	stream, err := be.OpenWire(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	drainWireReplayStream(t, stream)
	if n := calls.Load(); n != 2 {
		t.Fatalf("upstream attempts = %d, want 2 via propagated quota-switch threshold", n)
	}
}
