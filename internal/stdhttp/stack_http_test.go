package stdhttp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestStackHTTPHandler_recoveredPanicThenOK_metricsObserves5xx(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	hm := metrics.RegisterHTTPMetrics(reg, false)
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("do not leak") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: false},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{}, TraceGen: diag.NewTraceIDGenerator(), Inner: mux, HTTPProm: hm,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panic path status=%d want 500", res.StatusCode)
	}
	res2, err := http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	if err := res2.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("ok path status=%d want 204", res2.StatusCode)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	s := dump.String()
	// TextProto metric dump uses label { name:"status_class" value:"5xx" } for 5xx-class responses.
	if !strings.Contains(s, `value:"5xx"`) {
		t.Fatalf("expected 5xx status class in http metrics after recovered panic, got:\n%s", s)
	}
}

func TestStackHTTPHandler_recoveredPanic_accessLogRecords5xx(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("do not leak") })
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: true},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Security: HTTPSecurityInput{}, TraceGen: diag.NewTraceIDGenerator(), Inner: mux, HTTPProm: nil,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", res.StatusCode)
	}
	var found bool
	scan := bufio.NewScanner(&logBuf)
	for scan.Scan() {
		line := scan.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line: %s", err)
		}
		if m["msg"] != "http.access" {
			continue
		}
		// JSON decoder unmarshals numbers as float64
		if st, ok := m["status"].(float64); ok && st == 500 {
			found = true
			break
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected access log with status 500, got %q", logBuf.String())
	}
}

// TestStackHTTPHandler_recoveredPanic_combinedMetricsAccessAndSafeBody covers Req 1.3 / 5.x
// integration: metrics + access log + safe body on the same handler stack as production.
func TestStackHTTPHandler_recoveredPanic_combinedMetricsAccessAndSafeBody(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	hm := metrics.RegisterHTTPMetrics(reg, false)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("do not leak") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: true},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Security: HTTPSecurityInput{}, TraceGen: diag.NewTraceIDGenerator(), Inner: mux, HTTPProm: hm,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/panic")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("panic path status=%d want 500", res.StatusCode)
	}
	if strings.Contains(string(body), "do not leak") {
		t.Fatalf("response body leaked panic value: %q", body)
	}
	if !strings.Contains(string(body), "internal error") {
		t.Fatalf("body=%q want internal error text", body)
	}

	res2, err := http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	if err := res2.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("ok path status=%d want 204 (unrelated request after isolated failure)", res2.StatusCode)
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var metricDump strings.Builder
	for _, mf := range mfs {
		metricDump.WriteString(mf.String())
	}
	md := metricDump.String()
	if !strings.Contains(md, `value:"5xx"`) {
		t.Fatalf("expected 5xx status class in http metrics after recovered panic, got:\n%s", md)
	}

	logDump := logBuf.String()
	var access500 bool
	scan := bufio.NewScanner(strings.NewReader(logDump))
	for scan.Scan() {
		line := scan.Bytes()
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line: %s", err)
		}
		if m["msg"] != "http.access" {
			continue
		}
		if st, ok := m["status"].(float64); ok && st == 500 {
			access500 = true
			break
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if !access500 {
		t.Fatalf("expected access log with status 500, got %q", logDump)
	}

	logStr := logDump
	if !strings.Contains(logStr, `"operation":"http_handler"`) {
		t.Fatalf("expected inner isolated panic log with operation=http_handler, got %q", logStr)
	}
	if strings.Contains(logStr, `"operation":"http_outer_handler"`) {
		t.Fatalf("did not expect outer handler operation for inner mux panic, got %q", logStr)
	}
	if strings.Contains(logStr, `"panic_message"`) {
		t.Fatalf("must not log panic_message, got %q", logStr)
	}
}

// TestStackHTTPHandler_validation400_notLoggedAsIsolatedPanic confirms client validation / 4xx
// paths do not emit request-handler isolated-panic diagnostics and classify as 4xx in metrics.
func TestStackHTTPHandler_validation400_notLoggedAsIsolatedPanic(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	hm := metrics.RegisterHTTPMetrics(reg, false)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mux := http.NewServeMux()
	mux.HandleFunc("/bad", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: true},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: log, Security: HTTPSecurityInput{}, TraceGen: diag.NewTraceIDGenerator(), Inner: mux, HTTPProm: hm,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", res.StatusCode)
	}

	logStr := logBuf.String()
	if strings.Contains(logStr, "stdhttp: isolated panic in request handler") {
		t.Fatalf("did not expect isolated request-handler panic log for normal 400, got %q", logStr)
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	for _, mf := range mfs {
		dump.WriteString(mf.String())
	}
	s := dump.String()
	if !strings.Contains(s, `value:"4xx"`) {
		t.Fatalf("expected 4xx status class in http metrics for 400 response, got:\n%s", s)
	}
}
