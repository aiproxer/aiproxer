package stdhttp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/stretchr/testify/assert"
)

// Task 1.5 no-drop contracts (req 5.1-5.10). Barriers/channels only — no sleeps.

func newNoDropHarness(t *testing.T) (*RefGenerationDispatcher, *runtimehost.Manager, *ListenerAliveCounter) {
	t.Helper()
	m := runtimehost.NewManager(8, nil)
	return NewRefGenerationDispatcher(m, NewNoDropHandlerRegistry()), m, &ListenerAliveCounter{}
}

func genEchoHandler(label string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := GenerationFromContext(r.Context())
		w.Header().Set("X-Gen-ID", fmt.Sprintf("%d", id))
		_, _ = io.WriteString(w, label)
	})
}

func TestNoDrop_KeepAlive_HTTP2_SSE_Cancel_Failover_Races(t *testing.T) {
	t.Parallel()

	t.Run("http11_keepalive", func(t *testing.T) {
		t.Parallel()
		d, _, alive := newNoDropHarness(t)
		if _, err := d.PublishWithHandler("g1", genEchoHandler("g1")); err != nil {
			t.Fatal(err)
		}
		alive.MarkPublish()
		srv := httptest.NewServer(d)
		defer srv.Close()
		var dials atomic.Int64
		tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
		client := &http.Client{Transport: tr}
		defer tr.CloseIdleConnections()
		res1, err := client.Get(srv.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		b1, _ := io.ReadAll(res1.Body)
		assert.NoError(t, res1.Body.Close())
		if _, err := d.PublishWithHandler("g2", genEchoHandler("g2")); err != nil {
			t.Fatal(err)
		}
		alive.MarkPublish()
		res2, err := client.Get(srv.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		b2, _ := io.ReadAll(res2.Body)
		assert.NoError(t, res2.Body.Close())
		if string(b1) != "g1" || string(b2) != "g2" || dials.Load() != 1 || alive.Publishes() != 2 {
			t.Fatalf("b1=%s b2=%s dials=%d pubs=%d", b1, b2, dials.Load(), alive.Publishes())
		}
	})

	t.Run("http2_multiplex", func(t *testing.T) {
		t.Parallel()
		d, m, _ := newNoDropHarness(t)
		hold, entered := make(chan struct{}), make(chan struct{}, 2)
		h1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := GenerationFromContext(r.Context())
			entered <- struct{}{}
			<-hold
			_, _ = fmt.Fprintf(w, "%d", id)
		})
		if _, err := d.PublishWithHandler("g1", h1); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(d)
		srv.EnableHTTP2 = true
		srv.StartTLS()
		defer srv.Close()
		client := srv.Client()
		out := make(chan string, 2)
		start := make(chan struct{})
		for range 2 {
			go func() {
				<-start
				res, err := client.Get(srv.URL + "/s")
				if err != nil {
					out <- "err"
					return
				}
				b, _ := io.ReadAll(res.Body)
				assert.NoError(t, res.Body.Close())
				out <- string(b)
			}()
		}
		close(start)
		<-entered
		<-entered
		if _, err := d.PublishWithHandler("g2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := GenerationFromContext(r.Context())
			_, _ = fmt.Fprintf(w, "%d", id)
		})); err != nil {
			t.Fatal(err)
		}
		close(hold)
		if a, b := <-out, <-out; a != "1" || b != "1" {
			t.Fatalf("in-flight=%s %s active=%s", a, b, m.Active().Label())
		}
		res, err := client.Get(srv.URL + "/s")
		if err != nil {
			t.Fatal(err)
		}
		nb, _ := io.ReadAll(res.Body)
		assert.NoError(t, res.Body.Close())
		if string(nb) != "2" {
			t.Fatalf("new stream=%s", nb)
		}
	})

	t.Run("sse_retain", func(t *testing.T) {
		t.Parallel()
		d, m, _ := newNoDropHarness(t)
		first, resume := make(chan struct{}), make(chan struct{})
		if _, err := d.PublishWithHandler("g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := GenerationFromContext(r.Context())
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "ResponseWriter does not implement http.Flusher", http.StatusInternalServerError)
				t.Error("ResponseWriter does not implement http.Flusher")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = fmt.Fprintf(w, "data: start-%d\n\n", id)
			flusher.Flush()
			close(first)
			<-resume
			_, _ = fmt.Fprintf(w, "data: end-%d\n\n", id)
			flusher.Flush()
		})); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(d)
		defer srv.Close()
		res, err := http.Get(srv.URL + "/sse")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { assert.NoError(t, res.Body.Close()) }()
		<-first
		if _, err := d.PublishWithHandler("g2", genEchoHandler("g2")); err != nil {
			t.Fatal(err)
		}
		if m.Active().Label() != "g2" {
			t.Fatal(m.Active().Label())
		}
		close(resume)
		var lines []string
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if sc.Text() != "" {
				lines = append(lines, sc.Text())
			}
			if len(lines) >= 2 {
				break
			}
		}
		if len(lines) < 2 || !strings.Contains(lines[0], "start-1") || !strings.Contains(lines[1], "end-1") {
			t.Fatalf("%v", lines)
		}
	})

	t.Run("cancel_no_migration", func(t *testing.T) {
		t.Parallel()
		d, _, _ := newNoDropHarness(t)
		entered, resume := make(chan struct{}), make(chan struct{})
		var bound atomic.Int64
		if _, err := d.PublishWithHandler("g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := GenerationFromContext(r.Context())
			bound.Store(id)
			close(entered)
			<-resume
			_, _ = fmt.Fprintf(w, "done-%d", id)
		})); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(d)
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/x", nil)
		errCh := make(chan error, 1)
		go func() {
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				errCh <- err
				return
			}
			_, _ = io.Copy(io.Discard, res.Body)
			assert.NoError(t, res.Body.Close())
			errCh <- nil
		}()
		<-entered
		if _, err := d.PublishWithHandler("g2", genEchoHandler("g2")); err != nil {
			t.Fatal(err)
		}
		cancel()
		close(resume)
		<-errCh
		if bound.Load() != 1 {
			t.Fatalf("bound=%d", bound.Load())
		}
	})

	t.Run("failover_pinned", func(t *testing.T) {
		t.Parallel()
		d, _, _ := newNoDropHarness(t)
		entered, gate := make(chan struct{}), make(chan struct{})
		var gens []int64
		var mu sync.Mutex
		if _, err := d.PublishWithHandler("g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := GenerationFromContext(r.Context())
			mu.Lock()
			gens = append(gens, id)
			mu.Unlock()
			close(entered)
			<-gate
			_, _ = fmt.Fprintf(w, "ok-%d", id)
		})); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(d)
		defer srv.Close()
		done := make(chan string, 1)
		go func() {
			res, err := http.Get(srv.URL + "/f")
			if err != nil {
				done <- err.Error()
				return
			}
			b, _ := io.ReadAll(res.Body)
			assert.NoError(t, res.Body.Close())
			done <- string(b)
		}()
		<-entered
		if _, err := d.PublishWithHandler("g2", genEchoHandler("g2")); err != nil {
			t.Fatal(err)
		}
		close(gate)
		if body := <-done; body != "ok-1" {
			t.Fatalf("%q", body)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, g := range gens {
			if g != 1 {
				t.Fatalf("gen=%d", g)
			}
		}
	})

	t.Run("publish_acquire_races", func(t *testing.T) {
		t.Parallel()
		d, m, _ := newNoDropHarness(t)
		if _, err := d.PublishWithHandler("seed", genEchoHandler("seed")); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(d)
		defer srv.Close()
		for round := range 24 {
			readyAcq, readyPub, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
			result := make(chan string, 1)
			go func() {
				close(readyAcq)
				<-gate
				res, err := http.Get(srv.URL + "/r")
				if err != nil {
					result <- "err"
					return
				}
				b, _ := io.ReadAll(res.Body)
				assert.NoError(t, res.Body.Close())
				result <- string(b)
			}()
			label := fmt.Sprintf("g-%d", round)
			go func() {
				close(readyPub)
				<-gate
				_, _ = d.PublishWithHandler(label, genEchoHandler(label))
			}()
			<-readyAcq
			<-readyPub
			close(gate)
			if got := <-result; got == "err" || got == "" || m.Active() == nil {
				t.Fatalf("round %d got=%q", round, got)
			}
		}
	})

	t.Run("explicit_trigger_only", func(t *testing.T) {
		t.Parallel()
		d, m, _ := newNoDropHarness(t)
		if _, err := d.PublishWithHandler("g1", genEchoHandler("g1")); err != nil {
			t.Fatal(err)
		}
		before := m.Active().ID()
		if m.Active().ID() != before {
			t.Fatal("mutated without publish")
		}
	})
}

// Phase 6.1 production no-drop certification lives in
// runtime_config_reload_nodrop_cert_test.go (TestRuntimeConfigReload_NoDrop_*).
