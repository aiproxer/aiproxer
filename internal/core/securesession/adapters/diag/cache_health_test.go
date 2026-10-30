package diag_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

type stubCacheHealthReader struct {
	rows map[string]diag.SessionCacheHealth
}

func (s stubCacheHealthReader) SessionCacheHealth(sessionID string) diag.SessionCacheHealth {
	if h, ok := s.rows[sessionID]; ok {
		return h
	}
	return diag.SessionCacheHealth{Status: diag.CacheHealthUnknown}
}

func testDiagHandlerWithHealth(t *testing.T, st *memory.Store, r diag.CacheHealthReader) http.Handler {
	t.Helper()
	h, err := diag.NewHandlerWithCacheHealth("/debug/sessions", st, "standard", nil, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})), r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func seedHealthSession(ctx context.Context, t *testing.T) *memory.Store {
	t.Helper()
	st := memory.New(memory.Options{})
	seedSession(ctx, t, st, "sess-a", "alice", "aleg-1", domain.PolicyMetadata{
		TranscriptEnabled: true, PolicyVersion: "pv1", RedactionProfile: "standard", AuditMode: "best_effort",
	})
	return st
}

func TestHandler_cacheHealthDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := seedHealthSession(ctx, t)
	h := testDiagHandlerWithHealth(t, st, stubCacheHealthReader{rows: map[string]diag.SessionCacheHealth{
		"sess-a": {Status: diag.CacheHealthCollapsed, Stale: true},
	}})

	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-a", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	health, ok := env.Session["cache_health"].(map[string]any)
	if !ok {
		t.Fatalf("missing cache_health in %#v", env.Session)
	}
	if health["status"] != "collapsed" || health["stale"] != true {
		t.Fatalf("cache_health=%#v want collapsed stale", health)
	}
}

func TestHandler_cacheHealthList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := seedHealthSession(ctx, t)
	h := testDiagHandlerWithHealth(t, st, stubCacheHealthReader{rows: map[string]diag.SessionCacheHealth{
		"sess-a": {Status: diag.CacheHealthHealthy},
	}})

	req := httptest.NewRequest(http.MethodGet, "/debug/sessions?owner=alice", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Sessions) != 1 {
		t.Fatalf("sessions=%d", len(env.Sessions))
	}
	health, ok := env.Sessions[0]["cache_health"].(map[string]any)
	if !ok {
		t.Fatalf("missing cache_health in %#v", env.Sessions[0])
	}
	if health["status"] != "healthy" || health["stale"] != false {
		t.Fatalf("cache_health=%#v want healthy fresh", health)
	}
}

func TestHandler_cacheHealthUnknownWhenUnobserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := seedHealthSession(ctx, t)
	h := testDiagHandlerWithHealth(t, st, stubCacheHealthReader{})

	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-a", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var env struct {
		Session map[string]any `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	health, ok := env.Session["cache_health"].(map[string]any)
	if !ok || health["status"] != "unknown" {
		t.Fatalf("cache_health=%#v want unknown", env.Session["cache_health"])
	}
}

func TestHandler_cacheHealthDeniedScopeLeaksNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := seedHealthSession(ctx, t)
	h := testDiagHandlerWithHealth(t, st, stubCacheHealthReader{rows: map[string]diag.SessionCacheHealth{
		"sess-a": {Status: diag.CacheHealthCollapsed},
	}})

	req := httptest.NewRequest(http.MethodGet, "/debug/sessions/sess-a", nil)
	req.Header.Set(diag.HeaderOwnerScope, "mallory")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "cache_health") {
		t.Fatalf("denied response leaks health: %s", rec.Body.String())
	}
}

func TestHandler_cacheHealthAbsentWithoutReader(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := seedHealthSession(ctx, t)
	h := testDiagHandler(t, st)

	for _, path := range []string{"/debug/sessions/sess-a", "/debug/sessions?owner=alice"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "cache_health") {
			t.Fatalf("%s: unwired handler must not emit cache_health: %s", path, rec.Body.String())
		}
	}
}
