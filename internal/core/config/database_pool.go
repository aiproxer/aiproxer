package config

import (
	"fmt"
	"strings"
	"time"
)

// DatabasePoolSettings holds validated optional *sql.DB pool tuning from [DatabaseConfig].
// Zero values mean unset (driver defaults) when no managed PostgreSQL store is selected.
// When any store is postgres, [Validate] requires MaxOpenConns > 0 (fail-closed).
// Use [ParseDatabasePoolSettings] after YAML decode and alongside [Validate] for full checks.
type DatabasePoolSettings struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// usesManagedPostgres reports whether any composition-root path would open a
// managed PostgreSQL handle for the given config. Retired accounting.ledger.*
// YAML is ignored: that store is never opened.
func usesManagedPostgres(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return storeIsPostgres(EffectiveContinuityStore(cfg.Continuity)) ||
		storeIsPostgres(cfg.SecureSession.Store) ||
		(cfg.ControlPlane.Enabled && storeIsPostgres(cfg.ControlPlane.Store)) ||
		(cfg.Accounting.Authority.Enabled && storeIsPostgres(cfg.Accounting.Authority.Store)) ||
		(cfg.Accounting.Concurrency.Enabled && storeIsPostgres(cfg.Accounting.Concurrency.Store)) ||
		(cfg.Metering.Enabled && storeIsPostgres(cfg.Metering.Journal.Store))
}

func storeIsPostgres(store string) bool {
	return strings.EqualFold(strings.TrimSpace(store), "postgres")
}

// EffectiveDatabaseModes returns compatibility-preserving connection and schema
// modes for dual-plane PostgreSQL runtime paths after validating their combination.
func EffectiveDatabaseModes(d DatabaseConfig) (DatabaseConnectionMode, DatabaseSchemaMode, error) {
	connMode, schemaMode := d.EffectiveConnectionMode(), d.EffectiveSchemaMode()
	if connMode != DatabaseConnectionModeDirect && connMode != DatabaseConnectionModeTransactionPool {
		return "", "", fmt.Errorf("database.connection_mode: invalid value %q", d.ConnectionMode)
	}
	if schemaMode != DatabaseSchemaModeAutoMigrate && schemaMode != DatabaseSchemaModeVerifyOnly {
		return "", "", fmt.Errorf("database.schema_mode: invalid value %q", d.SchemaMode)
	}
	if connMode == DatabaseConnectionModeTransactionPool && schemaMode == DatabaseSchemaModeAutoMigrate {
		return "", "", fmt.Errorf("database: connection_mode transaction_pool requires schema_mode verify_only")
	}
	return connMode, schemaMode, nil
}

func validateDatabasePoolSettings(s DatabasePoolSettings) error {
	if s.MaxOpenConns < 0 {
		return fmt.Errorf("invalid pool max open conns: %d", s.MaxOpenConns)
	}
	if s.MaxIdleConns < 0 {
		return fmt.Errorf("invalid pool max idle conns: %d", s.MaxIdleConns)
	}
	if s.ConnMaxLifetime < 0 {
		return fmt.Errorf("invalid pool conn max lifetime: %s", s.ConnMaxLifetime)
	}
	if s.ConnMaxIdleTime < 0 {
		return fmt.Errorf("invalid pool conn max idle time: %s", s.ConnMaxIdleTime)
	}
	return nil
}

// ParseDatabasePoolSettings parses [DatabaseConfig] into [DatabasePoolSettings] and validates
// numeric and duration fields.
func ParseDatabasePoolSettings(d DatabaseConfig) (DatabasePoolSettings, error) {
	if _, _, err := EffectiveDatabaseModes(d); err != nil {
		return DatabasePoolSettings{}, err
	}
	lifetime, err := parseOptionalDBDuration("conn_max_lifetime", d.ConnMaxLifetime)
	if err != nil {
		return DatabasePoolSettings{}, err
	}
	idle, err := parseOptionalDBDuration("conn_max_idle_time", d.ConnMaxIdleTime)
	if err != nil {
		return DatabasePoolSettings{}, err
	}
	ps := DatabasePoolSettings{
		MaxOpenConns:    d.MaxOpenConns,
		MaxIdleConns:    d.MaxIdleConns,
		ConnMaxLifetime: lifetime,
		ConnMaxIdleTime: idle,
	}
	if err := validateDatabasePoolSettings(ps); err != nil {
		return DatabasePoolSettings{}, fmt.Errorf("database: %w", err)
	}
	return ps, nil
}

func parseOptionalDBDuration(name, raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("database.%s: invalid duration %q: %w", name, raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("database.%s: duration must be non-negative", name)
	}
	return d, nil
}
