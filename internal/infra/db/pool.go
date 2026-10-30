package db

import (
	"database/sql"
	"fmt"
	"time"
)

// PoolSettings are optional *sql.DB pool fields. Zero values are ignored so driver
// defaults are preserved. Invalid (negative) values are rejected.
type PoolSettings struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// ValidatePoolSettings returns an error if any field is out of the accepted range
// (negative integers or negative durations). Zero values are valid and mean
// "unset" for the corresponding setter.
func ValidatePoolSettings(s PoolSettings) error {
	if s.MaxOpenConns < 0 {
		return fmt.Errorf("db: invalid pool max open conns: %d", s.MaxOpenConns)
	}
	if s.MaxIdleConns < 0 {
		return fmt.Errorf("db: invalid pool max idle conns: %d", s.MaxIdleConns)
	}
	if s.ConnMaxLifetime < 0 {
		return fmt.Errorf("db: invalid pool conn max lifetime: %s", s.ConnMaxLifetime)
	}
	if s.ConnMaxIdleTime < 0 {
		return fmt.Errorf("db: invalid pool conn max idle time: %s", s.ConnMaxIdleTime)
	}
	return nil
}

// ApplyPoolSettings sets connection pool options on sqldb. Only non-zero fields
// are applied, preserving driver defaults elsewhere. Fails for nil *sql.DB or
// invalid settings.
func ApplyPoolSettings(sqldb *sql.DB, s PoolSettings) error {
	if sqldb == nil {
		return ErrNilDB
	}
	if err := ValidatePoolSettings(s); err != nil {
		return err
	}
	if s.MaxOpenConns > 0 {
		sqldb.SetMaxOpenConns(s.MaxOpenConns)
	}
	if s.MaxIdleConns > 0 {
		sqldb.SetMaxIdleConns(s.MaxIdleConns)
	}
	if s.ConnMaxLifetime > 0 {
		sqldb.SetConnMaxLifetime(s.ConnMaxLifetime)
	}
	if s.ConnMaxIdleTime > 0 {
		sqldb.SetConnMaxIdleTime(s.ConnMaxIdleTime)
	}
	return nil
}
