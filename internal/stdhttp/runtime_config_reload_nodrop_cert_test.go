package stdhttp_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Phase 6.1 certification: production GenerationDispatcher + reload host no-drop
// evidence matching validation filter RuntimeConfigReload.*NoDrop|HTTP2|SSE|Failover|Parallel.

type certPlane struct{ h http.Handler }

func (p *certPlane) Handler() http.Handler         { return p.h }
func (p *certPlane) Close() error                  { return nil }
func (p *certPlane) Quiesce(context.Context) error { return nil }

func publishCertPlane(t *testing.T, m *runtimehost.Manager, label string, h http.Handler) *runtimehost.Generation {
	t.Helper()
	g := m.PrepareRequestPlane(label, &certPlane{h: h})
	if err := m.Publish(g); err != nil {
		t.Fatalf("Publish(%s): %v", label, err)
	}
	return g
}

func TestRuntimeConfigReload_NoDrop_HTTP1KeepAliveSSEFailoverParallel(t *testing.T) {
	t.Parallel()

	t.Run("http11_keepalive_stable_connection", func(t *testing.T) {
		t.Parallel()
		m := runtimehost.NewManager(8, nil)
		d := runtimehost.NewGenerationDispatcher(m)
		publishCertPlane(t, m, "g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, ok := runtimehost.BindingFromContext(r.Context())
			if !ok || b.Meta().ID != 1 {
				t.Errorf("gen1 binding ok=%v id=%v", ok, b)
			}
			_, _ = io.WriteString(w, "g1")
		}))
		srv := httptest.NewServer(d)
		t.Cleanup(srv.Close)
		var dials atomic.Int64
		tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}
		client := &http.Client{Transport: tr}
		t.Cleanup(tr.CloseIdleConnections)

		res1, err := client.Get(srv.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		b1, _ := io.ReadAll(res1.Body)
		require.NoError(t, res1.Body.Close())

		publishCertPlane(t, m, "g2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, ok := runtimehost.BindingFromContext(r.Context())
			if !ok || b.Meta().ID != 2 {
				t.Errorf("gen2 binding ok=%v id=%v", ok, b)
			}
			_, _ = io.WriteString(w, "g2")
		}))
		res2, err := client.Get(srv.URL + "/r")
		if err != nil {
			t.Fatal(err)
		}
		b2, _ := io.ReadAll(res2.Body)
		require.NoError(t, res2.Body.Close())
		if string(b1) != "g1" || string(b2) != "g2" || dials.Load() != 1 {
			t.Fatalf("b1=%s b2=%s dials=%d (listener/connection must stay stable)", b1, b2, dials.Load())
		}
	})

	t.Run("sse_old_stream_pinned_generation_1", func(t *testing.T) {
		t.Parallel()
		m := runtimehost.NewManager(8, nil)
		d := runtimehost.NewGenerationDispatcher(m)
		first, resume := make(chan struct{}), make(chan struct{})
		publishCertPlane(t, m, "g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := runtimehost.BindingFromContext(r.Context())
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "ResponseWriter does not implement http.Flusher", http.StatusInternalServerError)
				t.Error("ResponseWriter does not implement http.Flusher")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "data: start-%d\n\n", b.Meta().ID)
			flusher.Flush()
			close(first)
			<-resume
			_, _ = fmt.Fprintf(w, "data: end-%d\n\n", b.Meta().ID)
			flusher.Flush()
		}))
		srv := httptest.NewServer(d)
		t.Cleanup(srv.Close)
		res, err := http.Get(srv.URL + "/sse")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			require.NoError(t, res.Body.Close())
		}()
		<-first
		publishCertPlane(t, m, "g2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := runtimehost.BindingFromContext(r.Context())
			_, _ = fmt.Fprintf(w, "new-%d", b.Meta().ID)
		}))
		if m.Active().ID() != 2 {
			t.Fatalf("active=%d", m.Active().ID())
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
			t.Fatalf("sse mixed generations: %v", lines)
		}
		rr := httptest.NewRecorder()
		d.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/new", nil))
		if rr.Body.String() != "new-2" {
			t.Fatalf("new request=%q want new-2", rr.Body.String())
		}
	})

	t.Run("failover_pinned_no_mixed_generation", func(t *testing.T) {
		t.Parallel()
		m := runtimehost.NewManager(8, nil)
		d := runtimehost.NewGenerationDispatcher(m)
		entered, gate := make(chan struct{}), make(chan struct{})
		var gens []int64
		var mu sync.Mutex
		publishCertPlane(t, m, "g1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := runtimehost.BindingFromContext(r.Context())
			mu.Lock()
			gens = append(gens, b.Meta().ID)
			mu.Unlock()
			close(entered)
			<-gate
			_, _ = fmt.Fprintf(w, "ok-%d", b.Meta().ID)
		}))
		srv := httptest.NewServer(d)
		t.Cleanup(srv.Close)
		done := make(chan string, 1)
		go func() {
			res, err := http.Get(srv.URL + "/f")
			if err != nil {
				done <- err.Error()
				return
			}
			body, _ := io.ReadAll(res.Body)
			assert.NoError(t, res.Body.Close())
			done <- string(body)
		}()
		<-entered
		publishCertPlane(t, m, "g2", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "g2")
		}))
		close(gate)
		if body := <-done; body != "ok-1" {
			t.Fatalf("in-flight failover pin=%q", body)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, g := range gens {
			if g != 1 {
				t.Fatalf("mixed generation observed: %d", g)
			}
		}
	})

	t.Run("parallel_publish_acquire_races", func(t *testing.T) {
		t.Parallel()
		m := runtimehost.NewManager(64, nil)
		d := runtimehost.NewGenerationDispatcher(m)
		publishCertPlane(t, m, "seed", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "seed")
		}))
		srv := httptest.NewServer(d)
		t.Cleanup(srv.Close)
		for round := range 8 {
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
			publishDone := make(chan error, 1)
			go func() {
				close(readyPub)
				<-gate
				g := m.PrepareRequestPlane(label, &certPlane{h: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, label)
				})})
				publishDone <- m.Publish(g)
			}()
			<-readyAcq
			<-readyPub
			close(gate)
			got := <-result
			if err := <-publishDone; err != nil {
				t.Fatalf("round %d publish: %v", round, err)
			}
			if got == "err" || got == "" || m.Active() == nil {
				t.Fatalf("round %d got=%q", round, got)
			}
		}
	})
}

type recordingCertBLeg struct {
	canceled atomic.Bool
	closed   atomic.Bool
}

func (b *recordingCertBLeg) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, context.Canceled
}

func (b *recordingCertBLeg) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	b.canceled.Store(true)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (b *recordingCertBLeg) Close() error {
	b.closed.Store(true)
	return nil
}

func atomicWriteCertConfig(t *testing.T, path, body string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func dogfoodLocalStubBody(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(bpkit.WriteDogfoodLocalStubConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func localStubVersionBody(t *testing.T, base, version string) string {
	t.Helper()
	const original = `text: "[dogfood] local stub"`
	if !strings.Contains(base, original) {
		t.Fatalf("fixture missing %q", original)
	}
	return strings.Replace(base, original, `text: "`+version+`"`, 1)
}

func postNonStreamingResponses(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()
	body := `{"model":"stub-default","stream":false,"input":[{"role":"user","content":"ping"}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		require.NoError(t, res.Body.Close())
	}()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	return string(raw)
}

func TestRuntimeConfigReload_NoDrop_FullHost_ValidInvalidCancelALeg(t *testing.T) {
	t.Parallel()
	base := dogfoodLocalStubBody(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	atomicWriteCertConfig(t, path, localStubVersionBody(t, base, "generation-one"))

	ctx := context.Background()
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	if !host.Ready() || host.ExecutorView() == nil || host.HTTPHandler() == nil {
		t.Fatal("incomplete reload host")
	}
	if host.ActiveGenerationID() != 1 {
		t.Fatalf("startup generation=%d want 1", host.ActiveGenerationID())
	}

	srv := httptest.NewServer(host.HTTPHandler())
	t.Cleanup(srv.Close)
	var dials atomic.Int64
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	client := &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)

	body1 := postNonStreamingResponses(t, client, srv.URL)
	if !strings.Contains(body1, "generation-one") {
		t.Fatalf("startup body=%s", body1)
	}

	// Pin an in-flight Execute stream to generation 1, then publish generation 2.
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "dogfood-local:stub-default"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("pin-gen1")},
		}},
	}
	oldStream, err := host.ExecutorView().Execute(ctx, call)
	if err != nil {
		t.Fatalf("Execute gen1: %v", err)
	}
	t.Cleanup(func() { _ = oldStream.Close() })
	if call.Session.ALegID == "" {
		t.Fatal("missing A-leg id")
	}
	work := &recordingCertBLeg{}
	aLeg := host.StartALeg(call.Session.ALegID)
	if err := aLeg.RegisterBLeg(ctx, leglifecycle.BLegHandle{ID: "cert-b", Attempt: work}); err != nil {
		t.Fatalf("RegisterBLeg: %v", err)
	}

	atomicWriteCertConfig(t, path, localStubVersionBody(t, base, "generation-two"))
	pub := host.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "p6-cert"})
	if pub.Category != sdkreload.ResultPublished || pub.ActiveGeneration != 2 {
		t.Fatalf("valid reload=%+v", pub)
	}

	// Cancel before Collect so EndALeg from stream completion cannot drop the registered B-leg.
	if err := host.ExecutorView().CancelALeg(ctx, aipapi.ALegCancelRequest{
		ALegID: call.Session.ALegID,
		Reason: "p6-cert-cancel",
	}); err != nil {
		t.Fatalf("CancelALeg: %v", err)
	}
	if !work.canceled.Load() || !work.closed.Load() {
		t.Fatalf("old A-leg work not canceled/closed after publication cancel canceled=%v closed=%v", work.canceled.Load(), work.closed.Load())
	}

	oldCollected, err := aipapi.Collect(ctx, oldStream)
	if err != nil {
		// Cancel may surface on Collect; still require gen1 text when events were produced.
		if got := oldCollected.Text.String(); got != "" && got != "generation-one" {
			t.Fatalf("old stream mixed generations: %q err=%v", got, err)
		}
	} else if got := oldCollected.Text.String(); got != "generation-one" {
		t.Fatalf("old stream mixed generations: %q", got)
	}

	body2 := postNonStreamingResponses(t, client, srv.URL)
	if !strings.Contains(body2, "generation-two") || strings.Contains(body2, "generation-one") {
		t.Fatalf("new nonstreaming must be gen2: %s", body2)
	}
	if dials.Load() != 1 {
		t.Fatalf("keepalive dials=%d want 1", dials.Load())
	}

	// Invalid cycles retain last-good generation 2.
	atomicWriteCertConfig(t, path, "")
	empty := host.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "p6-cert"})
	if empty.Category != sdkreload.ResultSourceIntegrity && empty.Category != sdkreload.ResultInvalid {
		t.Fatalf("empty reload category=%q", empty.Category)
	}
	if host.ActiveGenerationID() != 2 {
		t.Fatalf("empty must retain gen2, active=%d", host.ActiveGenerationID())
	}

	atomicWriteCertConfig(t, path, "server: [\n")
	malformed := host.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "p6-cert"})
	if malformed.Category != sdkreload.ResultInvalid && malformed.Category != sdkreload.ResultSourceIntegrity {
		t.Fatalf("malformed reload category=%q", malformed.Category)
	}
	if host.ActiveGenerationID() != 2 {
		t.Fatalf("malformed must retain gen2, active=%d", host.ActiveGenerationID())
	}
	still := postNonStreamingResponses(t, client, srv.URL)
	if !strings.Contains(still, "generation-two") {
		t.Fatalf("last-good body after invalid=%s", still)
	}

	// Corrected atomic rename + explicit retrigger publishes generation 3.
	atomicWriteCertConfig(t, path, localStubVersionBody(t, base, "generation-three"))
	fixed := host.Reload(ctx, sdkreload.Trigger{Kind: sdkreload.TriggerAPI, SafeActor: "p6-cert"})
	if fixed.Category != sdkreload.ResultPublished || fixed.ActiveGeneration != 3 {
		t.Fatalf("corrected reload=%+v", fixed)
	}
	body3 := postNonStreamingResponses(t, client, srv.URL)
	if !strings.Contains(body3, "generation-three") {
		t.Fatalf("gen3 body=%s", body3)
	}
}
