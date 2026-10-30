package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidate_secureSession_sqlQueryCacheTTL_emptyOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:                 config.BoolPtr(true),
			Store:                   "memory",
			TokenFingerprintKey:     strings.Repeat("k", 32),
			SQLQueryCacheTTL:        "",
			SQLQueryCacheMaxEntries: 100,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_sqlQueryCacheMaxEntries_negative(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:                 config.BoolPtr(true),
			Store:                   "memory",
			TokenFingerprintKey:     strings.Repeat("k", 32),
			SQLQueryCacheMaxEntries: -1,
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "sql_query_cache_max_entries") {
		t.Fatalf("want max_entries error, got %v", err)
	}
}

func TestValidate_secureSession_sqlQueryCacheTTL_emptyAndMaxOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:                 config.BoolPtr(true),
			Store:                   "memory",
			TokenFingerprintKey:     strings.Repeat("k", 32),
			SQLQueryCacheTTL:        "",
			SQLQueryCacheMaxEntries: 0,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_sqlQueryCacheTTL_positiveOK(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: strings.Repeat("k", 32),
			SQLQueryCacheTTL:    "30s",
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_secureSession_sqlQueryCacheTTL_invalidParse(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: strings.Repeat("k", 32),
			SQLQueryCacheTTL:    "not-a-duration",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "sql_query_cache_ttl") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_secureSession_sqlQueryCacheTTL_nonPositive(t *testing.T) {
	t.Parallel()
	for _, ttl := range []string{"0s", "-1ms"} {
		cfg := &config.Config{
			Plugins: secureSessionBaselinePlugins(),
			SecureSession: config.SecureSessionConfig{
				Enabled:             config.BoolPtr(true),
				Store:               "memory",
				TokenFingerprintKey: strings.Repeat("k", 32),
				SQLQueryCacheTTL:    ttl,
			},
		}
		err := config.Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "sql_query_cache_ttl") {
			t.Fatalf("ttl %q: want error, got %v", ttl, err)
		}
	}
}

func TestEffectiveSecureSessionSQLQueryCache_defaultMaxEntries(t *testing.T) {
	t.Parallel()
	ss := config.SecureSessionConfig{
		SQLQueryCacheTTL:        "1m",
		SQLQueryCacheMaxEntries: 0,
	}
	ttl, maxE, ok := config.EffectiveSecureSessionSQLQueryCache(ss)
	if !ok || ttl != time.Minute || maxE != 4096 {
		t.Fatalf("got ttl=%v maxE=%v ok=%v", ttl, maxE, ok)
	}
}
