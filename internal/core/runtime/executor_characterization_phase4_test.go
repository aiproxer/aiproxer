package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestExecutor_execute_nilStoreReturnsInvalidArguments locks the nil-Store
// validation path (Execute phase 1, executor.go:229-230). Phase 4
// characterization for the executor collaborator extraction.
func TestExecutor_execute_nilStoreReturnsInvalidArguments(t *testing.T) {
	t.Parallel()
	exec := runtime.TestExecutor()
	_, err := exec.Execute(context.Background(), &aipapi.Call{ID: "test"})
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("want invalid arguments, got %v", err)
	}
}

// TestExecutor_execute_nilCallReturnsInvalidArguments locks the nil-call
// validation path (executor.go:229-230).
func TestExecutor_execute_nilCallReturnsInvalidArguments(t *testing.T) {
	t.Parallel()
	exec := runtime.TestExecutor()
	_, err := exec.Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("want invalid arguments, got %v", err)
	}
}

// TestExecutor_execute_callValidateFailure locks the call.Validate() failure
// path (executor.go:236-237). A zero-value Call with no ID and no messages
// must fail validation and surface "executor: validate call".
func TestExecutor_execute_callValidateFailure(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	exec := runtime.TestExecutor()
	exec.Store = st
	exec.Bus = hooks.New(hooks.Config{})
	_, err = exec.Execute(context.Background(), &aipapi.Call{})
	if err == nil || !strings.Contains(err.Error(), "executor: validate call") {
		t.Fatalf("want validate call error, got %v", err)
	}
}
