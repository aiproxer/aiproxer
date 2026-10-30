package runtimebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessServices_CompactionDetectorInterfaceAndGenerationSharing(t *testing.T) {
	t.Parallel()

	// 1. Assert CompactionDetector is no longer on ProcessServices (owned by StandardFeatures).
	_, ok := reflect.TypeFor[*ProcessServices]().Elem().FieldByName("CompactionDetector")
	assert.False(t, ok, "ProcessServices must not have legacy CompactionDetector field")

	// 2. Assert field type on executorBuildInput is the runtime.CompactionDetector interface.
	buildField, ok := reflect.TypeFor[runtime.CorePorts]().FieldByName("CompactionDetector")
	require.True(t, ok, "CorePorts must have CompactionDetector field")
	assert.Equal(t, reflect.Interface, buildField.Type.Kind(), "CorePorts.CompactionDetector must be an interface")
	assert.Equal(t, "runtime.CompactionDetector", buildField.Type.String(), "CorePorts.CompactionDetector must be runtime.CompactionDetector")

	// 3. Test instantiation & no-closer invariant on ProcessServices.
	ctx := context.Background()
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	detector := ps.StandardFeatures.CompactionDetector()
	require.NotNil(t, detector, "StandardFeatures must instantiate CompactionDetector")
	_, implements := any(detector).(runtime.CompactionDetector)
	assert.True(t, implements, "ps.StandardFeatures.CompactionDetector must implement runtime.CompactionDetector")

	// Invariant: CompactionDetector itself has no Close method
	dType := reflect.TypeOf(detector)
	for m := range dType.Methods() {
		assert.NotEqual(t, "Close", m.Name, "CompactionDetector must not have a Close method")
	}

	// 4. Verify generation sharing: generation build inputs receive the exact same instance.
	buildInput := executorBuildInput{
		CorePorts: runtime.CorePorts{CompactionDetector: ps.StandardFeatures.CompactionDetector()},
	}
	assert.Equal(t, detector, buildInput.CorePorts.CompactionDetector, "generation build input must receive same process-owned detector reference")
}
