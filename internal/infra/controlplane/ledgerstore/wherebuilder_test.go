package ledgerstore

import (
	"strings"
	"testing"

	"github.com/uptrace/bun/dialect"
)

func TestWhereBuilder_PostgresPlaceholderNumberingFromZero(t *testing.T) {
	t.Parallel()
	w := newWhereBuilder(dialect.PG)
	// No eq/gte/lt/lte clauses; a trailing LIMIT placeholder must still bind at $1.
	ph := w.placeholder()
	if ph != "$1" {
		t.Fatalf("first PG placeholder = %q, want $1", ph)
	}
	if w.n != 1 {
		t.Fatalf("counter = %d, want 1", w.n)
	}
	// A second allocation (LIMIT-style) advances to $2.
	if got := w.placeholder(); got != "$2" {
		t.Fatalf("second PG placeholder = %q, want $2", got)
	}
}

func testWhereBuilderPlaceholderNumbering(t *testing.T, d dialect.Name, placeholderAt func(int) string) {
	t.Helper()
	w := newWhereBuilder(d)
	w.eq("a", 1)
	w.eq("b", 2)
	w.addRaw("id > " + w.placeholder())
	w.args = append(w.args, 99)
	lim := w.placeholder()
	w.args = append(w.args, 11)
	got := w.clause() + " LIMIT " + lim
	for i, want := range []string{placeholderAt(1), placeholderAt(2), placeholderAt(3), placeholderAt(4)} {
		if !strings.Contains(got, want) {
			t.Fatalf("dialect %s: placeholder %d %q missing in %q", d, i+1, want, got)
		}
	}
	if len(w.args) != 4 {
		t.Fatalf("dialect %s: args len = %d, want 4", d, len(w.args))
	}
}

func TestWhereBuilder_PostgresPlaceholderNumberingWithRawAndLimit(t *testing.T) {
	t.Parallel()
	testWhereBuilderPlaceholderNumbering(t, dialect.PG, func(i int) string {
		return "$" + itoaWhere(i)
	})
}

func TestWhereBuilder_SQLitePlaceholderNumberingWithRawAndLimit(t *testing.T) {
	t.Parallel()
	testWhereBuilderPlaceholderNumbering(t, dialect.SQLite, func(int) string { return "?" })
}

func TestWhereBuilder_PostgresEqKnownBindsBool(t *testing.T) {
	t.Parallel()
	w := newWhereBuilder(dialect.PG)
	w.eqKnown("principal_known", 1)
	if len(w.args) != 1 {
		t.Fatalf("args len = %d, want 1", len(w.args))
	}
	b, ok := w.args[0].(bool)
	if !ok {
		t.Fatalf("PG eqKnown arg type = %T, want bool", w.args[0])
	}
	if !b {
		t.Fatalf("PG eqKnown arg = false, want true for known=1")
	}
	if !strings.Contains(w.clause(), "principal_known = $1") {
		t.Fatalf("PG eqKnown clause = %q, want principal_known = $1", w.clause())
	}
}

func TestWhereBuilder_SQLiteEqKnownBindsInt(t *testing.T) {
	t.Parallel()
	w := newWhereBuilder(dialect.SQLite)
	w.eqKnown("principal_known", 1)
	if len(w.args) != 1 {
		t.Fatalf("args len = %d, want 1", len(w.args))
	}
	if n, ok := w.args[0].(int); !ok || n != 1 {
		t.Fatalf("SQLite eqKnown arg = %v(%T), want int 1", w.args[0], w.args[0])
	}
}

func TestDurableStore_KnownArgDialectAware(t *testing.T) {
	t.Parallel()
	pg := &DurableStore{dialect: dialect.PG}
	if v, ok := pg.knownArg(1).(bool); !ok || !v {
		t.Fatalf("PG knownArg(1) = %v(%T), want bool true", pg.knownArg(1), pg.knownArg(1))
	}
	if v, ok := pg.knownArg(0).(bool); !ok || v {
		t.Fatalf("PG knownArg(0) = %v(%T), want bool false", pg.knownArg(0), pg.knownArg(0))
	}
	sqlite := &DurableStore{dialect: dialect.SQLite}
	if v, ok := sqlite.knownArg(1).(int); !ok || v != 1 {
		t.Fatalf("SQLite knownArg(1) = %v(%T), want int 1", sqlite.knownArg(1), sqlite.knownArg(1))
	}
}

func itoaWhere(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
