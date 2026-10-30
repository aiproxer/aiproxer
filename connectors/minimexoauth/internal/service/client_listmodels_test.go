package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newListModelsTestServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_ListModels_DiscoveryAuthoritative(t *testing.T) {
	t.Parallel()

	srv := newListModelsTestServer(t, `{"data":[{"id":"MiniMax-M2.7"},{"id":"MiniMax-M3"}]}`, http.StatusOK)
	c := &Client{
		Config:        Config{InferenceBaseURL: srv.URL},
		TokenProvider: &StaticTokenProvider{AccessToken: "test-token"},
		HTTPClient:    srv.Client(),
	}

	resp, err := c.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("Models len = %d, want exactly 2 enumerated models (no static union)", len(resp.Models))
	}
	seen := map[string]bool{}
	for _, m := range resp.Models {
		seen[m.CanonicalModelID] = true
		if !strings.HasPrefix(m.CanonicalModelID, FactoryKind+"/") {
			t.Fatalf("CanonicalModelID %q missing %q prefix", m.CanonicalModelID, FactoryKind)
		}
	}
	if !seen[FactoryKind+"/MiniMax-M2.7"] || !seen[FactoryKind+"/MiniMax-M3"] {
		t.Fatalf("enumerated models missing, got %+v", resp.Models)
	}
	if seen[FactoryKind+"/"+ModelM27Highspeed] {
		t.Fatalf("non-enumerated static model %q must not be advertised, got %+v", ModelM27Highspeed, resp.Models)
	}
	if resp.InventorySource != FactoryKind {
		t.Fatalf("InventorySource = %q, want %q", resp.InventorySource, FactoryKind)
	}
	if resp.FetchedUnixMS <= 0 {
		t.Fatalf("FetchedUnixMS = %d, want positive", resp.FetchedUnixMS)
	}
}

func TestClient_ListModels_LimitApplied(t *testing.T) {
	t.Parallel()

	srv := newListModelsTestServer(t, `{"data":[{"id":"MiniMax-M2.7"},{"id":"MiniMax-M3"}]}`, http.StatusOK)
	c := &Client{
		Config:        Config{InferenceBaseURL: srv.URL},
		TokenProvider: &StaticTokenProvider{AccessToken: "test-token"},
		HTTPClient:    srv.Client(),
	}

	resp, err := c.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("Models len = %d, want 1 after limit", len(resp.Models))
	}
}

func TestClient_ListModels_EmptySuccessReturnsEmpty(t *testing.T) {
	t.Parallel()

	srv := newListModelsTestServer(t, `{"data":[]}`, http.StatusOK)
	c := &Client{
		Config:        Config{InferenceBaseURL: srv.URL},
		TokenProvider: &StaticTokenProvider{AccessToken: "test-token"},
		HTTPClient:    srv.Client(),
	}

	resp, err := c.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 0 {
		t.Fatalf("Models len = %d, want 0 for successful empty enumeration (no static resurrection)", len(resp.Models))
	}
}

func TestClient_ListModels_NilTokenProviderFails(t *testing.T) {
	t.Parallel()

	c := &Client{Config: Config{InferenceBaseURL: "http://127.0.0.1"}}
	_, err := c.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error when token provider is not configured, got nil")
	}
}

func TestClient_ListModels_TokenErrorFailsClosed(t *testing.T) {
	t.Parallel()

	c := &Client{
		Config:        Config{InferenceBaseURL: "http://127.0.0.1"},
		TokenProvider: &StaticTokenProvider{},
	}
	_, err := c.ListModels(context.Background(), 0)
	if err == nil {
		t.Fatal("expected token error, got nil")
	}
}

func TestClient_ListModels_ContextCanceledPreserved(t *testing.T) {
	t.Parallel()

	srv := newListModelsTestServer(t, `{"data":[{"id":"MiniMax-M2.7"}]}`, http.StatusOK)
	c := &Client{
		Config:        Config{InferenceBaseURL: srv.URL},
		TokenProvider: &StaticTokenProvider{AccessToken: "test-token"},
		HTTPClient:    srv.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.ListModels(ctx, 0)
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
}

func TestClient_ListModels_FetchedTimestampRecent(t *testing.T) {
	t.Parallel()

	before := time.Now().UnixMilli()
	srv := newListModelsTestServer(t, `{"data":[{"id":"MiniMax-M2.7"}]}`, http.StatusOK)
	c := &Client{
		Config:        Config{InferenceBaseURL: srv.URL},
		TokenProvider: &StaticTokenProvider{AccessToken: "test-token"},
		HTTPClient:    srv.Client(),
	}

	resp, err := c.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if resp.FetchedUnixMS < before || resp.FetchedUnixMS > time.Now().UnixMilli()+1000 {
		t.Fatalf("FetchedUnixMS = %d, want near %d", resp.FetchedUnixMS, before)
	}
}
