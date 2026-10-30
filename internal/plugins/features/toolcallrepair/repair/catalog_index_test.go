package repair_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCatalogIndex_Exact(t *testing.T) {
	t.Parallel()
	idx := repair.BuildCatalogIndex([]aipapi.ToolDef{
		{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "Get-Weather", Parameters: json.RawMessage(`{"type":"string"}`)},
	})
	got, ok := idx.Exact("get_weather")
	if !ok || got.Name != "get_weather" {
		t.Fatalf("exact miss: ok=%v name=%q", ok, got.Name)
	}
	if _, ok := idx.Exact("missing"); ok {
		t.Fatal("unexpected exact hit")
	}
}

func TestCatalogIndex_UniqueNormalized(t *testing.T) {
	t.Parallel()
	idx := repair.BuildCatalogIndex([]aipapi.ToolDef{
		{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "other_tool", Parameters: json.RawMessage(`{"type":"object"}`)},
	})
	got, ok := idx.UniqueNormalized("Get-Weather")
	if !ok {
		t.Fatal("expected unique normalized match")
	}
	if got.Name != "get_weather" {
		t.Fatalf("name=%q want get_weather", got.Name)
	}
}

func TestCatalogIndex_AmbiguousNormalizedNotUnique(t *testing.T) {
	t.Parallel()
	idx := repair.BuildCatalogIndex([]aipapi.ToolDef{
		{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "Get-Weather", Parameters: json.RawMessage(`{"type":"object"}`)},
	})
	if _, ok := idx.UniqueNormalized("getweather"); ok {
		t.Fatal("ambiguous normalized name must not be unique")
	}
	if _, ok := idx.Exact("get_weather"); !ok {
		t.Fatal("exact lookup must still work for ambiguous catalog")
	}
	if _, ok := idx.Exact("Get-Weather"); !ok {
		t.Fatal("exact lookup must still work for second tool")
	}
}

func TestCatalogIndex_ParametersDetachedFromCaller(t *testing.T) {
	t.Parallel()
	params := json.RawMessage(`{"type":"object"}`)
	catalog := []aipapi.ToolDef{{Name: "get_weather", Parameters: params}}
	idx := repair.BuildCatalogIndex(catalog)
	params[2] = 'X' // mutate caller-owned schema bytes after index build
	got, ok := idx.Exact("get_weather")
	if !ok {
		t.Fatal("exact miss")
	}
	if string(got.Parameters) != `{"type":"object"}` {
		t.Fatalf("index Parameters aliased caller bytes: %q", got.Parameters)
	}
}

func TestCatalogIndex_UniqueNormalizedParametersDetachedFromCaller(t *testing.T) {
	t.Parallel()
	params := json.RawMessage(`{"type":"object"}`)
	catalog := []aipapi.ToolDef{
		{Name: "get_weather", Parameters: params},
		{Name: "other_tool", Parameters: json.RawMessage(`{"type":"string"}`)},
	}
	idx := repair.BuildCatalogIndex(catalog)
	params[2] = 'X'
	got, ok := idx.UniqueNormalized("Get-Weather")
	if !ok {
		t.Fatal("expected unique normalized match")
	}
	if string(got.Parameters) != `{"type":"object"}` {
		t.Fatalf("UniqueNormalized Parameters aliased caller bytes: %q", got.Parameters)
	}
}

func TestCatalogIndex_EdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()
		var idx *repair.CatalogIndex
		if _, ok := idx.Exact("x"); ok {
			t.Fatal("nil Exact")
		}
		if _, ok := idx.UniqueNormalized("x"); ok {
			t.Fatal("nil UniqueNormalized")
		}
	})
	t.Run("empty_and_nil_parameters", func(t *testing.T) {
		t.Parallel()
		idx := repair.BuildCatalogIndex([]aipapi.ToolDef{
			{Name: "a", Parameters: nil},
			{Name: "b", Parameters: json.RawMessage{}},
		})
		got, ok := idx.Exact("a")
		if !ok || got.Parameters != nil {
			t.Fatalf("nil Parameters: ok=%v params=%v", ok, got.Parameters)
		}
		got, ok = idx.Exact("b")
		if !ok || len(got.Parameters) != 0 {
			t.Fatalf("empty Parameters: ok=%v params=%v", ok, got.Parameters)
		}
	})
	t.Run("empty_normalized_name_skipped", func(t *testing.T) {
		t.Parallel()
		idx := repair.BuildCatalogIndex([]aipapi.ToolDef{
			{Name: "---", Parameters: json.RawMessage(`{}`)},
			{Name: "keep", Parameters: json.RawMessage(`{}`)},
		})
		if _, ok := idx.UniqueNormalized("---"); ok {
			t.Fatal("empty normalized name must not be indexed")
		}
		if _, ok := idx.Exact("keep"); !ok {
			t.Fatal("exact keep miss")
		}
	})
}
