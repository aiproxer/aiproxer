package journalstore_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/uptrace/bun/driver/pgdriver"
)

// sqlStateCarrier exposes the PostgreSQL SQLSTATE field ('C') of a driver error
// so the optional-absence decision is unit testable without a live server.
type sqlStateCarrier interface {
	Field(k byte) string
}

// Compile-time proof that the production PostgreSQL driver error carries the
// SQLSTATE field the classifier reads.
var _ sqlStateCarrier = pgdriver.Error{}

// isOptionalIsolatedDatabaseAbsence reports whether err is a PostgreSQL error
// meaning the current role legitimately cannot use the optional disposable
// database capability: insufficient_privilege (42501) or
// feature_not_supported (0A000). Any other error is unexpected and must not be
// treated as a reason to skip a required proof.
func isOptionalIsolatedDatabaseAbsence(err error) bool {
	if err == nil {
		return false
	}
	var carrier sqlStateCarrier
	if errors.As(err, &carrier) {
		switch carrier.Field('C') {
		case "42501", "0A000":
			return true
		}
	}
	return false
}

type fakeSQLStateError struct{ code string }

func (e fakeSQLStateError) Error() string { return "fake sql error " + e.code }

func (e fakeSQLStateError) Field(k byte) string {
	if k == 'C' {
		return e.code
	}
	return ""
}

func TestIsOptionalIsolatedDatabaseAbsence(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unrelated", err: errors.New("boom"), want: false},
		{name: "insufficient privilege", err: fakeSQLStateError{code: "42501"}, want: true},
		{name: "feature not supported", err: fakeSQLStateError{code: "0A000"}, want: true},
		{name: "undefined table", err: fakeSQLStateError{code: "42P01"}, want: false},
		{name: "duplicate database", err: fakeSQLStateError{code: "42P04"}, want: false},
		{name: "wrapped insufficient privilege", err: fmt.Errorf("create isolated journal database: %w", fakeSQLStateError{code: "42501"}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOptionalIsolatedDatabaseAbsence(tc.err); got != tc.want {
				t.Fatalf("isOptionalIsolatedDatabaseAbsence(%v) = %v want %v", tc.err, got, tc.want)
			}
		})
	}
}
