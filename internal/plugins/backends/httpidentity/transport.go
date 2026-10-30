package httpidentity

import (
	"context"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/identity"
)

const headerUserAgent = "User-Agent"

// Transport is an http.RoundTripper that sets, replaces, or omits User-Agent
// according to Policy on every outbound request.
type Transport struct {
	Base   http.RoundTripper
	Policy identity.FieldPolicy
}

var _ http.RoundTripper = (*Transport)(nil)

// WrapClient returns a shallow clone of client whose Transport applies policy.
// The input client is never mutated. Nil client yields nil.
func WrapClient(client *http.Client, policy identity.FieldPolicy) *http.Client {
	if client == nil {
		return nil
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out := *client
	out.Transport = &Transport{Base: base, Policy: policy}
	return &out
}

// RoundTrip clones req, applies User-Agent policy, and delegates to Base.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if req == nil {
		return base.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	if out.Header == nil {
		out.Header = make(http.Header)
	} else {
		out.Header = out.Header.Clone()
	}
	applyUserAgent(out.Context(), out.Header, t.Policy)
	return base.RoundTrip(out)
}

func applyUserAgent(ctx context.Context, h http.Header, policy identity.FieldPolicy) {
	value, omit := resolveUserAgent(ctx, policy)
	if omit {
		// Explicit nil entry suppresses Go's Go-http-client/1.1 default.
		h[headerUserAgent] = nil
		return
	}
	h.Set(headerUserAgent, value)
}

func resolveUserAgent(ctx context.Context, policy identity.FieldPolicy) (value string, omit bool) {
	switch policy.Mode {
	case identity.ModeDrop:
		return "", true
	case identity.ModeCustom:
		return policy.Value, false
	case identity.ModePassthrough:
		ua, isCall := identity.CallClientUserAgent(ctx)
		if isCall {
			accepted, ok := identity.AcceptClientUserAgent(ua)
			if !ok {
				return "", true
			}
			return accepted, false
		}
		// Unmarked or explicit background: never forward client identity.
		return identity.DefaultProductName, false
	default: // ModeProxy and empty/unknown → product identity
		return identity.DefaultProductName, false
	}
}
