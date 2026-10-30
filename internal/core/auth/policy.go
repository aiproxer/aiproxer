package auth

import (
	"context"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// PolicyAuthenticator routes inbound metadata to the configured local handler or
// [RemoteDecider] and enforces api_key_sso sequencing (local API key then remote).
type PolicyAuthenticator struct {
	Handler  sdkauth.HandlerKind
	Required sdkauth.RequiredLevel
	Noop     Authenticator
	APIKey   Authenticator
	Remote   RemoteDecider
	// OnRemoteDecideError, if set, is invoked when [RemoteDecider].Decide returns a non-nil error
	// before the policy maps the outcome to deny (err is not returned to the transport). Used for
	// observability at the composition root; [internal/core/auth] does not import logging.
	OnRemoteDecideError func(ctx context.Context, err error)
}

// Authenticate implements [Authenticator].
func (a PolicyAuthenticator) Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	switch a.Handler {
	case sdkauth.HandlerLocalNoop:
		if a.Noop == nil {
			return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "local_noop_misconfigured"}, nil
		}
		return a.Noop.Authenticate(ctx, req)

	case sdkauth.HandlerLocalAPIKey:
		if a.Required == sdkauth.LevelAPIKeySSO {
			return a.authenticateAPIKeySSO(ctx, req)
		}
		if a.APIKey == nil {
			return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "local_api_key_misconfigured"}, nil
		}
		return a.APIKey.Authenticate(ctx, req)

	case sdkauth.HandlerRemote:
		// api_key_sso always requires a local API-key leg and a remote; do not fall through to remote-only auth.
		if a.Required == sdkauth.LevelAPIKeySSO {
			if a.APIKey == nil || a.Remote == nil {
				return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "api_key_sso_misconfigured"}, nil
			}
			return a.authenticateAPIKeySSO(ctx, req)
		}
		return a.authenticateRemote(ctx, req)

	default:
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "unknown_auth_handler"}, nil
	}
}

func (a PolicyAuthenticator) authenticateAPIKeySSO(
	ctx context.Context,
	req sdkauth.InboundCallMeta,
) (sdkauth.Decision, error) {
	if a.APIKey == nil || a.Remote == nil {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "api_key_sso_misconfigured"}, nil
	}
	local, err := a.APIKey.Authenticate(ctx, req)
	if err != nil {
		return sdkauth.Decision{}, err
	}
	if local.Outcome != sdkauth.OutcomeAllow {
		return local, nil
	}
	return a.mergeRemoteAfterLocalAllow(ctx, req, local)
}

func (a PolicyAuthenticator) authenticateRemote(
	ctx context.Context,
	req sdkauth.InboundCallMeta,
) (sdkauth.Decision, error) {
	if a.Remote == nil {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_misconfigured"}, nil
	}
	d, err := a.Remote.Decide(ctx, req)
	if err != nil {
		// Fail closed without returning a transport error: stdhttp maps non-nil Authenticate errors
		// to HTTP 500; callers must get a deny decision on the success path so the adapter can
		// emit events and render protocol-safe terminal responses (Req 7.3 / 12.6).
		if a.OnRemoteDecideError != nil {
			a.OnRemoteDecideError(ctx, err)
		}
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_unavailable"}, nil
	}
	if d.Outcome == sdkauth.OutcomeAllow && strings.TrimSpace(d.Principal.ID) == "" {
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_unusable_decision"}, nil
	}
	return d, nil
}

func (a PolicyAuthenticator) mergeRemoteAfterLocalAllow(
	ctx context.Context,
	req sdkauth.InboundCallMeta,
	local sdkauth.Decision,
) (sdkauth.Decision, error) {
	d, err := a.authenticateRemote(ctx, req)
	if err != nil {
		return sdkauth.Decision{}, err
	}
	switch d.Outcome {
	case sdkauth.OutcomeDeny, sdkauth.OutcomeChallenge:
		return d, nil
	case sdkauth.OutcomeAllow:
		out := d
		out.SatisfiedLevel = sdkauth.LevelAPIKeySSO
		if strings.TrimSpace(out.Principal.ID) == "" {
			out.Principal = local.Principal
		}
		if out.Device.KeyID == "" && out.Device.Fingerprint == "" && out.Device.ID == "" {
			out.Device = local.Device
		}
		return out, nil
	default:
		return sdkauth.Decision{Outcome: sdkauth.OutcomeDeny, ReasonCode: "remote_unusable_decision"}, nil
	}
}
