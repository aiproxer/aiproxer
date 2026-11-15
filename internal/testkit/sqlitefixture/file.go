// Package sqlitefixture provides SQLite test fixture configuration.
package sqlitefixture

import (
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/db"
)

// FileDSN configures a file-backed SQLite fixture with WAL and NORMAL sync.
// Callers testing other durability semantics should construct their own DSN.
func FileDSN(path string) (string, error) {
	if p := strings.TrimSpace(path); p == "" || p == ":memory:" {
		return "", errors.New("sqlitefixture: a file path is required")
	}
	dsn, err := db.SQLiteFileDSN(path)
	if err != nil {
		return "", err
	}
	return dsn + "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", nil
}
