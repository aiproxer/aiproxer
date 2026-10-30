// Package contract defines the cycle-neutral HTTP composition input value
// types shared by runtimebundle (canonical construction) and stdhttp
// (canonical and transitional composition). It owns no lifecycle, imports no
// runtimebundle, and imports no root stdhttp (task 3.4 dependency-direction
// contract).
package contract

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	ssessiondiag "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	adminkeepwarm "github.com/aiproxer/aiproxer/internal/stdhttp/admin/keepwarm"
	adminaccounting "github.com/aiproxer/aiproxer/internal/stdhttp/admin/tokenaccounting"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	aipcp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

// StandardHTTPInput is the focused, lifecycle-free HTTP composition projection.
type StandardHTTPInput struct {
	Core       HTTPCoreInput
	Security   HTTPSecurityInput
	Operations HTTPOperationsInput
	Models     HTTPModelInput
	Frontends  HTTPFrontendInput
}

type HTTPCoreInput struct {
	// Executor is assigned only from a non-nil concrete executor.
	Executor aipsdk.ExecutorView
}

// HTTPSecurityInput uses adapter-owned narrow interfaces (no root core app imports).
type HTTPSecurityInput struct {
	HTTPAuthProviders    []httpauth.Provider
	SecureSessionStore   ssessiondiag.Store
	UsageAuthority       cpadmin.AccountingAuthorityQueries
	ConcurrencyAuthority cpadmin.ConcurrencyAuthorityQueries
	GeoIP                GeoIPSecurityInput
	SelfDefense          SelfDefenseSecurityInput
}

// GeoIPResolverConfig carries only generation-scoped address-source policy.
// Fixed parser bounds are owned by the HTTP adapter and are not caller knobs.
type GeoIPResolverConfig struct {
	Source         config.ClientIPSource
	TrustedProxies []netip.Prefix
}

// GeoIPObserver receives finite-label decision outcomes only.
type GeoIPObserver interface {
	Decision(coregeoip.Reason, bool)
}

// GeoIPSecurityInput is a non-owning projection of the process lookup service
// and immutable generation policy into the standard data-plane handler graph.
type GeoIPSecurityInput struct {
	Policy   *coregeoip.Policy
	Lookup   coregeoip.CountryLookup
	Resolver GeoIPResolverConfig
	Observer GeoIPObserver
}

// HTTPOperationsInput carries the generation's operation surfaces. Billing
// admin surfaces do not appear here: they are registered as protected
// [AdminMount] values instead (task 4.2), so the HTTP layer never imports the
// billing domain.
type HTTPOperationsInput struct {
	// AdminMounts are caller-registered operator handlers. Each is mounted
	// under /admin/ behind the diagnostics shared-secret protection; the
	// registered literal paths are validated before any handler is mounted.
	AdminMounts []AdminMount
	Metrics     *metrics.Bundle
	// RouteTrace is the composition-root-owned ring the route-trace mount serves.
	RouteTrace           *diag.RouteTraceBuffer
	Store                diag.AttemptLoader
	SecretGuardInventory *diag.InventoryExtras
	ControlPlaneQueries  aipcp.Queries
	ReadinessReport      aipcp.ReadinessReportReader
	// ConcurrencyProvider is the projected logical-request lease authority.
	ConcurrencyProvider    authority.ConcurrencyProvider
	TokenAccountingAdmin   adminaccounting.Service
	RouteOverrideAdmin     http.Handler
	KeepwarmAdmin          adminkeepwarm.Options
	KeepwarmAdminEnabled   bool
	Registrations          []aipsdk.Registration
	TerminalDecisionPolicy TerminalDecisionPolicyInput
	// ProxyMemory is the feature-host-projected handler that owns
	// /v1/aip/memory and its subtree; a nil handler leaves the routes unmounted.
	ProxyMemory http.Handler
}

// AdminMount is one operator handler the standard HTTP layer mounts behind
// the existing admin authentication.
//
// Path must be a literal absolute path below /admin/ (no ServeMux method/host
// pattern, wildcard, backslash, percent escape, query or fragment, control or
// whitespace character, dot segment, or empty segment). The HTTP layer owns
// that validation, rejects duplicate, nested, built-in, and
// configured-path-colliding registrations at build time, and mounts the
// handler with the mount path prefix stripped behind the diagnostics
// shared-secret wrapper; an empty shared secret mounts nothing.
type AdminMount struct {
	Path    string
	Handler http.Handler
}

type HTTPModelInput struct {
	CatalogRuntime       *modelcatalog.CatalogRuntime
	ModelRegistryRuntime *modelregistry.Runtime
}

type HTTPFrontendInput struct {
	Executor             aipsdk.ExecutorView
	Registry             *pluginreg.Registry
	DefaultRouteSelector string
	RoutePrefixes        []string
	Plugins              []config.PluginConfig
	MaxRequestBodyBytes  int64
	DecodeAdmission      aipsdk.DecodeAdmission
	TrafficPorts         traffic.PortBundle
	PreRequestKeepalive  aipsdk.FrontendKeepaliveConfig
	// HTTPHeaders is the resolved inbound header-name set for this generation.
	HTTPHeaders aipsdk.HTTPHeaders
	// StreamKeepaliveInterval is the recovery SSE keepalive interval.
	StreamKeepaliveInterval time.Duration
	// GenerationContext is the runtime-owned generation lifecycle context. It
	// cancels when the generation begins shutdown (quiesce/drain) and is passed
	// to every mounted frontend through [aipsdk.FrontendMountOptions] so
	// long-lived transports (WebSocket sessions) close exactly once on retire.
	GenerationContext context.Context
	// ContinuationWiringFactory creates composition-root-owned continuation
	// state for each frontend mount generation.
	ContinuationWiringFactory aipsdk.ContinuationMountWiringFactory
	// FrontendRouteClaims optionally maps a frontend factory id to a provider
	// that computes the normalized owner-aware route claims for one frontend
	// instance. When set, [stdhttp.MountBundledFrontends] validates route
	// ownership (owner-aware claims + canonical path takeover) in a
	// generation-scoped RouteRegistry BEFORE any ServeMux handler is mounted; a
	// conflict fails the candidate atomically with both-owner diagnostics.
	// Frontends without a provider participate in no route-ownership
	// validation, so the seam stays fully generic plugin architecture.
	FrontendRouteClaims map[string]FrontendRouteClaims
	// LargePayload carries optional large-payload fast-path candidate configuration
	// for supported frontends.
	LargePayload LargePayloadInput
	// Log is the host logger handed to every mounted frontend.
	Log *slog.Logger
}

// FrontendRouteClaims computes the normalized owner-aware route claims for one
// enabled frontend instance. instanceID is the immutable owner identity used
// for the route-ownership registry; cfg is the plugin-local config subtree.
type FrontendRouteClaims func(instanceID string, cfg yaml.Node) ([]RouteClaim, error)

// TrafficSnapshot is the narrow capability a generation extension snapshot
// exposes for traffic-port projection. Both runtimebundle's frozen extension
// snapshot and stdhttp test fixtures satisfy it structurally.
type TrafficSnapshot interface {
	RawCapture() traffic.RawCaptureSink
	TrafficObserver() traffic.Observer
	TrafficRedactors() []traffic.Redactor
}

// TrafficPortsFromSnapshot projects a TrafficSnapshot into an immutable
// traffic.PortBundle with a defensive redactor-slice copy.
func TrafficPortsFromSnapshot(snap TrafficSnapshot) traffic.PortBundle {
	if snap == nil {
		return traffic.PortBundle{}
	}
	return traffic.PortBundle{Raw: snap.RawCapture(), Obs: snap.TrafficObserver(), Red: CloneTrafficRedactors(snap.TrafficRedactors())}
}

// CloneHTTPAuthProviders returns a defensive copy of an auth provider slice.
func CloneHTTPAuthProviders(in []httpauth.Provider) []httpauth.Provider {
	if in == nil {
		return nil
	}
	return append([]httpauth.Provider(nil), in...)
}

// CloneAdminMounts returns a defensive copy of registered admin mounts so a
// caller mutating its slice after projection cannot change what a generation
// serves.
func CloneAdminMounts(in []AdminMount) []AdminMount {
	if in == nil {
		return nil
	}
	return append([]AdminMount(nil), in...)
}

// CloneStrings returns a defensive copy of a string slice.
func CloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

// CloneTrafficRedactors returns a defensive copy of a redactor slice.
func CloneTrafficRedactors(in []traffic.Redactor) []traffic.Redactor {
	if in == nil {
		return nil
	}
	return append([]traffic.Redactor(nil), in...)
}

// ClonePluginConfigs returns a deep defensive copy of plugin configuration
// rows, including their nested YAML config nodes.
func ClonePluginConfigs(in []config.PluginConfig) []config.PluginConfig {
	if in == nil {
		return nil
	}
	out := make([]config.PluginConfig, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config = CloneYAMLNode(in[i].Config)
	}
	return out
}

// CloneRegistrations returns a deep defensive copy of plugin registrations,
// including their nested YAML config nodes.
func CloneRegistrations(in []aipsdk.Registration) []aipsdk.Registration {
	if in == nil {
		return nil
	}
	out := make([]aipsdk.Registration, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config.Node = CloneYAMLNode(in[i].Config.Node)
	}
	return out
}

// CloneYAMLNode returns a deep defensive copy of a YAML node, including
// aliases and nested content.
func CloneYAMLNode(n yaml.Node) yaml.Node {
	out := n
	if n.Alias != nil {
		cloned := CloneYAMLNode(*n.Alias)
		out.Alias = &cloned
	}
	if len(n.Content) == 0 {
		out.Content = nil
		return out
	}
	out.Content = make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		if c == nil {
			continue
		}
		cloned := CloneYAMLNode(*c)
		out.Content[i] = &cloned
	}
	return out
}
