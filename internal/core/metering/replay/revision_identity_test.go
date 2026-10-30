package replay

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRevisionIdentityIncludesRevisionAndInputHash(t *testing.T) {
	t.Parallel()
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	a, err := NewRevisionIdentity("customer", "b-leg", 1, hashA)
	require.NoError(t, err)
	b, err := NewRevisionIdentity("customer", "b-leg", 2, hashA)
	require.NoError(t, err)
	c, err := NewRevisionIdentity("customer", "b-leg", 1, hashB)
	require.NoError(t, err)
	d, err := NewRevisionIdentity("provider", "b-leg", 1, hashA)
	require.NoError(t, err)
	require.NotEqual(t, a.Key(), b.Key())
	require.NotEqual(t, a.Key(), c.Key())
	require.NotEqual(t, a.Key(), d.Key())
	require.True(t, a.Less(b))
	require.True(t, a.Less(c))
}

func TestRevisionIdentityRejectsInvalidHash(t *testing.T) {
	t.Parallel()
	_, err := NewRevisionIdentity("customer", "b-leg", 1, strings.Repeat("A", 64))
	require.ErrorIs(t, err, ErrInvalidRevisionIdentity)
	_, err = NewRevisionIdentity("customer", "b-leg", 1, " "+strings.Repeat("a", 64))
	require.ErrorIs(t, err, ErrInvalidRevisionIdentity)
}
