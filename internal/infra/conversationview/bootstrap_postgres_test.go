//go:build integration

package conversationview_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/uptrace/bun"
)

func TestBootstrap_PostgresIndependentHandlesRollbackAndReopen(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	runBootstrapDurable(t, func() *bun.DB { return testkit.OpenPostgresBunForTest(t, dsn, 2) })
}
