package repair_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
)

// TestSchemaCache_SharedCompileSurvivesCallerCancellation pins the shared
// singleflight context: the first caller's cancellation must stop only that
// caller's wait, not the compile every peer waiting on the same digest needs.
func TestSchemaCache_SharedCompileSurvivesCallerCancellation(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}`)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)

	var first atomic.Bool
	compileCtxErr := make(chan error, 1)
	repair.ExportSetSchemaCacheCompile(cache, func(ctx context.Context, s json.RawMessage, limits repair.SchemaLimits) (*repair.CompiledSchema, error) {
		if first.CompareAndSwap(false, true) {
			close(started)
			<-release
			compileCtxErr <- ctx.Err()
		}
		return repair.ExportCompileSchema(context.Background(), s, limits)
	})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan error, 1)
	go func() {
		_, err := cache.GetOrCompileWithContext(leaderCtx, schema)
		leaderDone <- err
	}()
	<-started
	waiterDone := make(chan error, 1)
	go func() {
		_, err := cache.GetOrCompileWithContext(context.Background(), schema)
		waiterDone <- err
	}()

	cancelLeader()
	<-leaderCtx.Done()
	releaseAll()

	if err := <-waiterDone; err != nil {
		t.Fatalf("waiting caller: %v", err)
	}
	<-leaderDone
	if err := <-compileCtxErr; err != nil {
		t.Fatalf("shared compile observed the first caller's cancellation: %v", err)
	}
}

// TestSchemaCache_SharedCompilePanicBecomesSchemaError guards the DoChan
// migration: the shared compile now runs in its own goroutine, where an
// unrecovered compiler panic would terminate the process.
func TestSchemaCache_SharedCompilePanicBecomesSchemaError(t *testing.T) {
	t.Parallel()
	cache := repair.NewSchemaCache(repair.DefaultSchemaLimits())
	repair.ExportSetSchemaCacheCompile(cache, func(context.Context, json.RawMessage, repair.SchemaLimits) (*repair.CompiledSchema, error) {
		panic("compile boom")
	})
	_, err := cache.GetOrCompile(json.RawMessage(`{"type":"object"}`))
	var schemaErr *repair.SchemaError
	if !errors.As(err, &schemaErr) || schemaErr.ReasonCode != repair.ReasonCompilePanic {
		t.Fatalf("err=%v, want SchemaError reason=%s", err, repair.ReasonCompilePanic)
	}
}
