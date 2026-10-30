package auth

import (
	"context"
	"errors"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

type remoteDecisionStub struct {
	decision sdkauth.Decision
	err      error
}

func (s remoteDecisionStub) Decide(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	_ = ctx
	_ = req
	return s.decision, s.err
}

func TestPolicyAuthenticator_localNoop(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalNoop,
		Required: sdkauth.LevelNone,
		Noop: LocalNoOpAuthenticator{OS: fakeOSIdentity{snap: OSIdentitySnapshot{
			PrincipalID: "u1",
		}}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow || d.Principal.ID != "u1" {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_localAPIKey_only(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKey,
		APIKey:   a,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteAllow(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKey,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{ID: "remote-user"},
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow || d.Principal.ID != "remote-user" {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteDeny(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler: sdkauth.HandlerRemote,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome:    sdkauth.OutcomeDeny,
			ReasonCode: "remote_denied",
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteChallenge(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler: sdkauth.HandlerRemote,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome: sdkauth.OutcomeChallenge,
			Challenge: sdkauth.Challenge{
				Kind:       sdkauth.ChallengeSSORequired,
				ReasonCode: "sso",
				Summary:    "SSO required",
			},
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeChallenge || d.Challenge.Kind != sdkauth.ChallengeSSORequired {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteErrorFailClosed(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler: sdkauth.HandlerRemote,
		Remote:  remoteDecisionStub{err: errors.New("unavailable")},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_unavailable" {
		t.Fatalf("want deny remote_unavailable, got %+v", d)
	}
}

func TestPolicyAuthenticator_remote_apiKeySSO_withAPIKey_nilRemote_denies(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote:   nil,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "api_key_sso_misconfigured" {
		t.Fatalf("want deny api_key_sso_misconfigured, not remote_misconfigured, got %+v", d)
	}
}

func TestPolicyAuthenticator_remote_apiKeySSO_nilAPIKey_deniesWithoutRemote(t *testing.T) {
	t.Parallel()
	remoteCalled := false
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   nil,
		Remote: stubDeciderFunc(func(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
			remoteCalled = true
			return sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Principal: execview.PrincipalView{ID: "evil-bypass"}}, nil
		}),
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "Bearer x"})
	if err != nil {
		t.Fatal(err)
	}
	if remoteCalled {
		t.Fatal("Remote.Decide must not run when api_key_sso is required but APIKey authenticator is nil")
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "api_key_sso_misconfigured" {
		t.Fatalf("want deny api_key_sso_misconfigured, got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteUnusableAllow(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler: sdkauth.HandlerRemote,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{},
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_unusable_decision" {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_apiKeySSO_localFailSkipsRemote(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote: stubDeciderFunc(func(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
			called = true
			return sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Principal: execview.PrincipalView{ID: "r"}}, nil
		}),
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("remote must not run when local API key fails")
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_apiKeySSO_remoteChallenge(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome: sdkauth.OutcomeChallenge,
			Challenge: sdkauth.Challenge{
				Kind:    sdkauth.ChallengeSSORequired,
				Summary: "sign in",
			},
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeChallenge {
		t.Fatalf("got %+v", d)
	}
	if d.Challenge.Kind != sdkauth.ChallengeSSORequired {
		t.Fatalf("challenge: %+v", d.Challenge)
	}
}

func TestPolicyAuthenticator_apiKeySSO_remoteAllow(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "localp", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{ID: "ssouser"},
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("got %+v", d)
	}
	if d.SatisfiedLevel != sdkauth.LevelAPIKeySSO {
		t.Fatalf("SatisfiedLevel: got %v", d.SatisfiedLevel)
	}
	if d.Principal.ID != "ssouser" {
		t.Fatalf("want remote principal, got %+v", d.Principal)
	}
}

func TestPolicyAuthenticator_apiKeySSO_remoteErrorFailClosed(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote:   remoteDecisionStub{err: errors.New("boom")},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_unavailable" {
		t.Fatalf("got %+v", d)
	}
}

func TestPolicyAuthenticator_unknownHandler_denies(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerKind("bogus_future"),
		Required: sdkauth.LevelNone,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "unknown_auth_handler" {
		t.Fatalf("want deny unknown_auth_handler, got %+v", d)
	}
}

func TestPolicyAuthenticator_localNoop_nilNoop_denies(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalNoop,
		Required: sdkauth.LevelNone,
		Noop:     nil,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "local_noop_misconfigured" {
		t.Fatalf("want deny local_noop_misconfigured, got %+v", d)
	}
}

func TestPolicyAuthenticator_localAPIKey_nilAuthenticator_denies(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKey,
		APIKey:   nil,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "local_api_key_misconfigured" {
		t.Fatalf("want deny local_api_key_misconfigured, got %+v", d)
	}
}

func TestPolicyAuthenticator_remote_nilRemote_denies(t *testing.T) {
	t.Parallel()
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKey,
		Remote:   nil,
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_misconfigured" {
		t.Fatalf("want deny remote_misconfigured, got %+v", d)
	}
}

func TestPolicyAuthenticator_apiKeySSO_mergeRemote_unknownOutcome_denies(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "localp", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKeySSO,
		APIKey:   a,
		Remote: remoteDecisionStub{decision: sdkauth.Decision{
			Outcome: sdkauth.DecisionOutcome("future_outcome"),
		}},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{AuthorizationBearer: "test-local-api-key-16"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_unusable_decision" {
		t.Fatalf("want deny remote_unusable_decision, got %+v", d)
	}
}

func TestPolicyAuthenticator_remoteError_invokesOnRemoteDecideError(t *testing.T) {
	t.Parallel()
	var capturedErr error
	p := PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKey,
		Remote:   remoteDecisionStub{err: errors.New("remote-down")},
		OnRemoteDecideError: func(_ context.Context, err error) {
			capturedErr = err
		},
	}
	d, err := p.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny || d.ReasonCode != "remote_unavailable" {
		t.Fatalf("want deny remote_unavailable, got %+v", d)
	}
	if capturedErr == nil || capturedErr.Error() != "remote-down" {
		t.Fatalf("OnRemoteDecideError: want 'remote-down', got %v", capturedErr)
	}
}

func TestPolicyAuthenticator_implementsAuthenticator(t *testing.T) {
	t.Parallel()
	var _ Authenticator = PolicyAuthenticator{
		Handler: sdkauth.HandlerLocalNoop,
		Noop:    LocalNoOpAuthenticator{OS: fakeOSIdentity{snap: OSIdentitySnapshot{PrincipalID: "x"}}},
	}
}

type stubDeciderFunc func(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error)

func (f stubDeciderFunc) Decide(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return f(ctx, req)
}
