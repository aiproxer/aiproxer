package runtimebundle_test

import (
	"testing"

	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

func hostActiveExecutor(t *testing.T, host *runtimebundle.Host) *coreruntime.Executor {
	t.Helper()
	if host == nil {
		t.Fatal("nil host")
	}
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex
}
