package repair_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
)

func TestSchemaCache_Hit(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
	a, err := cache.GetOrCompile(schema)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	b, err := cache.GetOrCompile(schema)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if a != b {
		t.Fatal("expected cache hit to return same *CompiledSchema")
	}
	if err := b.Validate([]byte(`{"x":"ok"}`)); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSchemaCache_Eviction(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	limits.MaxCacheEntries = 2
	cache := repair.NewSchemaCache(limits)

	s1 := json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"}},"required":["a"]}`)
	s2 := json.RawMessage(`{"type":"object","properties":{"b":{"type":"number"}},"required":["b"]}`)
	s3 := json.RawMessage(`{"type":"object","properties":{"c":{"type":"number"}},"required":["c"]}`)

	c1, err := cache.GetOrCompile(s1)
	if err != nil {
		t.Fatalf("s1: %v", err)
	}
	if _, err := cache.GetOrCompile(s2); err != nil {
		t.Fatalf("s2: %v", err)
	}
	if _, err := cache.GetOrCompile(s3); err != nil {
		t.Fatalf("s3: %v", err)
	}
	c1b, err := cache.GetOrCompile(s1)
	if err != nil {
		t.Fatalf("s1 recompile: %v", err)
	}
	if c1 == c1b {
		t.Fatal("expected s1 to be evicted and recompiled as a new instance")
	}
}

func TestSchemaCache_ConcurrentSameDigest(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	ptrs := make(chan *repair.CompiledSchema, n)
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			cs, err := cache.GetOrCompile(schema)
			if err != nil {
				errs <- err
				return
			}
			if err := cs.Validate([]byte(`{"n":7}`)); err != nil {
				errs <- err
				return
			}
			ptrs <- cs
		}()
	}
	wg.Wait()
	close(errs)
	close(ptrs)
	for err := range errs {
		t.Fatalf("concurrent compile/validate: %v", err)
	}
	var first *repair.CompiledSchema
	for cs := range ptrs {
		if first == nil {
			first = cs
			continue
		}
		if cs != first {
			t.Fatalf("concurrent same digest returned distinct instances: %p vs %p", first, cs)
		}
	}
	if first == nil {
		t.Fatal("no compiled schemas returned")
	}
}

func TestSchemaCache_ByteBudgetEviction(t *testing.T) {
	t.Parallel()
	limits := repair.DefaultSchemaLimits()
	limits.MaxCacheEntries = 64
	limits.MaxCacheBytes = 200
	cache := repair.NewSchemaCache(limits)
	for i := range 8 {
		schema := json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"f%d":{"type":"string"}},"required":["f%d"]}`, i, i))
		if _, err := cache.GetOrCompile(schema); err != nil {
			t.Fatalf("compile %d: %v", i, err)
		}
	}
}
