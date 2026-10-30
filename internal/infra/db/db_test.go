package db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewBunDB_Nil(t *testing.T) {
	t.Parallel()
	_, err := NewBunDB(nil, DialectPostgres)
	if err == nil {
		t.Fatal("expected error for nil *sql.DB")
	}
}

func TestNewBunDB_UnknownDialect(t *testing.T) {
	t.Parallel()
	sqldb, oerr := openStubSQLite(t)
	if oerr != nil {
		t.Fatal(oerr)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	_, derr := NewBunDB(sqldb, Dialect("mysql"))
	if derr == nil {
		t.Fatal("expected error for unknown dialect")
	}
	if !strings.Contains(derr.Error(), "unknown dialect") {
		t.Fatalf("error should mention unknown dialect, got: %v", derr)
	}
}

func TestApplyPoolSettings_Nil(t *testing.T) {
	t.Parallel()
	err := ApplyPoolSettings(nil, PoolSettings{MaxOpenConns: 2})
	if !errors.Is(err, ErrNilDB) {
		t.Fatalf("expected ErrNilDB, got %v", err)
	}
}

func TestApplyPoolSettings_Invalid(t *testing.T) {
	t.Parallel()
	sqldb, err := openStubSQLite(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	cases := []struct {
		name string
		s    PoolSettings
	}{
		{"max_open", PoolSettings{MaxOpenConns: -1}},
		{"max_idle", PoolSettings{MaxIdleConns: -1}},
		{"lifetime", PoolSettings{ConnMaxLifetime: -1 * time.Second}},
		{"idle_time", PoolSettings{ConnMaxIdleTime: -1 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if aerr := ApplyPoolSettings(sqldb, tc.s); aerr == nil {
				t.Fatal("expected error for invalid pool settings")
			}
		})
	}
}

func TestApplyPoolSettings_RealDB(t *testing.T) {
	t.Parallel()
	sqldb, err := openStubSQLite(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	if err := ApplyPoolSettings(sqldb, PoolSettings{
		MaxOpenConns:    2,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 30 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRedactDSN_PasswordNotVisible(t *testing.T) {
	t.Parallel()
	const secret = "not-the-real-password-xyz"
	cases := []struct {
		in, label string
	}{
		{"postgres://u:" + secret + "@127.0.0.1:5432/db?sslmode=disable", "uri"},
		{"password=" + secret + " host=localhost", "keyval"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			out := RedactDSN(tc.in)
			if strings.Contains(out, secret) {
				t.Fatalf("redacted DSN must not contain secret, got: %q", out)
			}
		})
	}
}

func TestOpenPostgres_NilContext(t *testing.T) {
	t.Parallel()
	_, err := OpenPostgres(nil, "postgres://u@h/db") //nolint:staticcheck // contract: explicit nil ctx must yield ErrNilContext
	if !errors.Is(err, ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
}

func TestOpenPostgres_EmptyDSN(t *testing.T) {
	t.Parallel()
	_, err := OpenPostgres(context.Background(), "   ")
	if !errors.Is(err, ErrEmptyDSN) {
		t.Fatalf("expected ErrEmptyDSN, got %v", err)
	}
}

func TestOpenPostgres_ErrorRedactsDSNSecret(t *testing.T) {
	t.Parallel()
	const secret = "unit-test-secret-abc-999"
	// Unreachable address; connection or ping should fail. DSN must not include secret in error text.
	dsn := "postgres://dumbuser:" + secret + "@127.0.0.1:7/nope?sslmode=disable&connect_timeout=1"
	_, err := OpenPostgres(context.Background(), dsn)
	if err == nil {
		t.Fatal("expected error (connection refused or timeout)")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not include DSN password, got: %v", err)
	}
}

func TestRedactOpenError_visibleOnlyNoRawDriverSuffix(t *testing.T) {
	t.Parallel()
	const secret = "embedded-open-err-secret-xyz"
	dsn := "postgres://u:" + secret + "@h/db"
	inner := errors.New("driver failed: " + dsn)
	err := redactOpenError(dsn, "open postgres: ping", inner)
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Error() must not repeat raw driver text: %v", err)
	}
	if !errors.Is(err, inner) {
		t.Fatalf("expected errors.Is chain to inner, got %v", err)
	}
}

func TestValidatePoolSettings_ZeroIsOK(t *testing.T) {
	t.Parallel()
	if err := ValidatePoolSettings(PoolSettings{}); err != nil {
		t.Fatal(err)
	}
}
