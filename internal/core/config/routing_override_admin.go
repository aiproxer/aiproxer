package config

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// DefaultRoutingOverrideAdminPathPrefix is used when override admin is enabled
	// without an explicit path_prefix.
	DefaultRoutingOverrideAdminPathPrefix = "/admin/routing-overrides"
	// DefaultRoutingOverrideAdminMaxBodyBytes is MaxRouteSelectorBytes plus bounded JSON overhead.
	DefaultRoutingOverrideAdminMaxBodyBytes = int64(aipapi.MaxRouteSelectorBytes + 4096)
)

func validateRoutingOverrideAdmin(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	admin := &cfg.Routing.OverrideAdmin
	if !admin.Enabled {
		return nil
	}
	prefix := strings.TrimSpace(admin.PathPrefix)
	if prefix == "" {
		prefix = DefaultRoutingOverrideAdminPathPrefix
	}
	if !strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("routing.override_admin.path_prefix: must start with /")
	}
	if strings.ContainsAny(prefix, "{}") {
		return fmt.Errorf("routing.override_admin.path_prefix: must be a literal path")
	}
	if strings.Contains(prefix, "//") || strings.ContainsAny(prefix, " \t\n") {
		return fmt.Errorf("routing.override_admin.path_prefix: invalid path")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	segs := strings.Split(strings.TrimPrefix(prefix, "/"), "/")
	if len(segs) < 2 {
		return fmt.Errorf("routing.override_admin.path_prefix: must contain at least two path segments")
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("routing.override_admin.path_prefix: invalid path")
		}
	}
	for _, root := range []string{"/v1", "/v1beta", "/v1beta1"} {
		if prefix == root || strings.HasPrefix(prefix, root+"/") {
			return fmt.Errorf("routing.override_admin.path_prefix: collides with frontend protocol path %s", root)
		}
	}
	admin.PathPrefix = prefix
	if admin.MaxBodyBytes < 0 {
		return fmt.Errorf("routing.override_admin.max_body_bytes: must be >= 0")
	}
	if admin.MaxBodyBytes == 0 {
		admin.MaxBodyBytes = DefaultRoutingOverrideAdminMaxBodyBytes
	}
	if admin.MaxBodyBytes < int64(aipapi.MaxRouteSelectorBytes) {
		return fmt.Errorf("routing.override_admin.max_body_bytes: must be >= %d", aipapi.MaxRouteSelectorBytes)
	}
	return nil
}

// RoutingOverrideAdminPathPrefix returns the configured or default admin path prefix.
func RoutingOverrideAdminPathPrefix(cfg *Config) string {
	if cfg == nil {
		return DefaultRoutingOverrideAdminPathPrefix
	}
	prefix := strings.TrimSpace(cfg.Routing.OverrideAdmin.PathPrefix)
	if prefix == "" {
		return DefaultRoutingOverrideAdminPathPrefix
	}
	return prefix
}
