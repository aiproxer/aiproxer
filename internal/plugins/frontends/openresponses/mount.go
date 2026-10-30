package openresponses

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

const (
	RouteOperationCreate    httpcontract.RouteKind = "openresponses_create"
	RouteOperationCompact   httpcontract.RouteKind = "openresponses_compact"
	RouteOperationWebSocket httpcontract.RouteKind = "openresponses_websocket"
)

// RouteClaims calculates the normalized route claims for an OpenResponses frontend config.
func RouteClaims(cfg Config) ([]httpcontract.RouteClaim, error) {
	return RouteClaimsForOwner(cfg, ID)
}

// RouteClaimsForOwner returns claims with an explicit immutable owner identity.
func RouteClaimsForOwner(cfg Config, ownerID string) ([]httpcontract.RouteClaim, error) {
	if strings.ContainsAny(ownerID, "\r\n\t") {
		return nil, fmt.Errorf("openresponses: owner identity contains control characters")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("openresponses: invalid config: %w", err)
	}
	claims, err := httpcontract.ClaimsForBasePath(ownerID, cfg.BasePath,
		httpcontract.RouteClaim{Method: http.MethodPost, Path: "/responses", Kind: RouteOperationCreate},
		httpcontract.RouteClaim{Method: http.MethodPost, Path: "/responses/compact", Kind: RouteOperationCompact},
		httpcontract.RouteClaim{Method: http.MethodGet, Path: "/responses", Kind: RouteOperationWebSocket},
	)
	if err != nil {
		return nil, err
	}

	if !cfg.WebSocket.IsEnabled() {
		filtered := make([]httpcontract.RouteClaim, 0, len(claims))
		for _, c := range claims {
			if c.Kind != RouteOperationWebSocket {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	return claims, nil
}

// RegisterClaims validates and atomically adds this frontend's claims to a registry.
// No handler is mounted by this operation.
func RegisterClaims(reg *httpcontract.RouteRegistry, cfg Config) ([]httpcontract.RouteClaim, error) {
	if reg == nil {
		return nil, fmt.Errorf("openresponses: nil route registry")
	}
	claims, err := RouteClaims(cfg)
	if err != nil {
		return nil, err
	}
	// Validate against a copy first so a conflict cannot leave a partial registration.
	staged := httpcontract.NewRouteRegistry()
	if err := staged.RegisterAll(reg.Claims()); err != nil {
		return nil, err
	}
	if err := staged.RegisterAll(claims); err != nil {
		return nil, err
	}
	if err := reg.RegisterAll(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// RegisterClaimsForOwner is the composition-root form when a frontend instance
// has an operator-assigned owner ID.
func RegisterClaimsForOwner(reg *httpcontract.RouteRegistry, cfg Config, ownerID string) ([]httpcontract.RouteClaim, error) {
	if reg == nil {
		return nil, fmt.Errorf("openresponses: nil route registry")
	}
	claims, err := RouteClaimsForOwner(cfg, ownerID)
	if err != nil {
		return nil, err
	}
	staged := httpcontract.NewRouteRegistry()
	if err := staged.RegisterAll(reg.Claims()); err != nil {
		return nil, err
	}
	if err := staged.RegisterAll(claims); err != nil {
		return nil, err
	}
	if err := reg.RegisterAll(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// RouteDiagnostics returns stable, sanitized diagnostics for this validated config.
func RouteDiagnostics(cfg Config) []httpcontract.RouteDiagnostic {
	claims, err := RouteClaims(cfg)
	if err != nil {
		return nil
	}
	reg := httpcontract.NewRouteRegistry()
	if err := reg.RegisterAll(claims); err != nil {
		return nil
	}
	return httpcontract.RouteDiagnosticsFromRegistry(reg)
}

// Mount registers the OpenResponses API handler and route claims on mux.
func Mount(mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	cfg, err := DecodeConfig(opts.PluginCfg)
	if err != nil {
		return err
	}

	claims, err := RouteClaims(cfg)
	if err != nil {
		return err
	}

	// Prefer lifecycle-owned composition-root continuation wiring. Direct/minimal
	// mounts retain a bounded SDK fallback so the plugin never imports core.
	store := opts.ContinuationStore
	resolver := opts.ContinuationResolver
	var ownedWiring aipsdk.ContinuationMountWiring
	if opts.ContinuationWiring == nil && opts.ContinuationWiringFactory != nil {
		instanceID := opts.FrontendInstanceID
		if instanceID == "" {
			instanceID = ID
		}
		ownedWiring, err = opts.ContinuationWiringFactory(ID, instanceID, opts.PluginCfg)
		if err != nil {
			return err
		}
		if ownedWiring.Store != nil {
			store = ownedWiring.Store
		}
		if ownedWiring.Resolver != nil {
			resolver = ownedWiring.Resolver
		}
	}
	if opts.ContinuationWiring != nil {
		if opts.ContinuationWiring.Store != nil {
			store = opts.ContinuationWiring.Store
		}
		if opts.ContinuationWiring.Resolver != nil {
			resolver = opts.ContinuationWiring.Resolver
		}
	}
	var fallbackStore *aipcont.MemoryStore
	if store == nil {
		var bounds aipcont.Bounds
		fallbackStore, bounds = newConfigContinuationStore(cfg)
		store = fallbackStore
		if resolver == nil {
			resolver = aipcont.NewResolver(store, bounds)
		}
	}
	if opts.GenerationContext != nil && fallbackStore != nil {
		_ = context.AfterFunc(opts.GenerationContext, func() { _ = fallbackStore.Close() })
	}
	activeClose := ownedWiring.Close
	if opts.ContinuationWiring != nil {
		activeClose = opts.ContinuationWiring.Close
	}
	if activeClose != nil {
		if opts.GenerationContext == nil {
			_ = activeClose()
			return fmt.Errorf("openresponses: continuation wiring requires generation context for lifecycle ownership")
		}
		_ = context.AfterFunc(opts.GenerationContext, func() { _ = activeClose() })
	}
	var lpCfg frontendpipe.LargePayloadConfig
	if c, ok := opts.LargePayload.(frontendpipe.LargePayloadConfig); ok {
		lpCfg = c
	}
	handler := NewHandler(HandlerConfig{
		Executor:                opts.Exec,
		DefaultRouteSelector:    opts.DefaultRoute,
		RoutePrefixes:           opts.RoutePrefixes,
		MaxRequestBodyBytes:     opts.MaxRequestBodyBytes,
		ProtocolLimits:          proto.DefaultLimits(),
		DecodeAdmission:         opts.DecodeAdmission,
		Authorizer:              opts.Authorizer,
		RequireAuthentication:   !opts.AllowUnauthenticated,
		AllowUnauthenticated:    opts.AllowUnauthenticated,
		TrafficPorts:            opts.TrafficPorts,
		PreRequestKeepalive:     opts.PreRequestKeepalive,
		ContinuationStore:       store,
		ContinuationResolver:    resolver,
		Config:                  cfg,
		HTTPHeaders:             opts.HTTPHeaders,
		StreamKeepaliveInterval: opts.StreamKeepaliveInterval,
		Log:                     diag.LoggerOrDefault(opts.Log),
		LargePayload:            lpCfg,
		Profile:                 NewProfile(),
	})

	if mux != nil {
		createPath := cfg.BasePath + "/responses"
		compactPath := cfg.BasePath + "/responses/compact"

		mux.Handle("POST "+createPath, handler)
		mux.Handle("POST "+compactPath, handler)
		if cfg.WebSocket.IsEnabled() {
			wsPath := cfg.BasePath + "/responses"
			wsHandler := NewWebSocketHandler(WebSocketHandlerConfig{
				Config: cfg,
				Runner: NewSessionRunner(SessionRunnerConfig{
					Executor:             opts.Exec,
					DefaultRouteSelector: opts.DefaultRoute,
					RoutePrefixes:        opts.RoutePrefixes,
					MaxMessageBytes:      opts.MaxRequestBodyBytes,
					ProtocolLimits:       proto.DefaultLimits(),
				}),
				// The generation context is the runtime-owned shutdown signal:
				// every session of this handler observes it and closes exactly once
				// when the generation quiesces during reload or server shutdown.
				ShutdownCtx:           opts.GenerationContext,
				MaxMessageBytes:       opts.MaxRequestBodyBytes,
				Authorizer:            opts.Authorizer,
				RequireAuthentication: !opts.AllowUnauthenticated,
				AllowUnauthenticated:  opts.AllowUnauthenticated,
			})
			mux.Handle("GET "+wsPath, wsHandler)
		}
	}

	_ = claims
	return nil
}

// newConfigContinuationStore derives a bounded in-memory continuation store and
// materialization bounds from a validated frontend config. Zero fields fall back
// to profile defaults so the default config stays consistent with the contract.
func newConfigContinuationStore(cfg Config) (*aipcont.MemoryStore, aipcont.Bounds) {
	depth := cfg.Continuation.MaxChainDepth
	if depth <= 0 {
		depth = DefaultMaxChainDepth
	}
	maxBytes := cfg.Continuation.MaxMaterializedBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMaterializedBytes
	}
	store := aipcont.NewMemoryStoreWithLimits(aipcont.StorageLimits{
		MaxRecords:     10_000,
		MaxBytes:       maxBytes,
		MaxRecordBytes: 16 << 20,
		MaxChainDepth:  depth,
	})
	bounds := aipcont.Bounds{
		MaxChainDepth:        depth,
		MaxMaterializedBytes: maxBytes,
		MaxMaterializedItems: 100_000,
	}
	return store, bounds
}
