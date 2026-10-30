//go:build integration

package runtimebundle_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func jsonBodyRestart(model string, input string, stream bool, meta map[string]string) string {
	m := map[string]any{"model": model, "input": input, "stream": stream}
	if len(meta) > 0 {
		m["metadata"] = meta
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type clockHolder struct{ t time.Time }

func TestSecureSessionE2E_sqliteRestart_resumeSurvivesProcessClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	contPath := filepath.Join(dir, "cont.db")
	ssPath := filepath.Join(dir, "ss.db")
	ch := &clockHolder{t: time.Unix(40000, 0).UTC()}
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "stub:gpt-4o-mini",
		},
		Plugins: testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{
			Store:      "sqlite",
			SQLitePath: contPath,
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			SQLitePath:          ssPath,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "best_effort",
			ResumeWindow:        "24h",
		},
	}
	buildOpts := func() *runtimebundle.BuildOptions {
		return &runtimebundle.BuildOptions{
			PluginRegistry: pluginreg.NewRegistry(),
			Testing:        runtimebundle.TestingOptions{Clock: func() time.Time { return ch.t }},
		}
	}

	_, b1 := mustProcessAndCandidate(t, cfg, buildOpts())
	injectStubBackend(t, b1)
	s1, sid, tok, aleg := runCreatePhase(t, b1)
	s1.Close()
	closeBuilt(t, b1)

	ch.t = ch.t.Add(2 * time.Minute)
	_, b2 := mustProcessAndCandidate(t, cfg, buildOpts())
	injectStubBackend(t, b2)
	s2 := runResumePhase(t, b2, sid, tok, aleg)
	s2.Close()
	closeBuilt(t, b2)

	ch.t = ch.t.Add(2 * time.Minute)
	_, b3 := mustProcessAndCandidate(t, cfg, buildOpts())
	injectStubBackend(t, b3)
	s3 := runWrongOwnerPhase(t, b3, sid, tok)
	s3.Close()
	closeBuilt(t, b3)
}

func runCreatePhase(t *testing.T, b *runtimebundle.CandidateHTTPCompile) (*httptest.Server, string, string, string) {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor(), DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "restart-owner"))
	srv := httptest.NewServer(mux)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "boot", false, nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("create status %d body=%s", resp.StatusCode, bb)
	}
	sid := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	tok := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderResumeToken))
	_, _ = io.ReadAll(resp.Body)
	if sid == "" || tok == "" {
		srv.Close()
		t.Fatalf("missing carriers sid=%q tok=%q", sid, tok)
	}
	rec, err := runtimebundle.CandidateSecureSessionStore(b).LoadByID(context.Background(), domain.SessionID(sid))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	if rec.Owner.ID != "restart-owner" {
		srv.Close()
		t.Fatalf("owner: want restart-owner got %q", rec.Owner.ID)
	}
	return srv, sid, tok, rec.ALegID
}

func runResumePhase(t *testing.T, b *runtimebundle.CandidateHTTPCompile, sid, tok, wantALeg string) *httptest.Server {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor(), DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "restart-owner"))
	srv := httptest.NewServer(mux)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "resume", false, meta)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("resume status %d body=%s", resp.StatusCode, bb)
	}
	_, _ = io.ReadAll(resp.Body)
	rec, err := runtimebundle.CandidateSecureSessionStore(b).LoadByID(context.Background(), domain.SessionID(sid))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	if rec.ALegID != wantALeg {
		srv.Close()
		t.Fatalf("A-leg after restart: want %q got %q", wantALeg, rec.ALegID)
	}
	return srv
}

func runWrongOwnerPhase(t *testing.T, b *runtimebundle.CandidateHTTPCompile, sid, tok string) *httptest.Server {
	t.Helper()
	h := &openairesponses.Handler{Exec: b.Executor(), DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipalRestart(h, "other-owner"))
	srv := httptest.NewServer(mux)
	meta := map[string]string{
		sessionwire.MetaKeyAuthoritativeSessionID: sid,
		sessionwire.MetaKeyResumeToken:            tok,
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(jsonBodyRestart("gpt-4o-mini", "bad", false, meta)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		bb, _ := io.ReadAll(resp.Body)
		srv.Close()
		t.Fatalf("wrong owner want 400 got %d body=%s", resp.StatusCode, bb)
	}
	_, _ = io.ReadAll(resp.Body)
	return srv
}

func closeBuilt(t *testing.T, b *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if b == nil {
		return
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func injectStubBackend(t *testing.T, b *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if b.Executor().Backends == nil {
		b.Executor().Backends = map[string]execbackend.Backend{}
	}
	b.Executor().Backends["stub"] = execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			_ = ctx
			_ = call
			_ = cand
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "sqlite-e2e"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func withPrincipalRestart(h http.Handler, pid string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := execview.WithPrincipal(r.Context(), execview.PrincipalView{ID: pid})
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
