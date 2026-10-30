package feature

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaterializeRequestSlice_ClonesMaterializerOutput(t *testing.T) {
	t.Parallel()

	shared := []string{"before-a", "before-b"}
	materializeCalls := 0

	got := materializeRequestSlice(
		[]string{"ignored-input"},
		func([]string) []string {
			materializeCalls++
			return shared // intentionally aliased output
		},
	)

	require.Equal(t, 1, materializeCalls)
	require.Equal(t, []string{"before-a", "before-b"}, got)

	shared[0] = "mutated-a"
	shared[1] = "mutated-b"

	assert.Equal(t, []string{"before-a", "before-b"}, got)
}

func TestMaterializeRequestSlice_ShapePreservation(t *testing.T) {
	t.Parallel()

	t.Run("materializer returns nil preserves nil", func(t *testing.T) {
		t.Parallel()
		got := materializeRequestSlice(
			[]string{"some-input"},
			func([]string) []string {
				return nil
			},
		)
		assert.Nil(t, got)
	})

	t.Run("materializer returns non-nil empty preserves non-nil empty", func(t *testing.T) {
		t.Parallel()
		got := materializeRequestSlice(
			[]string{"some-input"},
			func([]string) []string {
				return []string{}
			},
		)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("nil materializer fallback clones source and isolates mutation", func(t *testing.T) {
		t.Parallel()
		source := []string{"initial-1", "initial-2"}
		got := materializeRequestSlice(source, nil)
		require.Equal(t, []string{"initial-1", "initial-2"}, got)

		source[0] = "mutated-1"
		assert.Equal(t, []string{"initial-1", "initial-2"}, got)
	})
}
