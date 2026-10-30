package conversationview_test

import (
	"testing"
)

// TestDBParity_SQLite is the canonical parity entry point for conversationview persistence on SQLite.
func TestDBParity_SQLite(t *testing.T) { //nolint:paralleltest // delegates to TestConversationView_BunContract_SQLite, which parallelizes itself; a second Parallel on the same T would panic.
	t.Run("conversation", TestConversationView_BunContract_SQLite)
	t.Run("bootstrap", TestBootstrap_SQLiteIndependentHandlesRollbackAndReopen)
}
