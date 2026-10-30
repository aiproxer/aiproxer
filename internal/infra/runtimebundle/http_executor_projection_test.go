package runtimebundle

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	adminaccounting "github.com/aiproxer/aiproxer/internal/stdhttp/admin/tokenaccounting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projectionCountingLocalCounter returns a fixed token count per counting surface
// so a test can tell which service answered.
type projectionCountingLocalCounter struct{ input int }

func (c projectionCountingLocalCounter) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: c.input, TotalTokens: c.input}, nil
}

func (c projectionCountingLocalCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: c.input, TotalTokens: c.input}, nil
}

func (c projectionCountingLocalCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: c.input, TotalTokens: c.input}, nil
}

// TestBuildStandardHTTPInputNilExecutorStaysNilInterface pins the typed-nil
// guard: a candidate assembly without a concrete executor must project a nil
// interface, never a non-nil ExecutorView over a nil pointer, so the HTTP
// adapter's nil-executor guards keep firing on the zero input.
func TestBuildStandardHTTPInputNilExecutorStaysNilInterface(t *testing.T) {
	t.Parallel()

	in := buildStandardHTTPInput(context.Background(), &candidateAssembly{}, nil, nil, "")
	// Direct interface comparisons, not assert.Nil: testify's Nil unwraps an
	// interface and also accepts a non-nil interface over a nil *Executor, so
	// it cannot see the typed-nil projection this test exists to reject.
	if in.Core.Executor != nil {
		t.Fatalf("a nil concrete executor must stay a nil interface, got %T", in.Core.Executor)
	}
	if in.Frontends.Executor != nil {
		t.Fatalf("Frontends.Executor must stay a nil interface, got %T", in.Frontends.Executor)
	}
	if in.Operations.RouteTrace != nil {
		t.Fatalf("Operations.RouteTrace must stay nil for a zero candidate, got %T", in.Operations.RouteTrace)
	}
	if in.Operations.ConcurrencyProvider != nil {
		t.Fatalf("Operations.ConcurrencyProvider must stay a nil interface, got %T", in.Operations.ConcurrencyProvider)
	}
}

// TestBuildStandardHTTPInputProjectsExecutorSurfaces proves the composition
// root projects the narrow executor surfaces: the ExecutorView, the same
// route-trace buffer installed on the executor, the lease provider, and the
// admin count service preferring the explicit service then the executor's.
func TestBuildStandardHTTPInputProjectsExecutorSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	explicit := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, projectionCountingLocalCounter{input: 11})
	executorCounts := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, projectionCountingLocalCounter{input: 22})
	ex := coreruntime.TestExecutor()
	ex.AdminCountService = executorCounts
	ex.RouteTrace = diag.NewRouteTraceBuffer(4)

	cand := &candidateAssembly{
		execution:  candidateExecutionGroup{executor: ex},
		operations: candidateOperationsGroup{tokenAccountingAdmin: explicit},
	}
	in := buildStandardHTTPInput(ctx, cand, nil, nil, "")
	assert.Equal(t, ex, in.Core.Executor)
	assert.Equal(t, ex, in.Frontends.Executor)
	assert.Same(t, ex.RouteTrace, in.Operations.RouteTrace)
	assert.Equal(t, ex.ConcurrencyProvider, in.Operations.ConcurrencyProvider)

	svc := in.Operations.TokenAccountingAdmin
	require.NotNil(t, svc)
	resp, err := svc.Count(ctx, adminaccounting.CountRequest{Backend: "stub", Model: "model"})
	require.NoError(t, err)
	require.Len(t, resp.Planes, 1)
	assert.Equal(t, 11, resp.Planes[0].Tokens.Input, "the explicit admin service must win")

	// Explicit service absent: the executor's count service is the fallback.
	cand.operations.tokenAccountingAdmin = nil
	in = buildStandardHTTPInput(ctx, cand, nil, nil, "")
	svc = in.Operations.TokenAccountingAdmin
	require.NotNil(t, svc)
	resp, err = svc.Count(ctx, adminaccounting.CountRequest{Backend: "stub", Model: "model"})
	require.NoError(t, err)
	require.Len(t, resp.Planes, 1)
	assert.Equal(t, 22, resp.Planes[0].Tokens.Input, "the executor count service must be the fallback")

	// Neither service: nothing to mount.
	ex.AdminCountService = nil
	in = buildStandardHTTPInput(ctx, cand, nil, nil, "")
	assert.Nil(t, in.Operations.TokenAccountingAdmin)
}
