package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
)

// VerifyPostgresQueryRowContains runs a read-only PostgreSQL metadata query and
// checks that the returned text contains every required fragment.
func VerifyPostgresQueryRowContains(ctx context.Context, bunDB *bun.DB, description, query string, args []any, fragments ...string) error {
	if ctx == nil {
		return ErrNilContext
	}
	if bunDB == nil {
		return ErrNilDB
	}
	var raw string
	if err := bunDB.QueryRowContext(ctx, query, args...).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: verify postgres %s: missing", description)
		}
		return fmt.Errorf("db: verify postgres %s: %w", description, err)
	}
	lower := strings.ToLower(raw)
	for _, fragment := range fragments {
		if !strings.Contains(lower, strings.ToLower(fragment)) {
			return fmt.Errorf("db: verify postgres %s: unexpected definition %q", description, raw)
		}
	}
	return nil
}
