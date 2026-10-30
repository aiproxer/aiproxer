//go:build integration

package conversationview_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for conversationview persistence on PostgreSQL.
func TestDBParity_PostgresDirect(t *testing.T) {
	_ = testkit.SkipUnlessPostgres(t)
	t.Run("conversation", TestConversationView_PostgresContract)
	t.Run("bootstrap", TestBootstrap_PostgresIndependentHandlesRollbackAndReopen)
}
