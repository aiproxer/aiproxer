package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func newTestQueryService(t *testing.T, enabled bool) (*controlplane.QueryService, *controlplane.Status, controlplane.Store) {
	t.Helper()
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	queries := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         enabled,
		DefaultPageSize: 100,
		MaxPageSize:     500,
	})
	return queries, status, store
}

func seedSessionEvent(t *testing.T, store controlplane.Store) {
	t.Helper()
	ev := cp.Event{
		Category:       cp.CategorySession,
		OccurredAt:     time.Now().UTC(),
		RecordedAt:     time.Now().UTC(),
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Correlation:    cp.Correlation{SessionID: "sess-1", TraceID: "tr-1"},
		Summary:        "session started",
		Detail: &cp.SessionDetail{
			Action:    cp.SessionActionCreated,
			Certainty: "known",
		},
	}
	if _, err := store.Append(context.Background(), ev); err != nil {
		t.Fatalf("seed append: %v", err)
	}
}

func TestHandler_Status_ReturnsCapabilityStatus(t *testing.T) {
	t.Parallel()
	queries, _, _ := newTestQueryService(t, true)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var status cp.CapabilityStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != cp.CapabilityReady {
		t.Fatalf("status state: got %q, want ready", status.State)
	}
}

func TestHandler_Sessions_ReturnsBoundedPage(t *testing.T) {
	t.Parallel()
	queries, _, store := newTestQueryService(t, true)
	seedSessionEvent(t, store)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions: got %d, want 200", resp.StatusCode)
	}
	var page cp.Page[cp.SessionSummary]
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("sessions: expected at least one row")
	}
}

func TestHandler_DisabledQueries_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	queries, _, _ := newTestQueryService(t, false)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled: got %d, want 404", resp.StatusCode)
	}
}

func TestHandler_NilQueries_ReturnsNotFound(t *testing.T) {
	t.Parallel()
	h := NewHandler(Options{Queries: nil})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("nil queries: got %d, want 404", resp.StatusCode)
	}
}

func TestHandler_TooBroadLimit_ReturnsTooBroad(t *testing.T) {
	t.Parallel()
	queries, _, _ := newTestQueryService(t, true)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/sessions?limit=99999")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("too broad: got %d, want 400", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != string(cp.ErrCodeTooBroad) {
		t.Fatalf("too broad error: got %v, want %q", body["error"], cp.ErrCodeTooBroad)
	}
}

func TestHandler_InvalidCursor_ReturnsInvalidQuery(t *testing.T) {
	t.Parallel()
	queries, _, _ := newTestQueryService(t, true)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/events?cursor=!!!notbase64!!!")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid cursor: got %d, want 400", resp.StatusCode)
	}
}

func TestHandler_PostMethod_ReturnsMethodNotAllowed(t *testing.T) {
	t.Parallel()
	queries, _, _ := newTestQueryService(t, true)
	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/status", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post /status: got %d, want 405", resp.StatusCode)
	}
}
