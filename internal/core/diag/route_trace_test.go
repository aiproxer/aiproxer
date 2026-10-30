package diag

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRouteTraceBuffer_snapshotOrderAndWrap(t *testing.T) {
	t.Parallel()
	b := NewRouteTraceBuffer(3)
	b.Append(RouteTraceEntry{TraceID: "a"})
	b.Append(RouteTraceEntry{TraceID: "b"})
	b.Append(RouteTraceEntry{TraceID: "c"})
	got := b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "a" || got[1].TraceID != "b" || got[2].TraceID != "c" {
		t.Fatalf("fill: %#v", got)
	}
	b.Append(RouteTraceEntry{TraceID: "d"})
	got = b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "b" || got[1].TraceID != "c" || got[2].TraceID != "d" {
		t.Fatalf("after wrap: %#v", got)
	}
	b.Append(RouteTraceEntry{TraceID: "e"})
	got = b.Snapshot()
	if len(got) != 3 || got[0].TraceID != "c" || got[1].TraceID != "d" || got[2].TraceID != "e" {
		t.Fatalf("second wrap: %#v", got)
	}
}

func TestRouteTraceBuffer_emptySnapshotNonNil(t *testing.T) {
	t.Parallel()
	b := NewRouteTraceBuffer(4)
	got := b.Snapshot()
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestRouteTraceBuffer_nilAppendNoPanic(t *testing.T) {
	t.Parallel()
	var b *RouteTraceBuffer
	b.Append(RouteTraceEntry{TraceID: "x"})
}

func TestRouteTraceEntry_catalogJSON(t *testing.T) {
	t.Parallel()
	e := RouteTraceEntry{
		TraceID:  "t1",
		Decision: "plan_candidate",
		Detail:   "be:m",
		Catalog: &RouteTraceCatalog{
			MatchKind:  "exact",
			FactSource: "catalog",
		},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got RouteTraceEntry
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, string(b))
	}
	if got.TraceID != "t1" || got.Decision != "plan_candidate" || got.Detail != "be:m" {
		t.Fatalf("top-level fields: %#v", got)
	}
	if got.Catalog == nil {
		t.Fatal("expected catalog object in JSON")
	}
	if got.Catalog.MatchKind != "exact" || got.Catalog.FactSource != "catalog" {
		t.Fatalf("catalog: %#v", got.Catalog)
	}
}

func TestRouteTraceEntry_modelViewJSON(t *testing.T) {
	t.Parallel()
	e := RouteTraceEntry{
		TraceID: "t-model", Decision: "plan_candidate", Detail: "be:model",
		ModelView: &RouteTraceModelView{
			Digest: "digest", ConfigGeneration: 7, ConfigFingerprint: "fp-safe",
			RegistryGeneration: "reg-1", CatalogGeneration: "cat-1",
		},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"digest":"digest"`, `"config_generation":7`, `"registry_generation":"reg-1"`, `"catalog_generation":"cat-1"`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("route trace JSON %s missing %s", b, want)
		}
	}
}
