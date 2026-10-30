package aipsdk

import (
	"context"
	"net/http"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// AuthErrorRenderInput carries semantic auth state plus transport-facing fields for mapping a
// decision to a safe client-visible HTTP response. Renderers must not re-authenticate or read
// raw bearer material.
//
// Canonical definitions live in package aipsdk (not pkg/aipsdk/auth) so internal/core can
// reference [FrontendMountOptions.AuthErrorRenderer] without pulling pkg/aipsdk/transport into
// the orchestration dependency closure; [github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth]
// aliases these types for transport-only call sites.
type AuthErrorRenderInput struct {
	FrontendID  string
	RequestPath string
	Decision    sdkauth.Decision
	// DefaultStatus is the status suggested by the adapter (e.g. 401, 403, 503) before rendering.
	DefaultStatus    int
	ChallengeHeaders http.Header
	// Policy and trace fields are optional; default renderers use them for safe messaging only.
	AccessMode    sdkauth.AccessMode
	HandlerKind   sdkauth.HandlerKind
	RequiredLevel sdkauth.RequiredLevel
	TraceID       string
	RemoteAddr    string
}

// AuthErrorRenderResult is a single terminal HTTP response (reject or challenge).
// Headers and Body must not include secrets or fine-grained existence-revealing key material.
type AuthErrorRenderResult struct {
	Status      int
	Headers     http.Header
	ContentType string
	Body        []byte
}

// AuthErrorRenderer maps denied or challenged decisions into a safe transport response
// (implemented by the stdhttp layer and optional frontend hooks).
type AuthErrorRenderer interface {
	RenderAuthError(ctx context.Context, in AuthErrorRenderInput) AuthErrorRenderResult
}
