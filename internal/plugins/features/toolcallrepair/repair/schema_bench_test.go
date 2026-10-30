package repair_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var benchmarkOutcome repair.Outcome

func BenchmarkSchemaCompile(b *testing.B) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"location":{"type":"string"},
			"unit":{"type":"string","enum":["c","f"]}
		},
		"required":["location"],
		"additionalProperties":false
	}`)
	limits := repair.DefaultSchemaLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		cache := repair.NewSchemaCache(limits)
		if _, err := cache.GetOrCompile(schema); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSchemaValidate(b *testing.B) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"location":{"type":"string"},
			"unit":{"type":"string","enum":["c","f"]}
		},
		"required":["location"],
		"additionalProperties":false
	}`)
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	cs, err := cache.GetOrCompile(schema)
	if err != nil {
		b.Fatal(err)
	}
	args := []byte(`{"location":"NYC","unit":"c"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := cs.Validate(args); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSchemaCacheHit(b *testing.B) {
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	if _, err := cache.GetOrCompile(schema); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := cache.GetOrCompile(schema); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSchemaCacheMiss(b *testing.B) {
	limits := repair.DefaultSchemaLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		cache := repair.NewSchemaCache(limits)
		schema := json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"x":{"type":"string","description":"%d"}},"required":["x"]}`, i))
		if _, err := cache.GetOrCompile(schema); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEngineRepair(b *testing.B) {
	schema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"},"unit":{"type":"string","default":"c"}},"required":["location"],"additionalProperties":false}`)
	catalog := []aipapi.ToolDef{{Name: "get_weather", Parameters: schema}}
	engine := repair.NewEngine()
	if _, err := engine.Repair(repair.Input{ToolName: "get_weather", ArgsJSON: []byte(`{"location":"NYC"}`), Catalog: catalog}); err != nil {
		b.Fatal(err)
	}
	cases := []struct {
		name string
		args []byte
	}{
		{"valid_cached", []byte(`{"location":"NYC"}`)},
		{"syntax_completion", []byte(`{"location":"NYC"`)},
		{"property_rename", []byte(`{"Location":"NYC"}`)},
		{"default_insertion", []byte(`{"location":"NYC","extra":true}`)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				out, err := engine.Repair(repair.Input{
					ToolName: "get_weather", ArgsJSON: tc.args, Catalog: catalog,
				})
				if err != nil {
					b.Fatal(err)
				}
				benchmarkOutcome = out
			}
		})
	}
}

func TestEngineValidHotPathAllocationBudget(t *testing.T) { //nolint:paralleltest // AllocsPerRun forbids parallel tests
	schema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	catalog := []aipapi.ToolDef{{Name: "get_weather", Parameters: schema}}
	engine := repair.NewEngine()
	in := repair.Input{ToolName: "get_weather", ArgsJSON: []byte(`{"location":"NYC"}`), Catalog: catalog}
	if _, err := engine.Repair(in); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		out, err := engine.Repair(in)
		if err != nil || out.Kind != repair.OutcomePass {
			panic("valid hot path failed")
		}
	})
	if allocs > 100 {
		t.Fatalf("valid cached hot path allocations = %.1f, budget 100", allocs)
	}
}
