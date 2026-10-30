package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

func TestUnmarshal_databaseConfigAndPostgresDSNFields(t *testing.T) {
	t.Parallel()
	const yml = `
database:
  max_open_conns: 4
  max_idle_conns: 2
  conn_max_lifetime: 30m
  conn_max_idle_time: 5m
continuity:
  in_memory: false
  store: postgres
  postgres_dsn: "postgres://user:pass@localhost/continuity?sslmode=disable"
secure_session:
  store: postgres
  postgres_dsn: "postgres://user:pass@localhost/sessions?sslmode=disable"
  token_fingerprint_key: "01234567890123456789012345678901"
  audit_durability: best_effort
`
	var c config.Config
	if err := yaml.Unmarshal([]byte(yml), &c); err != nil {
		t.Fatal(err)
	}
	if c.Database.MaxOpenConns != 4 {
		t.Fatalf("max_open: got %d", c.Database.MaxOpenConns)
	}
	if c.Database.MaxIdleConns != 2 {
		t.Fatalf("max_idle: got %d", c.Database.MaxIdleConns)
	}
	if c.Database.ConnMaxLifetime != "30m" {
		t.Fatalf("conn_max_lifetime: %q", c.Database.ConnMaxLifetime)
	}
	if c.Database.ConnMaxIdleTime != "5m" {
		t.Fatalf("conn_max_idle_time: %q", c.Database.ConnMaxIdleTime)
	}
	if c.Continuity.PostgresDSN == "" {
		t.Fatal("continuity.postgres_dsn")
	}
	if c.SecureSession.PostgresDSN == "" {
		t.Fatal("secure_session.postgres_dsn")
	}
}

func TestValidate_continuityStoreRejectsUnknown(t *testing.T) {
	t.Parallel()
	for _, inMemory := range []bool{false, true} {
		t.Run(fmt.Sprintf("in_memory=%t", inMemory), func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Continuity: config.ContinuityConfig{InMemory: inMemory, Store: " ReDiS "},
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
			}
			err := config.Validate(cfg)
			if err == nil || err.Error() != `continuity.store: want memory, sqlite, or postgres, got " ReDiS "` {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestValidate_continuityPostgresMissingDSN(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: false, Store: "postgres", PostgresDSN: "   "},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "continuity.postgres_dsn") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_databasePoolRejectsInvalid(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Database:   config.DatabaseConfig{MaxOpenConns: -1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
	}
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_databaseConnMaxLifetimeInvalidString(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Database:   config.DatabaseConfig{ConnMaxLifetime: "not-a-duration"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
	}
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "conn_max_lifetime") {
		t.Fatalf("got %v", err)
	}
}

func TestValidate_durableAuditAllowedWithPostgresStore(t *testing.T) {
	t.Parallel()
	k := strings.Repeat("k", 32)
	cfg := &config.Config{
		Plugins: secureSessionBaselinePlugins(),
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "postgres",
			PostgresDSN:         "postgres://unreachable/test-skip",
			TokenFingerprintKey: k,
			AuditDurability:     "durable",
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Database:   config.DatabaseConfig{MaxOpenConns: 8},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFile_continuitySqliteWithDatabaseBlock(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	yml := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: false
  store: sqlite
  sqlite_path: ":memory:"
database:
  max_open_conns: 0
  conn_max_lifetime: ""
plugins:
  backends:
    - id: stub
      enabled: true
`
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestLoadFile_postgresContinuityAndSecureSession(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	k := strings.Repeat("k", 32)
	yml := fmt.Sprintf(`
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: false
  store: postgres
  postgres_dsn: "postgres://user:pass@127.0.0.1:1/aip?sslmode=disable"
database:
  max_open_conns: 8
  max_idle_conns: 2
  conn_max_lifetime: 30m
  conn_max_idle_time: 2m
plugins:
  backends:
    - id: stub
      enabled: true
secure_session:
  store: postgres
  postgres_dsn: "postgres://user:pass@127.0.0.1:1/aipss?sslmode=disable"
  token_fingerprint_key: %q
  audit_durability: best_effort
`, k)
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Continuity.Store != "postgres" {
		t.Fatalf("continuity.store: %q", cfg.Continuity.Store)
	}
}

func TestValidate_crossField_postgresDSNAndSQLitePathMutualExclusivity(t *testing.T) {
	t.Parallel()
	plugins := config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}}
	k32 := strings.Repeat("k", 32)
	cases := []struct {
		name       string
		cfg        *config.Config
		wantSubstr string
	}{
		{
			name: "secure_session_postgres_dsn_with_memory_store",
			cfg: &config.Config{
				Plugins: secureSessionBaselinePlugins(),
				SecureSession: config.SecureSessionConfig{
					Enabled:             config.BoolPtr(true),
					Store:               "memory",
					TokenFingerprintKey: k32,
					PostgresDSN:         "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
				},
				Continuity: config.ContinuityConfig{InMemory: true},
			},
			wantSubstr: "secure_session.postgres_dsn: may only be set when store is \"postgres\"",
		},
		{
			name: "continuity_postgres_dsn_when_store_memory",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    false,
					Store:       "memory",
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
				},
			},
			wantSubstr: "continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"",
		},
		{
			name: "continuity_in_memory_with_durable_store",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    true,
					Store:       "postgres",
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
				},
			},
			wantSubstr: "continuity.in_memory: true asks for the process-local backing",
		},
		{
			name: "continuity_postgres_dsn_when_store_sqlite",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    false,
					Store:       "sqlite",
					SQLitePath:  filepath.Join(t.TempDir(), "c.db"),
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
				},
			},
			wantSubstr: "continuity.postgres_dsn: may only be set when continuity.store is \"postgres\"",
		},
		{
			name: "continuity_sqlite_path_when_store_postgres",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    false,
					Store:       "postgres",
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
					SQLitePath:  filepath.Join(t.TempDir(), "legacy.db"),
				},
			},
			wantSubstr: "continuity.sqlite_path: may only be set when store is \"sqlite\"",
		},
		{
			name: "continuity_ttl_when_store_postgres",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    false,
					Store:       "postgres",
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
					TTL:         "1h",
				},
			},
			wantSubstr: "continuity: ttl is not supported for postgres store",
		},
		{
			name: "continuity_max_legs_when_store_postgres",
			cfg: &config.Config{
				Plugins: plugins,
				Continuity: config.ContinuityConfig{
					InMemory:    false,
					Store:       "postgres",
					PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
					MaxLegs:     3,
				},
			},
			wantSubstr: "continuity: max_legs is not supported for postgres store",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := config.Validate(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("want substring %q, got %v", tc.wantSubstr, err)
			}
		})
	}
}
