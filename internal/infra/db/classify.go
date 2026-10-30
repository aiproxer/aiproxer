package db

import (
	"errors"
	"strings"

	"github.com/uptrace/bun/driver/pgdriver"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation reports whether err is a unique-constraint violation on the
// supported dialects. SQLite reports the extended result codes
// SQLITE_CONSTRAINT_UNIQUE and SQLITE_CONSTRAINT_PRIMARYKEY through the
// driver's typed error; PostgreSQL reports SQLSTATE 23505 through
// pgdriver.Error field "C". The typed driver code is authoritative: constraint
// message text is never inspected, so an unrelated constraint failure (for
// example a CHECK or NOT NULL violation) is never misclassified as a replayable
// identity race.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		switch sqliteErr.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return true
		}
	}
	if pgErr, ok := errors.AsType[pgdriver.Error](err); ok {
		return pgErr.Field('C') == "23505"
	}
	return false
}

// IsSQLiteBusy reports SQLite lock contention (SQLITE_BUSY or SQLITE_LOCKED,
// including their extended variants). The driver's typed result code is
// authoritative; the message fallback matches only SQLite's own lock phrases
// ("database is locked", "database table is locked") so unrelated errors that
// merely contain the word "busy" are never retried. Callers that must not treat
// lock contention as retryable on other engines keep a dialect gate at the call
// site.
func IsSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	if sqliteErr, ok := errors.AsType[*sqlite.Error](err); ok {
		code := sqliteErr.Code() & 0xff
		return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked")
}
