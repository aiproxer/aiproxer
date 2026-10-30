package journalstore_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/stretchr/testify/require"
)

// SQLite VerifySchema must reject a store whose B-leg linked-statement
// accelerator indexes are absent. R10's bounded indexed statement lookup
// degrades to a large scan without either index, so a missing accelerator must
// fail schema verification even though the canonical facts remain readable.
func TestVerifySchemaSQLiteRequiresLinkedStatementIndexes(t *testing.T) {
	ctx := context.Background()

	require.Contains(t, journalstore.V2BoundedIndexNames, meteringStoreBLegStatementIndexName,
		"candidate index must be part of the SQLite bounded-index contract")

	for _, name := range []string{
		meteringStoreBLegIndexName,
		meteringStoreBLegStatementIndexName,
	} {
		t.Run(name, func(t *testing.T) {
			bunDB := openPhase34SQLiteDB(t)
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			_, err := bunDB.ExecContext(ctx, `DROP INDEX IF EXISTS `+name)
			require.NoError(t, err)
			err = journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "missing %s must fail verification", name)
			require.Contains(t, err.Error(), name)
		})
	}
}

const (
	meteringStoreBLegIndexName          = "idx_metering_facts_store_bleg"
	meteringStoreBLegStatementIndexName = "idx_metering_facts_store_bleg_statement"
)
