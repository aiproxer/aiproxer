package modelsdev_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
)

func TestParseSnapshot_validMinimal(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"anthropic": {
			"id": "anthropic",
			"name": "Anthropic",
			"models": [
				{"id": "claude-3-5-sonnet-20241022", "name": "Sonnet"}
			]
		}
	}`)
	fetched := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	snap, err := modelsdev.ParseSnapshot(raw, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ContentHash == "" || snap.Generation == "" {
		t.Fatalf("want non-empty hash and generation, got hash=%q gen=%q", snap.ContentHash, snap.Generation)
	}
	if !snap.FetchedAt.Equal(fetched) {
		t.Fatalf("FetchedAt: got %v want %v", snap.FetchedAt, fetched)
	}
	f, ok := snap.Index.FactsByCatalogModelID("anthropic/claude-3-5-sonnet-20241022")
	if !ok {
		t.Fatal("expected catalog id anthropic/claude-3-5-sonnet-20241022")
	}
	if f.Tools != modelcatalog.CapabilityUnknown || f.Reasoning != modelcatalog.CapabilityUnknown {
		t.Fatalf("sparse model: got tools=%v reasoning=%v", f.Tools, f.Reasoning)
	}
	if f.ContextLimit.State != modelcatalog.LimitUnknown {
		t.Fatalf("context limit: got %+v", f.ContextLimit)
	}
}

func TestParseModelIDs(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]},"google":{"id":"google","models":[{"id":"gemma3:4b"}]}}`)
	ids, err := modelsdev.ParseModelIDs(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"google/gemma3:4b", "openai/gpt-oss:120b"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %+v, want %+v", ids, want)
	}
}

func TestParseModelIDs_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     []byte
		want    []string
		wantErr bool
	}{
		{name: "empty", raw: nil, wantErr: true},
		{name: "null root", raw: []byte("null"), wantErr: true},
		{name: "empty object", raw: []byte("{}"), want: []string{}},
		{name: "provider without models", raw: []byte(`{"openai":{"id":"openai"}}`), want: []string{}},
		{name: "empty model ids skipped", raw: []byte(`{"openai":{"id":"openai","models":[{"id":""}]}}`), want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := modelsdev.ParseModelIDs(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseModelIDs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseModelIDs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSnapshot_richMapping(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"openai": {
			"id": "openai",
			"models": [{
				"id": "gpt-4o",
				"tool_call": true,
				"reasoning": false,
				"structured_output": true,
				"modalities": {"input": ["text", "image", "pdf"]},
				"limit": {"context": 128000, "input": 100000, "output": 4096},
				"pricing": {"prompt": 999, "completion": 888}
			}]
		}
	}`)
	snap, err := modelsdev.ParseSnapshot(raw, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := snap.Index.FactsByCatalogModelID("openai/gpt-4o")
	if !ok {
		t.Fatal("missing openai/gpt-4o")
	}
	if f.Tools != modelcatalog.CapabilitySupported {
		t.Fatalf("tools: got %v", f.Tools)
	}
	if f.Reasoning != modelcatalog.CapabilityUnsupported {
		t.Fatalf("reasoning explicit false: got %v", f.Reasoning)
	}
	if f.StructuredOutputs != modelcatalog.CapabilitySupported {
		t.Fatalf("structured: got %v", f.StructuredOutputs)
	}
	if f.Vision != modelcatalog.CapabilitySupported {
		t.Fatalf("vision: got %v", f.Vision)
	}
	if f.Documents != modelcatalog.CapabilitySupported {
		t.Fatalf("documents: got %v", f.Documents)
	}
	if f.ContextLimit.State != modelcatalog.LimitPresent || f.ContextLimit.Tokens != 128000 {
		t.Fatalf("context limit: got %+v", f.ContextLimit)
	}
	if f.InputLimit.State != modelcatalog.LimitPresent || f.InputLimit.Tokens != 100000 {
		t.Fatalf("input limit: got %+v", f.InputLimit)
	}
	if f.OutputLimit.State != modelcatalog.LimitPresent || f.OutputLimit.Tokens != 4096 {
		t.Fatalf("output limit: got %+v", f.OutputLimit)
	}
}

func TestParseSnapshot_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := modelsdev.ParseSnapshot([]byte(`{`), time.Time{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseSnapshot_nonObjectRoot(t *testing.T) {
	t.Parallel()
	_, err := modelsdev.ParseSnapshot([]byte(`[]`), time.Time{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseSnapshot_modelsNotArray(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"p": {"id":"p","models": {}}}`)
	_, err := modelsdev.ParseSnapshot(raw, time.Time{})
	if err == nil {
		t.Fatal("expected error for models object")
	}
}

func TestParseSnapshot_ignoresIrrelevantMetadata(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"x": {
			"id": "x",
			"api": "https://example.com",
			"npm": "@scope/pkg",
			"models": [{
				"id": "m1",
				"name": "M",
				"release_date": "2024-01-01",
				"last_updated": "2024-02-02",
				"tool_call": true
			}]
		}
	}`)
	snap, err := modelsdev.ParseSnapshot(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := snap.Index.FactsByCatalogModelID("x/m1")
	if !ok {
		t.Fatal("expected x/m1")
	}
	if f.Tools != modelcatalog.CapabilitySupported {
		t.Fatalf("tools: %v", f.Tools)
	}
}

func TestParseSnapshot_limitRejectsOverflowAndNonFinite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"inf", `{"p":{"id":"p","models":[{"id":"m","limit":{"context":1e308}}]}}`},
		{"above_int64", `{"p":{"id":"p","models":[{"id":"m","limit":{"context":1e100}}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snap, err := modelsdev.ParseSnapshot([]byte(tc.raw), time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			f, ok := snap.Index.FactsByCatalogModelID("p/m")
			if !ok {
				t.Fatal("expected p/m")
			}
			if f.ContextLimit.State != modelcatalog.LimitUnknown {
				t.Fatalf("context limit: got %+v want unknown", f.ContextLimit)
			}
		})
	}
}

func TestParseSnapshot_suffixLookupIndex(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"anthropic": {
			"id": "anthropic",
			"models": [{"id": "claude-opus-4-7"}]
		},
		"alibaba": {
			"id": "alibaba",
			"models": [{"id": "qwen3.7-plus"}]
		}
	}`)
	snap, err := modelsdev.ParseSnapshot(raw, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	ids := snap.Index.CatalogIDsForSuffixLookup("claude-opus-4.7")
	if len(ids) != 1 || ids[0] != "anthropic/claude-opus-4-7" {
		t.Fatalf("dotted suffix lookup = %v", ids)
	}
}

func TestParseSnapshot_generationMatchesContentHash(t *testing.T) {
	t.Parallel()
	a := []byte(`{"a":{"id":"a","models":[{"id":"m"}]}}`)
	b := []byte(`{"a":{"id":"a","models":[{"id":"n"}]}}`)
	sa, err := modelsdev.ParseSnapshot(a, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	sb, err := modelsdev.ParseSnapshot(b, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if sa.ContentHash == sb.ContentHash {
		t.Fatalf("different payloads should yield different hashes")
	}
	if sa.Generation != sa.ContentHash {
		t.Fatalf("generation should equal content hash, got gen=%q hash=%q", sa.Generation, sa.ContentHash)
	}
}
