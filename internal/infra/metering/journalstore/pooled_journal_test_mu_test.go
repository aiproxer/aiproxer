//go:build integration

package journalstore_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestReentrantTBMutex_NestedLockSameTDoesNotDeadlock(t *testing.T) {
	var m reentrantTBMutex
	m.Lock(t)
	m.Lock(t) // must return; non-reentrant mutex would hang the package timeout
}

func TestOpenSharedPooledJournalStore_NestedOpenSameTestDoesNotDeadlock(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	idA := testkit.UniquePostgresStoreID("nest-a")
	idB := testkit.UniquePostgresStoreID("nest-b")
	// store_id_isolation opens primary + peer; nested acquire must not deadlock.
	_, _ = openSharedPooledJournalStore(t, adminDSN, runtimeDSN, idA)
	_, _ = openSharedPooledJournalStore(t, adminDSN, runtimeDSN, idB)
}
