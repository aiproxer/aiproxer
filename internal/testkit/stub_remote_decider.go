package testkit

import (
	"context"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// StubRemoteDecider is a fixed [coreauth.RemoteDecider] for composition and runtimebundle tests.
type StubRemoteDecider struct {
	Decision sdkauth.Decision
	Err      error
}

// Decide implements [coreauth.RemoteDecider].
func (s *StubRemoteDecider) Decide(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = ctx
	_ = req
	if s == nil {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "nil_remote_stub"}, nil
	}
	if s.Err != nil {
		return sdkauth.Decision{}, s.Err
	}
	d := s.Decision
	if d.Outcome == sdkauth.OutcomeAllow && d.Principal.ID == "" {
		d.Principal = execview.PrincipalView{ID: "stub_remote_principal"}
	}
	return d, nil
}
