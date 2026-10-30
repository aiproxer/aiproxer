package auth

import (
	"context"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type stubAuthenticator struct{}

func (stubAuthenticator) Authenticate(_ context.Context, _ sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{
		Outcome:   sdkauth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "ok"},
	}, nil
}

type denyingRemoteStub struct{}

func (denyingRemoteStub) Decide(_ context.Context, _ sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "test-deny"}, nil
}

func TestAuthenticator_interface(t *testing.T) {
	t.Parallel()
	var _ Authenticator = stubAuthenticator{}
}

func TestRemoteDecider_interface(t *testing.T) {
	t.Parallel()
	var _ RemoteDecider = denyingRemoteStub{}
}
