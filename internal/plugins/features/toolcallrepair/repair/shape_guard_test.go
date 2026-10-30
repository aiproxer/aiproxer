package repair_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func nestedObjectJSON(depth int) []byte {
	var b strings.Builder
	for range depth {
		b.WriteString(`{"a":`)
	}
	b.WriteByte('1')
	for range depth {
		b.WriteByte('}')
	}
	return []byte(b.String())
}

func TestSchemaDefaultDepthBoundaries(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	if limits.MaxNestingDepth != 32 {
		t.Fatalf("DefaultSchemaLimits.MaxNestingDepth=%d want 32", limits.MaxNestingDepth)
	}
	cache := repair.NewSchemaCache(limits)
	if _, err := cache.GetOrCompile(nestedObjectJSON(32)); err != nil {
		t.Fatalf("depth exact 32 compile: %v", err)
	}
	err := mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(nestedObjectJSON(33))
		return e
	})
	assertSchemaReason(t, err, repair.ReasonNestingTooDeep)
	assertNoSecret(t, err.Error(), `"a":`)
}

func TestRepairArgsJSONPreflightsBeforeParse(t *testing.T) {
	t.Parallel()
	schemaOK := json.RawMessage(`{"type":"object"}`)
	argsOver := []byte(strings.Repeat(`[`, jsonshape.ToolArgumentsLimits().MaxDepth+1) + `1` + strings.Repeat(`]`, jsonshape.ToolArgumentsLimits().MaxDepth+1))
	_, reason, err := repair.ExportRepairArgsJSON(context.Background(), argsOver, schemaOK, repair.DefaultMaxArgsBytes, repair.DefaultSchemaLimits())
	if err == nil {
		t.Fatal("expected args depth+1 to fail before parse")
	}
	if reason != "" {
		t.Fatalf("reason=%q want empty on hard fail", reason)
	}
	if !strings.Contains(err.Error(), toolcall.ReasonUnrepairable) {
		t.Fatalf("err=%v want reason %q", err, toolcall.ReasonUnrepairable)
	}
	assertNoSecret(t, err.Error(), "1")

	schemaOver := nestedObjectJSON(repair.DefaultSchemaLimits().MaxNestingDepth + 1)
	_, _, err = repair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schemaOver, repair.DefaultMaxArgsBytes, repair.DefaultSchemaLimits())
	if err == nil {
		t.Fatal("expected schema depth+1 to fail before parse")
	}
	if !strings.Contains(err.Error(), toolcall.ReasonSchemaInvalid) {
		t.Fatalf("err=%v want reason %q", err, toolcall.ReasonSchemaInvalid)
	}
	assertNoSecret(t, err.Error(), `"a":`)
}

func TestRepairArgsJSONHonorsCustomPolicyAndCancel(t *testing.T) {
	t.Parallel()

	t.Run("max_args_over_default", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"x":"` + strings.Repeat("a", 70<<10) + `"}`)
		if len(payload) <= repair.DefaultMaxArgsBytes {
			t.Fatalf("fixture too small: %d", len(payload))
		}
		_, _, err := repair.ExportRepairArgsJSON(context.Background(), payload, json.RawMessage(`{"type":"object"}`), repair.DefaultMaxArgsBytes, repair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("default max must reject oversized args")
		}
		_, _, err = repair.ExportRepairArgsJSON(context.Background(), payload, json.RawMessage(`{"type":"object"}`), 100<<10, repair.DefaultSchemaLimits())
		if err != nil {
			t.Fatalf("custom maxArgsBytes must accept: %v", err)
		}
	})

	t.Run("schema_depth_over_default", func(t *testing.T) {
		t.Parallel()
		schema := nestedObjectJSON(35)
		_, _, err := repair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schema, repair.DefaultMaxArgsBytes, repair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("default schema depth must reject depth 35")
		}
		limits := repair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		_, _, err = repair.ExportRepairArgsJSON(context.Background(), []byte(`{}`), schema, repair.DefaultMaxArgsBytes, limits)
		if err != nil {
			t.Fatalf("custom schema depth must accept: %v", err)
		}
	})

	t.Run("canceled_before_parse", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := repair.ExportRepairArgsJSON(ctx, []byte(`{}`), json.RawMessage(`{"type":"object"}`), repair.DefaultMaxArgsBytes, repair.DefaultSchemaLimits())
		if err == nil {
			t.Fatal("expected canceled")
		}
		if !strings.Contains(err.Error(), toolcall.ReasonCanceled) {
			t.Fatalf("err=%v want %q", err, toolcall.ReasonCanceled)
		}
		assertNoSecret(t, err.Error(), "object")
	})
}

func TestEngineRepairHonorsCacheLimitsAndLargeMaxArgs(t *testing.T) {
	t.Parallel()

	t.Run("schema_cache_limits_snapshot", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		cache := repair.NewSchemaCache(limits)
		got := cache.Limits()
		if got.MaxNestingDepth != 40 {
			t.Fatalf("Limits().MaxNestingDepth=%d want 40", got.MaxNestingDepth)
		}
		got.MaxNestingDepth = 1
		if cache.Limits().MaxNestingDepth != 40 {
			t.Fatal("Limits() must return a defensive copy")
		}
	})

	t.Run("engine_large_max_args_empty_schema", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"x":"` + strings.Repeat("b", 70<<10) + `"}`)
		eng := repair.NewEngine()
		out, err := eng.RepairWithContext(context.Background(), repair.Input{
			ToolName:     "run",
			ArgsJSON:     payload,
			Catalog:      []aipapi.ToolDef{{Name: "run", Parameters: nil}},
			MaxArgsBytes: 100 << 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomePass {
			t.Fatalf("kind=%v reason=%q", out.Kind, out.ReasonCode)
		}
	})

	t.Run("engine_custom_schema_depth_repair", func(t *testing.T) {
		t.Parallel()
		limits := repair.DefaultSchemaLimits()
		limits.MaxNestingDepth = 40
		cache := repair.NewSchemaCache(limits)
		eng := repair.NewEngineWithCache(cache)
		schema := nestedObjectJSON(35)
		out, err := eng.RepairWithContext(context.Background(), repair.Input{
			ToolName: "run",
			ArgsJSON: []byte(`{}`),
			Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: schema}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Kind != repair.OutcomePass {
			t.Fatalf("kind=%v reason=%q want pass under custom schema depth", out.Kind, out.ReasonCode)
		}
	})
}

func TestSchemaPreflightRejectsBeforeMaterialize(t *testing.T) {
	t.Parallel()

	limits := repair.DefaultSchemaLimits()
	limits.MaxNestingDepth = 3
	limits.MaxProperties = 2
	cache := repair.NewSchemaCache(limits)

	deepOK := []byte(`{"a":{"b":{"c":1}}}`)
	if _, err := cache.GetOrCompile(deepOK); err != nil {
		t.Fatalf("depth exact compile: %v", err)
	}
	deepOver := []byte(`{"a":{"b":{"c":{"d":1}}}}`)
	err := mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(deepOver)
		return e
	})
	assertSchemaReason(t, err, repair.ReasonNestingTooDeep)

	dup := []byte(`{"type":"object","type":"string"}`)
	err = mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(dup)
		return e
	})
	assertSchemaReason(t, err, repair.ReasonMalformedJSON)

	wide := []byte(`{"a":1,"b":2,"c":3}`)
	err = mustSchemaErr(t, func() error {
		_, e := cache.GetOrCompile(wide)
		return e
	})
	assertSchemaReason(t, err, repair.ReasonTooManyProperties)
}

func TestSchemaShapeLimitsArrayCapFollowsMaxNodes(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	limits.MaxNodes = 10
	maxArr, maxNodes, _ := repair.ExportSchemaShapeLimits(limits)
	if maxArr != 10 || maxNodes != 10 {
		t.Fatalf("MaxArrayElems=%d MaxNodes=%d want both 10", maxArr, maxNodes)
	}
	if maxArr > maxNodes {
		t.Fatal("MaxArrayElems must never exceed MaxNodes")
	}
}

func TestSchemaPreflightFlatArrayExceedingMaxNodes(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	limits.MaxNodes = 10
	schema := []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 20), ",") + `]`)
	err := repair.ExportPreflightSchemaJSON(context.Background(), schema, limits)
	assertSchemaReason(t, err, repair.ReasonTooManyNodes)
	assertNoSecret(t, err.Error(), "1,1")

	err = mustSchemaErr(t, func() error {
		_, e := repair.NewSchemaCache(limits).GetOrCompile(schema)
		return e
	})
	assertSchemaReason(t, err, repair.ReasonTooManyNodes)
}

func TestMapEngineSchemaReasonSafeSpecific(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "canceled", err: &repair.SchemaError{Kind: repair.SchemaKindInvalid, ReasonCode: repair.ReasonCanceled}, want: toolcall.ReasonCanceled},
		{name: "unsupported", err: &repair.SchemaError{Kind: repair.SchemaKindUnsupported, ReasonCode: toolcall.ReasonSchemaUnsupported}, want: toolcall.ReasonSchemaUnsupported},
		{name: "too_many_nodes", err: &repair.SchemaError{Kind: repair.SchemaKindLimitExceeded, ReasonCode: repair.ReasonTooManyNodes}, want: toolcall.ReasonSchemaInvalid},
		{name: "nesting", err: &repair.SchemaError{Kind: repair.SchemaKindLimitExceeded, ReasonCode: repair.ReasonNestingTooDeep}, want: toolcall.ReasonSchemaInvalid},
		{name: "malformed", err: &repair.SchemaError{Kind: repair.SchemaKindMalformed, ReasonCode: repair.ReasonMalformedJSON}, want: toolcall.ReasonSchemaInvalid},
		{name: "plain", err: errors.New("boom"), want: toolcall.ReasonSchemaInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := repair.ExportMapEngineSchemaReason(tc.err); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSchemaPreflightCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repair.NewSchemaCache(repair.DefaultSchemaLimits()).GetOrCompileWithContext(ctx, json.RawMessage(`{"type":"object"}`))
	assertSchemaReason(t, err, repair.ReasonCanceled)
	assertNoSecret(t, err.Error(), "object")
}

func TestValidateWithContextShapeAndCancel(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`)
	cs, err := repair.NewSchemaCache(repair.DefaultSchemaLimits()).GetOrCompile(schema)
	if err != nil {
		t.Fatal(err)
	}

	depthOver := jsonshape.ToolArgumentsLimits().MaxDepth + 1
	deep := []byte(strings.Repeat(`[`, depthOver) + `1` + strings.Repeat(`]`, depthOver))
	err = cs.ValidateWithContext(context.Background(), deep)
	assertSchemaReason(t, err, repair.ReasonNestingTooDeep)
	assertNoSecret(t, err.Error(), "1")

	dup := []byte(`{"n":1,"n":2}`)
	err = cs.ValidateWithContext(context.Background(), dup)
	assertSchemaReason(t, err, repair.ReasonMalformedJSON)

	num := []byte(`{"n":` + strings.Repeat("9", 80) + `}`)
	err = cs.ValidateWithContext(context.Background(), num)
	assertSchemaReason(t, err, repair.ReasonArgsTooLargeShape)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = cs.ValidateWithContext(ctx, []byte(`{"n":1}`))
	assertSchemaReason(t, err, repair.ReasonCanceled)
}

func TestEngineRepairWithContextShapeGuards(t *testing.T) {
	t.Parallel()
	catalog := []aipapi.ToolDef{{
		Name:       "run",
		Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"additionalProperties":false}`),
	}}
	eng := repair.NewEngineWithCache(repair.NewSchemaCache(repair.DefaultSchemaLimits()))

	depthLimit := jsonshape.ToolArgumentsLimits().MaxDepth
	emptyCatalog := []aipapi.ToolDef{{Name: "run", Parameters: nil}}
	at := []byte(strings.Repeat(`[`, depthLimit) + `1` + strings.Repeat(`]`, depthLimit))
	out, err := eng.RepairWithContext(context.Background(), repair.Input{
		ToolName: "run", ArgsJSON: at, Catalog: emptyCatalog, MaxArgsBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind == repair.OutcomeUnrepairable {
		t.Fatalf("exact depth=%d must not be Unrepairable (kind=%v reason=%q)", depthLimit, out.Kind, out.ReasonCode)
	}
	if out.Kind != repair.OutcomePass && out.Kind != repair.OutcomeRewrite {
		t.Fatalf("exact depth kind=%v want Pass or Rewrite", out.Kind)
	}

	over := []byte(strings.Repeat(`[`, depthLimit+1) + `1` + strings.Repeat(`]`, depthLimit+1))
	out, err = eng.RepairWithContext(context.Background(), repair.Input{
		ToolName: "run", ArgsJSON: over, Catalog: emptyCatalog, MaxArgsBytes: 64 << 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonUnrepairable {
		t.Fatalf("depth+1: kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
	if string(out.ArgsJSON) != string(over) {
		t.Fatal("original args not preserved")
	}

	cases := []struct {
		name   string
		args   []byte
		reason string
	}{
		{name: "duplicate", args: []byte(`{"n":1,"n":2}`), reason: toolcall.ReasonUnrepairable},
		{name: "invalid_utf8", args: []byte{'{', '"', 'n', '"', ':', '"', 0xff, '"', '}'}, reason: toolcall.ReasonUnrepairable},
		{name: "huge_number", args: []byte(`{"n":` + strings.Repeat("9", 80) + `}`), reason: toolcall.ReasonArgsTooLarge},
		{name: "wide_array", args: []byte(`[` + strings.TrimRight(strings.Repeat(`1,`, 5000), ",") + `]`), reason: toolcall.ReasonUnrepairable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := eng.RepairWithContext(context.Background(), repair.Input{
				ToolName: "run", ArgsJSON: tc.args, Catalog: catalog, MaxArgsBytes: 64 << 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != tc.reason {
				t.Fatalf("kind=%v reason=%q want %q", out.Kind, out.ReasonCode, tc.reason)
			}
			if string(out.ArgsJSON) != string(tc.args) {
				t.Fatal("original not preserved")
			}
		})
	}
}

func TestEngineRepairWithContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := repair.NewEngine().RepairWithContext(ctx, repair.Input{
		ToolName: "run",
		ArgsJSON: []byte(`{"n":1}`),
		Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable || out.ReasonCode != toolcall.ReasonCanceled {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.ReasonCode)
	}
}

func TestEngineEmptySchemaStillShapeChecks(t *testing.T) {
	t.Parallel()
	eng := repair.NewEngine()
	depthOver := jsonshape.ToolArgumentsLimits().MaxDepth + 1
	over := []byte(strings.Repeat(`[`, depthOver) + `0` + strings.Repeat(`]`, depthOver))
	out, err := eng.RepairWithContext(context.Background(), repair.Input{
		ToolName: "run",
		ArgsJSON: over,
		Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable {
		t.Fatalf("kind=%v want unrepairable", out.Kind)
	}
}

func TestFinalizerPropagatesCancelReason(t *testing.T) {
	t.Parallel()
	fin := repair.NewFinalizer(repair.FinalizerPolicy{
		MaxArgsBytes:   1024,
		OnUnrepairable: repair.OnUnrepairableError,
		Order:          repair.DefaultFinalizerOrder,
		Schema:         repair.DefaultSchemaLimits(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := fin.Finalize(ctx, toolcall.CompletedCall{
		ToolCallID: "c1", ToolName: "run", ArgsJSON: []byte(`{"n":1}`),
	}, aipapi.ToolDef{Name: "run"}, []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`{"type":"object"}`)}}, toolcall.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonCanceled {
		t.Fatalf("action=%v reason=%q", res.Action, res.ReasonCode)
	}
}

func TestEngineCompletedSyntaxCandidateIsPreflighted(t *testing.T) {
	t.Parallel()
	prefix := strings.Repeat(`[`, jsonshape.ToolArgumentsLimits().MaxDepth+1)
	eng := repair.NewEngine()
	out, err := eng.RepairWithContext(context.Background(), repair.Input{
		ToolName: "run",
		ArgsJSON: []byte(prefix),
		Catalog:  []aipapi.ToolDef{{Name: "run", Parameters: json.RawMessage(`null`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != repair.OutcomeUnrepairable {
		t.Fatalf("kind=%v want unrepairable after completed deep JSON", out.Kind)
	}
}

func mustSchemaErr(t *testing.T, fn func() error) error {
	t.Helper()
	err := fn()
	if err == nil {
		t.Fatal("expected error")
	}
	return err
}

func assertSchemaReason(t *testing.T, err error, want string) {
	t.Helper()
	var se *repair.SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("got %T (%v), want *SchemaError", err, err)
	}
	if se.ReasonCode != want {
		t.Fatalf("reason=%q want %q (err=%v)", se.ReasonCode, want, err)
	}
}
