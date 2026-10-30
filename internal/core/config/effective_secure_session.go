package config

import (
	"strings"
	"time"
)

const defaultSecureSessionSQLQueryCacheMaxEntries = 4096

// EffectiveSecureSessionSQLQueryCache returns parsed TTL and capacity for durable secure-session SQL metadata caches.
// enabled is false when sql_query_cache_ttl is empty. Max entries zero coerces to defaultSecureSessionSQLQueryCacheMaxEntries.
func EffectiveSecureSessionSQLQueryCache(ss SecureSessionConfig) (ttl time.Duration, maxEntries uint64, enabled bool) {
	raw := strings.TrimSpace(ss.SQLQueryCacheTTL)
	if raw == "" {
		return 0, 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, 0, false
	}
	me := ss.SQLQueryCacheMaxEntries
	if me <= 0 {
		me = defaultSecureSessionSQLQueryCacheMaxEntries
	}
	return d, uint64(me), true
}

func (c *Config) SecureSessionEffectivelyEnabled() bool {
	if c == nil {
		return false
	}
	ss := &c.SecureSession
	if ss.Enabled == nil {
		return true
	}
	return *ss.Enabled
}

// EffectiveServerAuthMode returns the configured HTTP auth posture. Empty defaults to no_auth
// for developer-local defaults; startup validation restricts no_auth to explicit loopback binds.
func (c *Config) EffectiveServerAuthMode() AuthMode {
	if c == nil {
		return AuthModeNoAuth
	}
	if c.Server.AuthMode == "" {
		return AuthModeNoAuth
	}
	return c.Server.AuthMode
}

// SingleUserLocalMode reports whether startup policy permits local no-auth/synthetic-principal behavior.
func (c *Config) SingleUserLocalMode() bool {
	if c == nil {
		return false
	}
	return c.EffectiveServerAuthMode() == AuthModeNoAuth && IsExplicitLoopbackListenAddress(c.Server.Address)
}
