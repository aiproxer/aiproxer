package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestParseDatabasePoolSettings_zeroOK(t *testing.T) {
	t.Parallel()
	ps, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ps.MaxOpenConns != 0 || ps.MaxIdleConns != 0 || ps.ConnMaxLifetime != 0 || ps.ConnMaxIdleTime != 0 {
		t.Fatalf("unexpected non-zero pool: %+v", ps)
	}
}

func TestParseDatabasePoolSettings_parsesDurations(t *testing.T) {
	t.Parallel()
	ps, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{
		MaxOpenConns:    3,
		ConnMaxLifetime: "30m",
		ConnMaxIdleTime: "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ps.MaxOpenConns != 3 {
		t.Fatalf("max open: %d", ps.MaxOpenConns)
	}
	if ps.ConnMaxLifetime != 30*time.Minute || ps.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("durations: %+v", ps)
	}
}

func TestParseDatabasePoolSettings_invalidDuration(t *testing.T) {
	t.Parallel()
	_, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{
		ConnMaxLifetime: "not-a-duration",
	})
	if err == nil || !strings.Contains(err.Error(), "conn_max_lifetime") {
		t.Fatalf("want conn_max_lifetime error, got %v", err)
	}
}

func TestParseDatabasePoolSettings_negativeMaxOpen(t *testing.T) {
	t.Parallel()
	_, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: -1})
	if err == nil || !strings.Contains(err.Error(), "database:") {
		t.Fatalf("want database pool error, got %v", err)
	}
}

func TestParseDatabasePoolSettings_rejectsTransactionPoolAutoMigrate(t *testing.T) {
	t.Parallel()
	_, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeAutoMigrate,
	})
	if err == nil || !strings.Contains(err.Error(), "requires schema_mode verify_only") {
		t.Fatalf("want transaction_pool+auto_migrate rejection, got %v", err)
	}
}

func TestValidate_requiresMaxOpenConnsWhenPostgresStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{Store: "postgres", PostgresDSN: "postgres://u:p@127.0.0.1:1/db?sslmode=disable"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "database.max_open_conns: required when any store is postgres") {
		t.Fatalf("want max_open_conns required error, got %v", err)
	}
	cfg.Database.MaxOpenConns = 8
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate with max_open_conns: %v", err)
	}
}

func TestValidate_zeroMaxOpenOKWithoutPostgres(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: ":memory:",
		},
		Database: config.DatabaseConfig{MaxOpenConns: 0},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_zeroMaxOpenOKWithRetiredLedgerPostgres(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:0"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
		Accounting: config.AccountingConfig{
			Ledger: config.AccountingLedgerConfig{Store: "postgres"},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("retired accounting.ledger postgres leftover must not require database.max_open_conns, got %v", err)
	}
}

func TestDatabaseConfigModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		cfg            config.DatabaseConfig
		wantConnection config.DatabaseConnectionMode
		wantSchema     config.DatabaseSchemaMode
		wantErr        string
	}{
		{name: "compatibility defaults", cfg: config.DatabaseConfig{}, wantConnection: config.DatabaseConnectionModeDirect, wantSchema: config.DatabaseSchemaModeAutoMigrate},
		{name: "direct verify", cfg: config.DatabaseConfig{ConnectionMode: config.DatabaseConnectionModeDirect, SchemaMode: config.DatabaseSchemaModeVerifyOnly}, wantConnection: config.DatabaseConnectionModeDirect, wantSchema: config.DatabaseSchemaModeVerifyOnly},
		{name: "pooled verify", cfg: config.DatabaseConfig{ConnectionMode: config.DatabaseConnectionModeTransactionPool, SchemaMode: config.DatabaseSchemaModeVerifyOnly}, wantConnection: config.DatabaseConnectionModeTransactionPool, wantSchema: config.DatabaseSchemaModeVerifyOnly},
		{name: "unknown connection", cfg: config.DatabaseConfig{ConnectionMode: "sticky"}, wantErr: "database.connection_mode"},
		{name: "unknown schema", cfg: config.DatabaseConfig{SchemaMode: "maybe"}, wantErr: "database.schema_mode"},
		{name: "pooled auto migrate", cfg: config.DatabaseConfig{ConnectionMode: config.DatabaseConnectionModeTransactionPool, SchemaMode: config.DatabaseSchemaModeAutoMigrate}, wantErr: "requires schema_mode verify_only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connection, schema, err := config.EffectiveDatabaseModes(tt.cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error=%v want containing %q", err, tt.wantErr)
			}
			if err == nil && (connection != tt.wantConnection || schema != tt.wantSchema) {
				t.Fatalf("modes=(%q,%q) want (%q,%q)", connection, schema, tt.wantConnection, tt.wantSchema)
			}
		})
	}
}
