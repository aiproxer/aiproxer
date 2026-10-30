package featurehost

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	cv "github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/stretchr/testify/require"
)

type registrationFailure struct{ *cv.AuthorizedReferenceStore }

func TestConversationBootstrap_UnsupportedStoreDoesNotAdvertiseAuthority(t *testing.T) {
	t.Parallel()
	_, ok := wrapConversationStore(cv.NewReferenceStore()).(cv.BootstrapStore)
	require.False(t, ok)
}

func (registrationFailure) CreateALeg(context.Context, string) error { return cv.ErrALegNotFound }

func TestConversationBootstrap_RegistrationFailureRejectsBeforeDecision(t *testing.T) {
	t.Parallel()
	a, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := a.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	s := wrapConversationStore(registrationFailure{cv.NewAuthorizedReferenceStore(cv.NewReferenceStore(), a)})
	atomic, ok := s.(cv.BootstrapStore)
	require.True(t, ok)
	out, err := atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", func() (cv.BootstrapDecision, error) {
		t.Error("callback invoked after failed registration")
		return cv.BootstrapDecision{Outcome: cv.BootstrapNoMatch}, nil
	})
	require.ErrorIs(t, err, cv.ErrALegNotFound)
	require.Empty(t, out.Mutations)
}

func TestConversationBootstrap_WrapperRegistersMemoryAndForwardsCompletion(t *testing.T) {
	t.Parallel()
	a, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := a.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	s := wrapConversationStore(cv.NewAuthorizedReferenceStore(cv.NewReferenceStore(), a))
	atomic, ok := s.(cv.BootstrapStore)
	require.True(t, ok)
	out, err := atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", func() (cv.BootstrapDecision, error) { return cv.BootstrapDecision{Outcome: cv.BootstrapNoMatch}, nil })
	require.NoError(t, err)
	require.False(t, out.Reused)
	out, err = atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", nil)
	require.NoError(t, err)
	require.True(t, out.Reused)
}
