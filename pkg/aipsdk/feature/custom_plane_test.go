package feature_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// customExternalPlaneDeclaration is an external-like implementation of feature.PlaneDeclaration
// that does not implement any unexported hook target methods.
type customExternalPlaneDeclaration struct {
	id            string
	stageID       string
	order         int
	coalesceGroup string
}

func (c customExternalPlaneDeclaration) PlaneID() string                 { return c.id }
func (c customExternalPlaneDeclaration) ValidateDeclaration() error      { return nil }
func (c customExternalPlaneDeclaration) DiagnosticOrder() int            { return c.order }
func (c customExternalPlaneDeclaration) DiagnosticStageID() string       { return c.stageID }
func (c customExternalPlaneDeclaration) DiagnosticCoalesceGroup() string { return c.coalesceGroup }

// TestExternalPlaneDeclaration_SatisfiesInterface verifies that custom external types can satisfy
// feature.PlaneDeclaration without needing to implement any hook-target or unexported methods,
// and can be validated successfully by feature.ValidateManifest.
func TestExternalPlaneDeclaration_SatisfiesInterface(t *testing.T) {
	t.Parallel()

	var decl feature.PlaneDeclaration = customExternalPlaneDeclaration{
		id: "custom_external_plane",
	}
	require.NotNil(t, decl)
	assert.Equal(t, "custom_external_plane", decl.PlaneID())

	err := feature.ValidateManifest(decl)
	assert.NoError(t, err, "external PlaneDeclaration without hook target should validate cleanly in manifest")
}
