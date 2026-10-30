package aipsdk

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"gopkg.in/yaml.v3"
)

// DecodeAdmission bounds concurrent frontend decode work and weighted in-flight decode bytes.
//
// TryAcquire contract:
//   - ok=true, err=nil: capacity reserved; release is non-nil and must be called exactly once.
//   - ok=false, err=nil: saturated / rejected without waiting; release is nil and must not be called.
//   - ok=false, err!=nil: canceled, invalid weight, or overweight; release is nil and must not be called.
//   - ok=true with err!=nil is not a valid outcome.
//
// Nil receivers / nil DecodeAdmission values mean unlimited (custom/minimal mounts).
type DecodeAdmission interface {
	TryAcquire(ctx context.Context, weight int64) (release func(), ok bool, err error)
}

// BackendBuild is the opaque return type of [BackendFactory]. It aliases any on purpose:
// aipsdk must not import internal/core/runtime (AGENTS.md: core-owned types stay out of stable
// SDK surfaces). Official wiring in internal/pluginreg builds internal/core/execbackend.Backend
// values at the composition root; custom distributions may assert their own concrete backend wrapper instead.
// The alias documents that boundary while keeping registration signatures ergonomic for YAML-only factories.
type BackendBuild = any

// BackendFactory builds a backend adapter from opaque per-plugin YAML.
type BackendFactory func(n yaml.Node) (BackendBuild, error)

// ContinuationMountWiring supplies composition-root-owned continuation state to
// a frontend mount. Close is owned by the composition root and should be bound
// to the generation lifecycle by the mount coordinator.
type ContinuationMountWiring struct {
	Store    aipcont.Store
	Resolver aipcont.Resolver
	Close    func() error
}

// ContinuationMountWiringFactory creates one independent wiring instance for a
// mounted frontend generation. The factory receives immutable plugin identity
// and opaque configuration so the composition root can apply plugin-specific
// bounds without making frontend plugins depend on core packages.
type ContinuationMountWiringFactory func(frontendID, instanceID string, cfg yaml.Node) (ContinuationMountWiring, error)

// FrontendMountOptions carries runtime wiring for [FrontendMount] beyond the [http.ServeMux].
// Use composite literals with named fields at call sites.
type FrontendMountOptions struct {
	// PluginCfg is the opaque plugin-local YAML subtree for this frontend instance.
	PluginCfg yaml.Node
	// Exec is the runtime execution surface the mounted handler uses to submit canonical calls.
	// Real frontend mounts require a non-nil Exec.
	Exec ExecutorView
	// DefaultRoute is the selector used when the frontend protocol omits a route/header override.
	DefaultRoute string
	// RoutePrefixes are backend route-selector prefixes accepted from protocol model fields.
	RoutePrefixes []string
	// MaxRequestBodyBytes caps inbound HTTP request size. Zero means the frontend should use its
	// own default limit.
	MaxRequestBodyBytes int64
	// DecodeAdmission optionally bounds concurrent decode work and weighted in-flight decode bytes.
	// Nil means unlimited for custom/minimal mounts.
	DecodeAdmission DecodeAdmission
	// Authorizer optionally performs frontend-local authentication for direct mounts.
	// Standard HTTP composition authenticates in the outer transport middleware; a
	// custom/direct mount should provide this seam when it is externally reachable.
	Authorizer interface {
		Authenticate(context.Context, sdkauth.InboundCallMeta) (sdkauth.Decision, error)
	}
	// AllowUnauthenticated explicitly opts a direct mount into anonymous access.
	// It defaults to false; standard composition satisfies the default through its
	// outer transport-auth context.
	AllowUnauthenticated bool
	// TrafficPorts optionally emits client→proxy raw bytes after body read (design §10).
	TrafficPorts traffic.PortBundle
	// PreRequestKeepalive optionally emits standards-compliant HTTP informational keepalives
	// while streaming requests wait for pre-request admission to complete. It must not commit
	// final response status or body bytes.
	PreRequestKeepalive FrontendKeepaliveConfig
	// AuthErrorRenderer is an optional per-frontend hook for safe HTTP error bodies on transport
	// authentication failure (R4). When nil, the standard distribution uses the default safe JSON
	// renderer. For the standard binary, prefer [pluginreg.Registry.RegisterAuthErrorRenderer] keyed
	// by auth wire frontend id (see stdhttp/auth DefaultFrontendIDFromRequest); [runtimebundle.BuildOptions.AuthErrorRenderersByFrontend]
	// overrides registry entries per key. This field remains for custom mounts outside pluginreg.
	AuthErrorRenderer AuthErrorRenderer
	// GenerationContext is the runtime-owned lifecycle context of the generation this
	// frontend is mounted into. It cancels when the generation begins shutdown (quiesce
	// during a reload, or full server shutdown) and stays alive for the generation's
	// entire service life. Frontends that own long-lived transport state (WebSocket
	// sessions) must observe it and close their owned resources exactly once so retired
	// generations drain without leaks and new sessions never bind to a closing generation.
	// A nil value means the mount is not generation-bound (tests, custom minimal mounts).
	GenerationContext context.Context // ContinuationStore and ContinuationResolver are optional composition-root
	// injections for frontends that expose proxy-owned response continuation.
	// Minimal/direct mounts may omit them; the frontend then uses its bounded
	// protocol-neutral fallback store.
	ContinuationStore    aipcont.Store
	ContinuationResolver aipcont.Resolver
	// ContinuationWiring is an already-created lifecycle-owned resource. It takes
	// precedence over the factory when both are provided.
	ContinuationWiring *ContinuationMountWiring
	// ContinuationWiringFactory creates lifecycle-owned resources. The standard
	// composition requires GenerationContext when this factory returns Close.
	ContinuationWiringFactory ContinuationMountWiringFactory
	// FrontendInstanceID is the immutable configured instance identity passed to
	// ContinuationWiringFactory. Empty falls back to the factory ID.
	FrontendInstanceID string
	// HTTPHeaders is the resolved inbound header-name set (defaults plus aliases).
	// Zero value uses [DefaultHTTPHeaders].
	HTTPHeaders HTTPHeaders
	// StreamKeepaliveInterval is the recovery SSE keepalive interval. Zero uses
	// the stream package default (12s).
	StreamKeepaliveInterval time.Duration
	// LargePayload carries optional large-payload fast-path candidate configuration
	// for supported frontends.
	LargePayload any
	// Log is the host logger the mounted frontend uses for its own diagnostics
	// (decode, execute, encode, and cleanup failures). A frontend without a
	// logger cannot report an internal failure, so the standard composition
	// always supplies one. A nil value leaves the frontend on the standard
	// library default logger rather than dropping the record.
	Log *slog.Logger
}

type FrontendKeepaliveConfig struct {
	Enabled  bool
	Interval time.Duration
}

// FrontendMount registers HTTP routes for one frontend plugin instance.
type FrontendMount func(mux *http.ServeMux, opts FrontendMountOptions) error
