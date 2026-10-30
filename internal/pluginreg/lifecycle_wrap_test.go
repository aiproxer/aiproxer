package pluginreg

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWrapLifecycleBackend_RejectsNilWrapperResult(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	const factoryID = "test-backend"

	err := reg.RegisterLifecycleBackend(factoryID, func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps BackendFactoryDeps) (BackendBuildResult, error) {
		return BackendBuildResult{}, nil
	})
	require.NoError(t, err)

	// Wrapping with a function that returns a nil factory must return an error and preserve the original factory.
	err = reg.WrapLifecycleBackend(factoryID, func(orig LifecycleBackendFactory) LifecycleBackendFactory {
		return nil
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil")

	// Building should still succeed using the original non-nil factory, rather than panicking on nil function.
	res, err := reg.BuildBackendWithLifecycle(factoryID, "inst-1", yaml.Node{}, nil, BackendFactoryDeps{})
	require.NoError(t, err)
	_ = res
}
