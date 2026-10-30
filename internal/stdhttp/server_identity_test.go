package stdhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/holdalive"
)

func TestResolveDownstreamServerPolicy_table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		cfg        *config.Config
		wantMode   identity.Mode
		wantValue  string
		wantServer string
	}{
		{
			name:       "ID-050_nil_config_defaults_proxy_literal",
			cfg:        nil,
			wantMode:   identity.ModeProxy,
			wantServer: "aiproxer",
		},
		{
			name:       "zero_identity_defaults_proxy",
			cfg:        &config.Config{},
			wantMode:   identity.ModeProxy,
			wantServer: "aiproxer",
		},
		{
			name: "custom_exact",
			cfg: &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "Example Gateway"},
				},
			}},
			wantMode:   identity.ModeCustom,
			wantValue:  "Example Gateway",
			wantServer: "Example Gateway",
		},
		{
			name: "drop",
			cfg: &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.ModeDrop},
				},
			}},
			wantMode: identity.ModeDrop,
		},
		{
			name: "proxy_explicit",
			cfg: &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.ModeProxy},
				},
			}},
			wantMode:   identity.ModeProxy,
			wantServer: "aiproxer",
		},
		{
			name: "unknown_mode_preserved_for_fail_safe_omit",
			cfg: &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: identity.Mode("bogus")},
				},
			}},
			wantMode: identity.Mode("bogus"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var before identity.Config
			if tc.cfg != nil {
				before = tc.cfg.Identity
			}
			got := resolveDownstreamServerPolicy(tc.cfg)
			if got.Mode != tc.wantMode {
				t.Fatalf("mode=%q want %q", got.Mode, tc.wantMode)
			}
			if got.Value != tc.wantValue {
				t.Fatalf("value=%q want %q", got.Value, tc.wantValue)
			}
			if resolved := got.ResolvedValue("aiproxer"); resolved != tc.wantServer {
				t.Fatalf("resolved=%q want %q", resolved, tc.wantServer)
			}
			if tc.cfg != nil && tc.cfg.Identity != before {
				t.Fatalf("config mutated: before=%+v after=%+v", before, tc.cfg.Identity)
			}
		})
	}
}

func TestApplyDownstreamServerHeader_modes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		policy      identity.FieldPolicy
		preexist    string
		wantPresent bool
		wantValue   string
	}{
		{
			name:        "proxy_literal",
			policy:      identity.FieldPolicy{Mode: identity.ModeProxy},
			wantPresent: true,
			wantValue:   "aiproxer",
		},
		{
			name:        "custom_exact",
			policy:      identity.FieldPolicy{Mode: identity.ModeCustom, Value: "DiagGW/1"},
			wantPresent: true,
			wantValue:   "DiagGW/1",
		},
		{
			name:        "drop_absent_key",
			policy:      identity.FieldPolicy{Mode: identity.ModeDrop},
			preexist:    "inner-set",
			wantPresent: false,
		},
		{
			name:        "proxy_replaces_preexisting",
			policy:      identity.FieldPolicy{Mode: identity.ModeProxy},
			preexist:    "inner-set",
			wantPresent: true,
			wantValue:   "aiproxer",
		},
		{
			name:        "unknown_mode_omits_fail_safe",
			policy:      identity.FieldPolicy{Mode: identity.Mode("not-a-mode")},
			preexist:    "should-clear",
			wantPresent: false,
		},
		{
			name:        "passthrough_omits_fail_safe_on_server",
			policy:      identity.FieldPolicy{Mode: identity.ModePassthrough},
			preexist:    "should-clear",
			wantPresent: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			if tc.preexist != "" {
				rec.Header().Set("Server", tc.preexist)
			}
			applyDownstreamServerHeader(rec, tc.policy)
			_, present := rec.Result().Header["Server"]
			if present != tc.wantPresent {
				t.Fatalf("Server present=%v want %v (Get=%q)", present, tc.wantPresent, rec.Header().Get("Server"))
			}
			if tc.wantPresent && rec.Header().Get("Server") != tc.wantValue {
				t.Fatalf("Server=%q want %q", rec.Header().Get("Server"), tc.wantValue)
			}
		})
	}
}

func TestDownstreamServerMiddleware_setsBeforeHandlerAndPreservesFlusher(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Identity: identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "MidGW"},
		},
	}}
	underlying := httptest.NewRecorder()
	var sawAtEntry string
	var flusherOK bool
	var flushCalled bool
	var unwrapped bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sawAtEntry = w.Header().Get("Server")
		if _, ok := w.(http.Flusher); ok {
			flusherOK = true
		}
		if u, ok := w.(interface{ Unwrap() http.ResponseWriter }); ok {
			unwrapped = u.Unwrap() == underlying || u.Unwrap() != nil
		}
		w.Header().Set("X-Session", "keep")
		w.WriteHeader(http.StatusNoContent)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
			flushCalled = true
		}
	})
	h := DownstreamServerMiddleware(cfg, inner)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Server", "client-spoof")
	h.ServeHTTP(underlying, req)
	if sawAtEntry != "MidGW" {
		t.Fatalf("Server at handler entry=%q want MidGW (must set before next)", sawAtEntry)
	}
	if !flusherOK {
		t.Fatal("ResponseWriter must remain http.Flusher through policy wrapper")
	}
	if !flushCalled {
		t.Fatal("Flush must forward through policy wrapper")
	}
	if !unwrapped {
		t.Fatal("policy wrapper must expose Unwrap() for ResponseController")
	}
	if underlying.Header().Get("Server") != "MidGW" {
		t.Fatalf("response Server=%q", underlying.Header().Get("Server"))
	}
	if underlying.Header().Get("X-Session") != "keep" {
		t.Fatal("non-identity headers must be unchanged")
	}
	if underlying.Code != http.StatusNoContent {
		t.Fatalf("status=%d", underlying.Code)
	}
}

func TestDownstreamServerMiddleware_dropClearsInnerServerBeforeCommit(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Identity: identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeDrop},
		},
	}}
	h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "inner-leak")
		w.Header().Set("Content-Type", "text/plain")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if _, present := rec.Result().Header["Server"]; present {
		t.Fatalf("drop must omit Server map key, Get=%q", rec.Header().Get("Server"))
	}
	if rec.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("Content-Type=%q", rec.Header().Get("Content-Type"))
	}
}

func TestDownstreamServerMiddleware_commitTimeEnforcesPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mode        identity.Mode
		custom      string
		commit      string // writeheader | write | flush
		wantPresent bool
		wantValue   string
	}{
		{name: "drop_after_WriteHeader", mode: identity.ModeDrop, commit: "writeheader", wantPresent: false},
		{name: "drop_after_implicit_Write", mode: identity.ModeDrop, commit: "write", wantPresent: false},
		{name: "drop_after_Flush", mode: identity.ModeDrop, commit: "flush", wantPresent: false},
		{name: "proxy_overrides_WriteHeader", mode: identity.ModeProxy, commit: "writeheader", wantPresent: true, wantValue: "aiproxer"},
		{name: "custom_overrides_Write", mode: identity.ModeCustom, custom: "CommitGW", commit: "write", wantPresent: true, wantValue: "CommitGW"},
		{name: "unknown_omits_on_WriteHeader", mode: identity.Mode("weird"), commit: "writeheader", wantPresent: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: tc.mode, Value: tc.custom},
				},
			}}
			h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Server", "inner-conflict")
				w.Header().Set("Content-Type", "text/plain")
				switch tc.commit {
				case "writeheader":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("ok"))
				case "write":
					_, _ = w.Write([]byte("ok"))
				case "flush":
					w.WriteHeader(http.StatusOK)
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					_, _ = w.Write([]byte("ok"))
				default:
					t.Fatalf("unknown commit %q", tc.commit)
				}
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			assertServerHeader(t, rec.Result().Header, tc.wantPresent, tc.wantValue)
			if got := rec.Body.String(); got != "ok" {
				t.Fatalf("body=%q want ok", got)
			}
		})
	}
}

func TestDownstreamServerMiddleware_realHTTPServer_commitTimePolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mode        identity.Mode
		custom      string
		commit      string
		wantPresent bool
		wantValue   string
	}{
		{name: "proxy_WriteHeader", mode: identity.ModeProxy, commit: "writeheader", wantPresent: true, wantValue: "aiproxer"},
		{name: "custom_Write", mode: identity.ModeCustom, custom: "RealGW", commit: "write", wantPresent: true, wantValue: "RealGW"},
		{name: "drop_WriteHeader", mode: identity.ModeDrop, commit: "writeheader", wantPresent: false},
		{name: "drop_Write", mode: identity.ModeDrop, commit: "write", wantPresent: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Identity: identity.Config{
				Downstream: identity.DownstreamPolicy{
					Server: identity.FieldPolicy{Mode: tc.mode, Value: tc.custom},
				},
			}}
			h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Server", "inner-conflict")
				w.Header().Set("Content-Type", "text/plain")
				switch tc.commit {
				case "writeheader":
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "ok")
				case "write":
					_, _ = io.WriteString(w, "ok")
				}
			}))
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			res, err := http.Get(srv.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status=%d", res.StatusCode)
			}
			if string(body) != "ok" {
				t.Fatalf("body=%q", body)
			}
			assertServerHeader(t, res.Header, tc.wantPresent, tc.wantValue)
		})
	}
}

func TestDownstreamServerMiddleware_nilConfigProxyLiteral(t *testing.T) {
	t.Parallel()
	h := DownstreamServerMiddleware(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Server"); got != "aiproxer" {
		t.Fatalf("Server=%q want aiproxer", got)
	}
}

func TestDownstreamServerMiddleware_unknownModeOmitsOnLiveServer(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Identity: identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.Mode("skipped-validation")},
		},
	}}
	h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "should-not-win")
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	assertServerHeader(t, res.Header, false, "")
}

func TestDownstreamServerMiddleware_102HoldaliveSeesServerHeader(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Identity: identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "HoldGW"},
		},
	}}
	gate := make(chan struct{})
	w := &headerAtStatusWriter{hdr: make(http.Header), on102: func() { close(gate) }}
	h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, err := holdalive.Wait(context.Background(), rw, holdalive.Config{
			Enabled:  true,
			Interval: time.Millisecond,
		}, func(context.Context) (string, error) {
			return waitReleaseGate(gate)
		}, nil)
		if err != nil {
			t.Errorf("wait: %v", err)
		}
		rw.Header().Set("Server", "inner-after-102")
		rw.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.at102 == nil {
		t.Fatalf("expected 102 Processing; statuses=%v", w.wrote)
	}
	if got := w.at102.Get("Server"); got != "HoldGW" {
		t.Fatalf("Server on 102=%q want HoldGW", got)
	}
	if w.final != http.StatusOK {
		t.Fatalf("final status=%d", w.final)
	}
	if got := w.atFinal.Get("Server"); got != "HoldGW" {
		t.Fatalf("Server on final=%q want HoldGW (commit-time policy)", got)
	}
	if w.flushN != 0 {
		t.Fatal("Flush after 102 would commit a final 200")
	}
}

func TestDownstreamServerMiddleware_102Holdalive_realServer_httptrace(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Identity: identity.Config{
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "TraceGW"},
		},
	}}
	gate := make(chan struct{})
	h := DownstreamServerMiddleware(cfg, http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		release := &gateOn102Writer{ResponseWriter: rw, gate: gate}
		_, err := holdalive.Wait(context.Background(), release, holdalive.Config{
			Enabled:  true,
			Interval: time.Millisecond,
		}, func(context.Context) (string, error) {
			return waitReleaseGate(gate)
		}, nil)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.Header().Set("Server", "inner-leak")
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(rw, "done")
	}))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var saw1xx bool
	var serverOn1xx string
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, hdr textproto.MIMEHeader) error {
			if code == http.StatusProcessing {
				saw1xx = true
				serverOn1xx = hdr.Get("Server")
			}
			return nil
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if string(body) != "done" {
		t.Fatalf("body=%q", body)
	}
	assertServerHeader(t, res.Header, true, "TraceGW")
	if !saw1xx {
		t.Log("httptrace Got1xxResponse not observed on this platform/transport; final Server asserted")
		return
	}
	if serverOn1xx != "TraceGW" {
		t.Fatalf("Server on 102=%q want TraceGW", serverOn1xx)
	}
}

func waitReleaseGate(gate <-chan struct{}) (string, error) {
	select {
	case <-gate:
		return "done", nil
	case <-time.After(5 * time.Second):
		return "", errors.New("timed out waiting for 102 release gate")
	}
}

// headerAtStatusWriter captures Header clones when WriteHeader is called.
type headerAtStatusWriter struct {
	hdr     http.Header
	at102   http.Header
	atFinal http.Header
	final   int
	wrote   []int
	flushN  int
	on102   func()
}

func (w *headerAtStatusWriter) Header() http.Header { return w.hdr }
func (w *headerAtStatusWriter) Write(b []byte) (int, error) {
	if w.final == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return len(b), nil
}

func (w *headerAtStatusWriter) WriteHeader(code int) {
	w.wrote = append(w.wrote, code)
	clone := w.hdr.Clone()
	if code == http.StatusProcessing && w.at102 == nil {
		w.at102 = clone
		if w.on102 != nil {
			w.on102()
		}
	}
	if code >= 200 {
		w.final = code
		w.atFinal = clone
	}
}
func (w *headerAtStatusWriter) Flush() { w.flushN++ }

// gateOn102Writer closes gate once on the first HTTP 102 WriteHeader so holdalive
// callbacks can unblock without wall-clock sleeps.
type gateOn102Writer struct {
	http.ResponseWriter
	gate chan struct{}
	once sync.Once
}

func (w *gateOn102Writer) WriteHeader(code int) {
	if code == http.StatusProcessing {
		w.once.Do(func() { close(w.gate) })
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gateOn102Writer) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gateOn102Writer) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
