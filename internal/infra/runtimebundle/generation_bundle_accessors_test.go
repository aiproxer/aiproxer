package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestGenerationBundle_NilReceiverNeighboringAccessors characterizes and audits
// all neighboring pointer-receiver methods on (*GenerationBundle)(nil) (Requirements 1.1, 1.4).
// All neighboring accessors explicitly define safe zero-value returns for nil receivers.
func TestGenerationBundle_NilReceiverNeighboringAccessors(t *testing.T) {
	t.Parallel()

	var b *runtimebundle.GenerationBundle

	t.Run("Handler", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.Handler())
	})

	t.Run("ExecutorView", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.ExecutorView())
	})

	t.Run("ReadinessReport", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.ReadinessReport())
	})

	t.Run("BackendIDs", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.BackendIDs())
	})

	t.Run("BackendFactoryKindCounts", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.BackendFactoryKindCounts())
	})

	t.Run("Routing", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, runtimebundle.FrozenRoutingView{}, b.Routing())
	})

	t.Run("RoutePrefixes", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.RoutePrefixes())
	})

	t.Run("FrozenFrontends", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.FrozenFrontends())
	})

	t.Run("Registrations", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.Registrations())
	})

	t.Run("HTTPAuthProviders", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, b.HTTPAuthProviders())
	})

	t.Run("ResourceCount", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 0, b.ResourceCount())
	})

	t.Run("StartPublished", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, b.StartPublished(context.Background()))
	})

	t.Run("Quiesce", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, b.Quiesce(context.Background()))
	})

	t.Run("Close", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, b.Close())
	})

	t.Run("BindModelViews", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		assert.Equal(t, ctx, b.BindModelViews(ctx))
		assert.NotNil(t, b.BindModelViews(context.TODO()))
	})

	t.Run("TerminalProviders", func(t *testing.T) {
		t.Parallel()
		view := b.TerminalProviders()
		require.NotNil(t, view)
		prov, err := view.Resolve("test-provider", sdkterminal.WorkKindAppendFact)
		assert.Error(t, err)
		assert.Nil(t, prov)
	})
}
