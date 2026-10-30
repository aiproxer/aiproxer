package repair_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
)

func TestCompile_DefaultDraft2020(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)
	cs, err := cache.GetOrCompile(schema)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := cs.Validate([]byte(`{"n":1}`)); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestCompile_ExplicitDrafts(t *testing.T) {
	t.Parallel()
	drafts := []string{
		`http://json-schema.org/draft-04/schema#`,
		`http://json-schema.org/draft-06/schema#`,
		`http://json-schema.org/draft-07/schema#`,
		`https://json-schema.org/draft/2019-09/schema`,
		`https://json-schema.org/draft/2020-12/schema`,
	}
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	for _, draft := range drafts {
		t.Run(draft, func(t *testing.T) {
			t.Parallel()
			schema := json.RawMessage(`{"$schema":"` + draft + `","type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
			cs, err := cache.GetOrCompile(schema)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if err := cs.Validate([]byte(`{"ok":true}`)); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestCompile_UnsupportedDialect(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"$schema":"https://example.com/custom-dialect","type":"object"}`)
	_, err := cache.GetOrCompile(schema)
	assertSchemaKind(t, err, repair.SchemaKindUnsupported)
}

func TestCompile_ExternalRefRejectedWithoutIO(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.com/remote.json"}}}`)
	_, err := cache.GetOrCompile(schema)
	assertSchemaKind(t, err, repair.SchemaKindExternalRef)
}

func TestCompile_FileRefRejected(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"$ref":"file:///tmp/schema.json"}`)
	_, err := cache.GetOrCompile(schema)
	assertSchemaKind(t, err, repair.SchemaKindExternalRef)
}

func TestCompile_RelativeAndHTTPRefsRejected(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	cases := []string{
		`{"$ref":"other.json"}`,
		`{"$ref":"./defs.json#/$defs/x"}`,
		`{"$ref":"http://example.com/schema.json"}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := cache.GetOrCompile(json.RawMessage(raw))
			assertSchemaKind(t, err, repair.SchemaKindExternalRef)
		})
	}
}

func TestCompile_ClientSchemaConstructs(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	cases := []struct {
		name   string
		schema string
		valid  string
		bad    string
	}{
		{
			name: "openai_strict_object",
			schema: `{
				"type":"object",
				"properties":{
					"location":{"type":"string","description":"city"},
					"unit":{"type":"string","enum":["c","f"]}
				},
				"required":["location"],
				"additionalProperties":false
			}`,
			valid: `{"location":"NYC","unit":"c"}`,
			bad:   `{"location":"NYC","unit":"k"}`,
		},
		{
			name: "anthropic_input_schema_nullable",
			schema: `{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"limit":{"type":["integer","null"],"minimum":1}
				},
				"required":["query"]
			}`,
			valid: `{"query":"rain","limit":null}`,
			bad:   `{"query":"rain","limit":0}`,
		},
		{
			name: "gemini_object_with_array",
			schema: `{
				"type":"object",
				"properties":{
					"tags":{"type":"array","items":{"type":"string"},"minItems":1}
				},
				"required":["tags"]
			}`,
			valid: `{"tags":["a"]}`,
			bad:   `{"tags":[]}`,
		},
		{
			name: "anyof_branch",
			schema: `{
				"type":"object",
				"properties":{
					"value":{"anyOf":[{"type":"string"},{"type":"integer"}]}
				},
				"required":["value"]
			}`,
			valid: `{"value":12}`,
			bad:   `{"value":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs, err := cache.GetOrCompile(json.RawMessage(tc.schema))
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if err := cs.Validate([]byte(tc.valid)); err != nil {
				t.Fatalf("valid: %v", err)
			}
			if err := cs.Validate([]byte(tc.bad)); err == nil {
				t.Fatal("expected invalid instance")
			}
		})
	}
}

func TestCompile_CanceledContext(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.GetOrCompileWithContext(ctx, json.RawMessage(`{"type":"object"}`))
	assertSchemaKind(t, err, repair.SchemaKindInvalid)
	var se *repair.SchemaError
	if !errors.As(err, &se) || se.ReasonCode != repair.ReasonCanceled {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestCompile_LocalDefsRecursiveOK(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"node":{"$ref":"#/$defs/node"}},
		"$defs":{
			"node":{
				"type":"object",
				"properties":{
					"v":{"type":"string"},
					"child":{"$ref":"#/$defs/node"}
				},
				"additionalProperties":false
			}
		}
	}`)
	cs, err := cache.GetOrCompile(schema)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := cs.Validate([]byte(`{"node":{"v":"a","child":{"v":"b"}}}`)); err != nil {
		t.Fatalf("validate recursive: %v", err)
	}
}

func TestCompile_UnsafeDynamicRef(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"$dynamicRef":"#node"}`)
	_, err := cache.GetOrCompile(schema)
	assertSchemaKind(t, err, repair.SchemaKindUnsafe)
}

func TestCompile_Limits(t *testing.T) {
	t.Parallel()
	t.Run("schema_bytes", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxSchemaBytes = 16
		cache := repair.NewSchemaCache(limits)
		_, err := cache.GetOrCompile(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`))
		assertSchemaKind(t, err, repair.SchemaKindLimitExceeded)
	})
	t.Run("nesting_depth", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 2
		cache := repair.NewSchemaCache(limits)
		_, err := cache.GetOrCompile(json.RawMessage(`{"a":{"b":{"c":{"d":1}}}}`))
		assertSchemaKind(t, err, repair.SchemaKindLimitExceeded)
	})
	t.Run("nodes", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxNodes = 3
		cache := repair.NewSchemaCache(limits)
		_, err := cache.GetOrCompile(json.RawMessage(`{"a":1,"b":2,"c":3,"d":4}`))
		assertSchemaKind(t, err, repair.SchemaKindLimitExceeded)
	})
	t.Run("properties", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxProperties = 2
		cache := repair.NewSchemaCache(limits)
		_, err := cache.GetOrCompile(json.RawMessage(`{"a":1,"b":2,"c":3}`))
		assertSchemaKind(t, err, repair.SchemaKindLimitExceeded)
	})
	t.Run("local_ref_depth", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxLocalRefDepth = 1
		cache := repair.NewSchemaCache(limits)
		schema := json.RawMessage(`{
			"$ref":"#/$defs/a",
			"$defs":{
				"a":{"$ref":"#/$defs/b"},
				"b":{"$ref":"#/$defs/c"},
				"c":{"type":"string"}
			}
		}`)
		_, err := cache.GetOrCompile(schema)
		assertSchemaKind(t, err, repair.SchemaKindLimitExceeded)
		var se *repair.SchemaError
		if !errors.As(err, &se) || se.ReasonCode != repair.ReasonLocalRefTooDeep {
			t.Fatalf("want local_ref_too_deep, got %v", err)
		}
	})
}

func TestCompile_Malformed(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	t.Run("json", func(t *testing.T) {
		t.Parallel()
		_, err := cache.GetOrCompile(json.RawMessage(`{"type":`))
		assertSchemaKind(t, err, repair.SchemaKindMalformed)
	})
	t.Run("utf8", func(t *testing.T) {
		t.Parallel()
		_, err := cache.GetOrCompile(json.RawMessage("{\"type\":\"\xff\"}"))
		assertSchemaKind(t, err, repair.SchemaKindMalformed)
	})
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		_, err := cache.GetOrCompile(nil)
		assertSchemaKind(t, err, repair.SchemaKindMalformed)
	})
}

func TestDefaultSchemaLimits(t *testing.T) {
	t.Parallel()
	l := repair.DefaultSchemaLimits()
	if l.MaxSchemaBytes != 256*1024 {
		t.Fatalf("MaxSchemaBytes=%d", l.MaxSchemaBytes)
	}
	if l.MaxNestingDepth != 32 || l.MaxNodes != 4096 || l.MaxProperties != 1024 {
		t.Fatalf("unexpected structural limits: %+v", l)
	}
	if l.MaxLocalRefDepth != 32 || l.MaxCacheEntries != 64 || l.MaxCacheBytes != 4*1024*1024 {
		t.Fatalf("unexpected cache/ref limits: %+v", l)
	}
}

func assertSchemaKind(t *testing.T, err error, kind string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error kind %q", kind)
	}
	var se *repair.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("got %T (%v) want *SchemaError kind=%q", err, err, kind)
	}
	if se.Kind != kind {
		t.Fatalf("kind=%q want %q err=%v", se.Kind, kind, err)
	}
	msg := se.Error()
	if strings.Contains(msg, "https://example.com") || strings.Contains(msg, "file://") {
		t.Fatalf("error leaked URL payload: %q", msg)
	}
}
