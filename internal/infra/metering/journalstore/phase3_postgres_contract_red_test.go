//go:build integration

package journalstore_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

// Phase 3.1 RED contracts on direct and transaction-pooled PostgreSQL
// (requirements 11.3–11.5, 13.1, 13.6; design D12, D17).
// Skip deterministically when DSNs are unset. Isolate via UniquePostgresStoreID.

func TestPhase3_PostgresDirectJournalContracts(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)

	open := func(t *testing.T, storeID string) phase3Journal {
		t.Helper()
		t.Cleanup(func() {
			testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
		})
		return newPostgresJournal(t, dsn, storeID)
	}
	runPhase3JournalContracts(t, phase3Adapter{
		name:     "postgres-direct",
		open:     open,
		openPeer: open,
		reopen:   open,
		uniqueID: testkit.UniquePostgresStoreID,
	})
}

func TestPhase3_PostgresPooledJournalContracts(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	ensurePooledJournalSchema(t, adminDSN)

	open := func(t *testing.T, storeID string) phase3Journal {
		t.Helper()
		store, _ := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)
		return store
	}
	runPhase3JournalContracts(t, phase3Adapter{
		name:     "postgres-pooled",
		open:     open,
		openPeer: open,
		reopen:   open,
		uniqueID: testkit.UniquePostgresStoreID,
	})
}
