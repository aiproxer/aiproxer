package runtimebundle

import coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"

// BootstrapApp is the standard-distribution bootstrap application from [NewBootstrapApp]
// ([coreruntime.App]).
type BootstrapApp = coreruntime.App

// BootstrapOptions is [coreruntime.Options] for explicit root wiring.
type BootstrapOptions = coreruntime.Options

// NewBootstrapApp delegates to [coreruntime.New] so cmd/aipstd can call bootstrap without
// a direct import of internal/core/runtime.
func NewBootstrapApp(opts BootstrapOptions) (*BootstrapApp, error) {
	return coreruntime.New(opts)
}
