package repair_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func TestErrorsDoNotLeakToolCallPayloads(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_TOOLCALL_PAYLOAD_152"

	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	compiled, err := cache.GetOrCompile(json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	validationErr := compiled.Validate([]byte(`{"value":"SECRET_TOOLCALL_PAYLOAD_152"}`))
	if validationErr == nil {
		t.Fatal("expected validation error")
	}
	assertNoSecret(t, validationErr.Error(), secret)

	_, compileErr := cache.GetOrCompile(json.RawMessage(`{"description":"SECRET_TOOLCALL_PAYLOAD_152","$ref":"https://SECRET_TOOLCALL_PAYLOAD_152.invalid/schema"}`))
	if compileErr == nil {
		t.Fatal("expected compile error")
	}
	assertNoSecret(t, compileErr.Error(), secret)

	out, err := repair.NewEngineWithCache(cache).Repair(repair.Input{
		ToolCallID: secret,
		ToolName:   secret,
		ArgsJSON:   []byte(`{"value":"SECRET_TOOLCALL_PAYLOAD_152"}`),
		Catalog:    []aipapi.ToolDef{{Name: "other", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, out.ReasonCode, secret)
}

func TestValidationErrorPathIsBoundedAndValueFree(t *testing.T) {
	t.Parallel()
	const secret = "SECRET_INSTANCE_VALUE_152"
	longKey := strings.Repeat("property", 80)
	schema := json.RawMessage(`{"type":"object","properties":{"` + longKey + `":{"type":"integer"}}}`)
	compiled, err := repair.NewSchemaCache(repair.DefaultSchemaLimits()).GetOrCompile(schema)
	if err != nil {
		t.Fatal(err)
	}
	err = compiled.Validate([]byte(`{"` + longKey + `":"` + secret + `"}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
	var schemaErr *repair.SchemaError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("got %T, want *SchemaError", err)
	}
	if !strings.Contains(err.Error(), "path=") {
		t.Fatalf("missing bounded path: %v", err)
	}
	if !strings.Contains(err.Error(), "validation_failed") {
		t.Fatalf("missing stable reason: %v", err)
	}
	if len([]rune(schemaErr.InstancePath)) > 256 {
		t.Fatalf("path was not bounded: %d runes", len([]rune(schemaErr.InstancePath)))
	}
	assertNoSecret(t, err.Error(), secret)
}

func TestEngineAdversarialInputsFailClosed(t *testing.T) {
	t.Parallel()
	catalog := []aipapi.ToolDef{{
		Name:       "run",
		Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`),
	}}
	eng := repair.NewEngineWithCache(repair.NewSchemaCache(repair.DefaultSchemaLimits()))
	cases := []struct {
		name       string
		args       []byte
		wantReason string
	}{
		{name: "invalid_utf8", args: []byte{'{', '"', 'n', '"', ':', '"', 0xff, '"', '}'}, wantReason: toolcall.ReasonUnrepairable},
		{name: "nested_duplicate", args: []byte(`{"n":1,"child":{"x":1,"x":2}}`), wantReason: toolcall.ReasonUnrepairable},
		{name: "truncated_escape", args: []byte(`{"n":"\`), wantReason: toolcall.ReasonUnrepairable},
		{name: "truncated_unicode", args: []byte(`{"n":"\u12`), wantReason: toolcall.ReasonUnrepairable},
		{name: "truncated_number", args: []byte(`{"n":1e`), wantReason: toolcall.ReasonUnrepairable},
		{name: "truncated_literal", args: []byte(`{"n":tru`), wantReason: toolcall.ReasonUnrepairable},
		{name: "mismatched_closer", args: []byte(`{"n":1]`), wantReason: toolcall.ReasonUnrepairable},
		{name: "trailing_document", args: []byte(`{"n":1}{"n":2}`), wantReason: toolcall.ReasonUnrepairable},
		{name: "excessive_arguments", args: []byte(strings.Repeat(" ", 1025)), wantReason: toolcall.ReasonArgsTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := eng.Repair(repair.Input{
				ToolName: "run", ArgsJSON: tc.args, Catalog: catalog, MaxArgsBytes: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeUnrepairable {
				t.Fatalf("kind=%v want unrepairable", out.Kind)
			}
			if out.ReasonCode != tc.wantReason {
				t.Fatalf("reason=%q want %q", out.ReasonCode, tc.wantReason)
			}
		})
	}
}

func TestSchemaCacheConcurrentChurn(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	limits.MaxCacheEntries = 2
	limits.MaxCacheBytes = 1024
	cache := repair.NewSchemaCache(limits)

	const n = 64
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			schema := json.RawMessage(`{"type":"object","properties":{"p` + string(rune('a'+i%8)) + `":{"type":"integer"}}}`)
			compiled, err := cache.GetOrCompile(schema)
			if err != nil {
				errs <- err
				return
			}
			if err := compiled.Validate([]byte(`{}`)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func assertNoSecret(t *testing.T, got, secret string) {
	t.Helper()
	if strings.Contains(got, secret) {
		t.Fatalf("secret leaked: %q", got)
	}
}
