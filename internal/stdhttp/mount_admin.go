package stdhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	adminkeepwarm "github.com/aiproxer/aiproxer/internal/stdhttp/admin/keepwarm"
	adminaccounting "github.com/aiproxer/aiproxer/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// mountAccountingAdminInput carries inputs for [mountAccountingAdmin].
type mountAccountingAdminInput struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountAccountingAdmin mounts the token-accounting admin endpoint when accounting.admin.enabled
// is true and a path is configured. The handler is wrapped with the diagnostics shared-secret
// protection. Never returns an error: when disabled or misconfigured it simply mounts nothing.
type adminMountsInput struct {
	LogCtx context.Context
	Mux    *http.ServeMux
	Cfg    *config.Config
	Log    *slog.Logger
	Mounts []AdminMount
}

// adminMountBuiltinPaths are the fixed operator paths stdhttp itself may mount;
// a registered admin mount may never collide with one of them.
var adminMountBuiltinPaths = []string{"/admin/keepwarm", "/admin/session-features"}

// mountAdminMounts serves every caller-registered operator handler on the
// standard mux behind the existing admin authentication.
//
// Registration errors fail the build with a readable error instead of a
// ServeMux panic: every path must be a literal absolute path below /admin/,
// and duplicate, nested, built-in, or configured-path-colliding registrations
// are rejected before anything is mounted. [callMount] stays the backstop for
// any residual conflict with the rest of the request plane.
//
// An empty diagnostics shared secret mounts nothing (with an explicit warn),
// exactly like the other protected admin surfaces: the diagnostics wrapper is
// a pass-through then, so mounting would expose the handler unauthenticated.
func mountAdminMounts(in adminMountsInput) error {
	if in.Mux == nil || in.Cfg == nil || len(in.Mounts) == 0 {
		return nil
	}
	mounts, err := validateAdminMounts(in.Mounts, in.Cfg)
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Cfg.Diagnostics.SharedSecret) == "" {
		if in.Log != nil {
			for _, mount := range mounts {
				in.Log.WarnContext(in.LogCtx, "admin mount skipped: diagnostics shared_secret is empty",
					slog.String("path", mount.Path))
			}
		}
		return nil
	}
	for _, mount := range mounts {
		protected := wrapDiagnostics(in.Cfg, http.StripPrefix(mount.Path, mount.Handler))
		path := mount.Path
		if err := callMount(func() error {
			in.Mux.Handle(path, protected)
			in.Mux.Handle(path+"/", protected)
			return nil
		}); err != nil {
			return err
		}
		if in.Log != nil {
			in.Log.InfoContext(in.LogCtx, "protected admin mount served", "path", path)
		}
	}
	return nil
}

// validateAdminMounts normalizes and validates every registered admin mount
// against the literal path policy, the built-in operator paths, and the
// actual configured data-plane paths of this configuration.
func validateAdminMounts(mounts []AdminMount, cfg *config.Config) ([]AdminMount, error) {
	normalized := make([]AdminMount, 0, len(mounts))
	// Registered mounts collide with each other and with the reserved
	// operator surface; segment siblings such as /admin/foo and /admin/foobar
	// stay distinct.
	reserved := adminMountReservedPaths(cfg)
	for _, mount := range mounts {
		if mount.Handler == nil {
			return nil, fmt.Errorf("stdhttp: admin mount %q has no handler", mount.Path)
		}
		path, err := normalizeAdminMountPath(mount.Path)
		if err != nil {
			return nil, fmt.Errorf("stdhttp: admin mount %q: %w", mount.Path, err)
		}
		for _, other := range normalized {
			if adminMountPathsOverlap(path, other.Path) {
				return nil, fmt.Errorf("stdhttp: admin mount %q overlaps the registered admin mount %q", path, other.Path)
			}
		}
		for _, existing := range reserved {
			if adminMountPathsOverlap(path, existing) {
				return nil, fmt.Errorf("stdhttp: admin mount %q overlaps the protected operator path %q", path, existing)
			}
		}
		normalized = append(normalized, AdminMount{Path: path, Handler: mount.Handler})
	}
	return normalized, nil
}

// adminMountReservedPaths returns the operator paths a registered admin mount
// may not take: the fixed built-in operator mounts plus every configured
// data-plane path stdhttp mounts itself.
//
// The configured accounting.billing.reports_path is deliberately excluded: the
// Enterprise composition registers its billing surface as an admin mount at
// that path, so the configured value must not reject its own mount. A second
// registration at that path still fails as a duplicate registered mount.
func adminMountReservedPaths(cfg *config.Config) []string {
	reserved := append([]string(nil), adminMountBuiltinPaths...)
	reportsPath := strings.TrimSuffix(strings.TrimSpace(cfg.Accounting.Billing.ReportsPath), "/")
	for _, configured := range config.ConfiguredDataPlanePaths(cfg) {
		if reportsPath != "" && configured == reportsPath {
			continue
		}
		reserved = append(reserved, configured)
	}
	return reserved
}

// normalizeAdminMountPath applies the shared literal operator path policy and
// requires the result to stay below /admin/ (bare /admin itself is not a
// mountable operator surface).
func normalizeAdminMountPath(path string) (string, error) {
	if err := httpcontract.ValidateOperatorRoutePath("admin mount path", path); err != nil {
		return "", err
	}
	normalized, err := httpcontract.NormalizePath(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(normalized, "/admin/") {
		return "", fmt.Errorf("must be a literal path below /admin/")
	}
	return normalized, nil
}

// adminMountPathsOverlap reports whether two normalized operator paths
// duplicate or nest; the router could not serve both unambiguously.
func adminMountPathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func mountKeepwarmAdmin(in mountAccountingAdminInput) {
	if in.Mux == nil || in.Cfg == nil || !in.Operations.KeepwarmAdminEnabled || in.Operations.KeepwarmAdmin.Service == nil {
		return
	}
	// The keep-warm admin surface mutates per-session policy. Protect the same
	// way as the other admin surfaces: never mount when the diagnostics shared
	// secret is empty, because the protection wrapper is a pass-through then.
	if strings.TrimSpace(in.Cfg.Diagnostics.SharedSecret) == "" {
		if in.Log != nil {
			in.Log.WarnContext(in.LogCtx, "keep-warm admin disabled: diagnostics shared_secret is empty")
		}
		return
	}
	path := "/admin/keepwarm"
	h := adminkeepwarm.NewHandler(in.Operations.KeepwarmAdmin)
	in.Mux.Handle(path, wrapDiagnostics(in.Cfg, http.StripPrefix(path, h)))
	in.Mux.Handle(path+"/", wrapDiagnostics(in.Cfg, http.StripPrefix(path, h)))
	if in.Log != nil {
		in.Log.InfoContext(in.LogCtx, "keep-warm admin mounted", "path", path)
	}
}

func mountAccountingAdmin(in mountAccountingAdminInput) {
	mux, cfg, log, ops := in.Mux, in.Cfg, in.Log, in.Operations
	logCtx := in.LogCtx
	if !cfg.Accounting.Admin.Enabled {
		return
	}
	path := strings.TrimSpace(cfg.Accounting.Admin.Path)
	if path == "" {
		return
	}
	h := adminaccounting.NewHandler(adminaccounting.Options{
		Enabled:      true,
		MaxBodyBytes: cfg.Accounting.Admin.MaxBodyBytes,
		Service:      ops.TokenAccountingAdmin,
	})
	mux.Handle(path, wrapDiagnostics(cfg, h))
	log.InfoContext(logCtx, "token accounting admin mounted", "path", path)
}

// controlPlaneQueryMount carries inputs for [mountControlPlaneQuery].
type controlPlaneQueryMount struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountControlPlaneQuery mounts the protected control-plane status and query
// routes only when control-plane query exposure is explicitly enabled and the
// diagnostics shared-secret posture allows it (task 5.3; requirements 2.1–2.9,
// 4.6, 4.7, 5.5, 7.1, 7.4, 8.6, 9.1, 9.4, 10.4, 10.5).
//
// The handler is wrapped with the existing diagnostics shared-secret protection
// so it never becomes a client-facing LLM protocol response path and never
// leaks raw infrastructure details. When the capability is disabled or query
// exposure is off, no route is mounted and the path returns 404.
func mountControlPlaneQuery(in controlPlaneQueryMount) {
	mux, cfg, log, ops := in.Mux, in.Cfg, in.Log, in.Operations
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.ControlPlaneQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(
				logCtx, "control-plane query config enabled but diagnostics shared_secret is empty; mounting disabled (query surface would be unauthenticated)",
				slog.String("component", "control_plane"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if ops.ControlPlaneQueries == nil {
		if log != nil {
			log.WarnContext(
				logCtx, "control-plane query config enabled but no query service wired; mounting disabled",
				slog.String("component", "control_plane"),
				slog.String("notice", "query_service_unavailable"),
			)
		}
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(cfg.ControlPlane.Query.PathPrefix), "/")
	if base == "" {
		return
	}
	handler := cpadmin.NewHandler(cpadmin.Options{
		Queries:         ops.ControlPlaneQueries,
		ReadinessReport: ops.ReadinessReport,
	})
	protected := wrapDiagnostics(cfg, http.StripPrefix(base, handler))
	mux.Handle(base, protected)
	mux.Handle(base+"/", protected)
	if log != nil {
		log.InfoContext(logCtx, "control-plane query mounted", "path", base)
	}
}

// accountingAuthorityQueryMount carries inputs for [mountAccountingAuthorityQuery].
type accountingAuthorityQueryMount struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Security   HTTPSecurityInput
	Operations HTTPOperationsInput
}

// mountAccountingAuthorityQuery mounts the protected authority status and
// bounded query routes only when the authority capability and diagnostics
// shared-secret posture are explicitly configured.
func mountAccountingAuthorityQuery(in accountingAuthorityQueryMount) {
	mux, cfg, log, sec, ops := in.Mux, in.Cfg, in.Log, in.Security, in.Operations
	logCtx := in.LogCtx
	if mux == nil || cfg == nil {
		return
	}
	if !config.AuthorityQueryEffectivelyExposed(cfg) {
		return
	}
	if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
		if log != nil {
			log.WarnContext(
				logCtx, "accounting authority query config enabled but diagnostics shared_secret is empty; mounting disabled",
				slog.String("component", "accounting_authority"),
				slog.String("notice", "shared_secret_required"),
			)
		}
		return
	}
	if sec.UsageAuthority == nil {
		if log != nil {
			log.WarnContext(
				logCtx, "accounting authority query config enabled but no authority service wired; mounting disabled",
				slog.String("component", "accounting_authority"),
				slog.String("notice", "authority_service_unavailable"),
			)
		}
		return
	}
	base := strings.TrimSuffix(strings.TrimSpace(cfg.Accounting.Authority.Query.PathPrefix), "/")
	if base == "" {
		return
	}
	accHandler := cpadmin.NewAccountingAuthorityHandler(cpadmin.AuthorityOptions{
		Queries:         sec.UsageAuthority,
		DefaultPageSize: cfg.Accounting.Authority.Query.DefaultPageSize,
		MaxPageSize:     cfg.Accounting.Authority.Query.MaxPageSize,
	})
	handler := http.Handler(accHandler)
	if ops.ConcurrencyProvider != nil {
		leaseHandler := cpadmin.NewConcurrencyAuthorityHandler(cpadmin.ConcurrencyOptions{
			Provider:        ops.ConcurrencyProvider,
			Service:         sec.ConcurrencyAuthority,
			DefaultPageSize: cfg.Accounting.Authority.Query.DefaultPageSize,
			MaxPageSize:     cfg.Accounting.Authority.Query.MaxPageSize,
		})
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimPrefix(r.URL.Path, "/")
			if strings.HasPrefix(path, "leases") {
				leaseHandler.ServeHTTP(w, r)
				return
			}
			accHandler.ServeHTTP(w, r)
		})
	}
	protected := wrapDiagnostics(cfg, http.StripPrefix(base, handler))
	mux.Handle(base, protected)
	mux.Handle(base+"/", protected)
	if log != nil {
		log.InfoContext(logCtx, "accounting authority query mounted", "path", base)
	}
}

type routeOverrideAdminMount struct {
	LogCtx     context.Context
	Mux        *http.ServeMux
	Cfg        *config.Config
	Log        *slog.Logger
	Operations HTTPOperationsInput
}

// mountRouteOverrideAdmin mounts the generation-bound routing-override admin
// handler on the standard mux so it shares the request-plane access-auth stack.
func mountRouteOverrideAdmin(in routeOverrideAdminMount) {
	mux, cfg, log, ops := in.Mux, in.Cfg, in.Log, in.Operations
	if mux == nil || cfg == nil || ops.RouteOverrideAdmin == nil {
		return
	}
	if !cfg.Routing.OverrideAdmin.Enabled {
		return
	}
	prefix := strings.TrimSuffix(config.RoutingOverrideAdminPathPrefix(cfg), "/")
	if prefix == "" {
		return
	}
	protected := wrapDiagnostics(cfg, http.StripPrefix(prefix, ops.RouteOverrideAdmin))
	mux.Handle(prefix, protected)
	mux.Handle(prefix+"/", protected)
	if log != nil {
		log.InfoContext(in.LogCtx, "routing override admin mounted", "path", prefix)
	}
}
