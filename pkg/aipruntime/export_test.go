package aipruntime

import (
	"context"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// ReloadQueryForTest is the coordinator/query seam shape used by external facade tests.
type ReloadQueryForTest interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
}

// NewReloadControlForTest constructs a public ReloadControl over a test query seam.
// Production composition binds the host reload/status methods through unexported helpers.
func NewReloadControlForTest(q ReloadQueryForTest) *ReloadControl {
	return newReloadControl(q)
}

// HostAPIForTest is the narrow host seam used by public Runtime tests.
// It mirrors the private hostAPI contract without exposing internal types.
type HostAPIForTest = hostAPI

// NewRuntimeWithHostForTest constructs a Runtime over a test host seam.
// Production Build wraps [*runtimebundle.Host]; tests must not recreate that shape.
func NewRuntimeWithHostForTest(h HostAPIForTest) *Runtime {
	if h == nil {
		return &Runtime{}
	}
	return &Runtime{host: h}
}
