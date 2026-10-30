package modelcatalog

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRedactSourceURL_stripsUserinfo(t *testing.T) {
	t.Parallel()
	raw := "https://alice:hunter2@models.example.com/v1/models.json?x=1"
	got := RedactSourceURL(raw)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "alice:") {
		t.Fatalf("redaction leaked credentials: %q", got)
	}
	if !strings.Contains(got, "models.example.com") {
		t.Fatalf("host missing: %q", got)
	}
}

func TestRedactSourceURL_stripsQueryFragmentAndUserinfo(t *testing.T) {
	t.Parallel()
	got := RedactSourceURL("https://user:secret@example.com/models.json?api_key=hidden#token")
	if got != "https://example.com/models.json" {
		t.Fatalf("redacted URL = %q", got)
	}
}

func TestBuildCatalogDiagnosticsJSON_usageDisabled(t *testing.T) {
	t.Parallel()
	v := BuildCatalogDiagnosticsJSON(CatalogStatusHandlerConfig{UsageEnabled: false})
	if v.Status != CatalogDiagDisabled || v.UsageEnabled {
		t.Fatalf("got %#v", v)
	}
}

func TestBuildCatalogDiagnosticsJSON_unavailable(t *testing.T) {
	t.Parallel()
	rt := NewCatalogRuntime(RuntimeConfig{})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	v := BuildCatalogDiagnosticsJSON(CatalogStatusHandlerConfig{Runtime: rt, UsageEnabled: true, Now: time.Now})
	if v.Status != CatalogDiagUnavailable {
		t.Fatalf("got %#v", v)
	}
}

func TestBuildCatalogDiagnosticsJSON_staleByAge(t *testing.T) {
	t.Parallel()
	old := time.Unix(1_000_000, 0)
	now := old.Add(3 * time.Hour)
	s := Snapshot{
		Generation:  "g1",
		FetchedAt:   old,
		ContentHash: "abc",
		Index:       NewSnapshotIndex(map[string]ModelFacts{"m": {}}),
	}
	var rt CatalogRuntime
	rt.active.Store(&s)
	v := BuildCatalogDiagnosticsJSON(CatalogStatusHandlerConfig{
		Runtime:                &rt,
		UsageEnabled:           true,
		ExternalUpdatesEnabled: true,
		UpdateInterval:         1 * time.Hour,
		Now:                    func() time.Time { return now },
	})
	if v.Status != CatalogDiagStale {
		t.Fatalf("status=%q want stale", v.Status)
	}
}
