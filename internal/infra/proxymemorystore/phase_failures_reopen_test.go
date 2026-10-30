package proxymemorystore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

// TestPhaseFailureEvidence_CommittedReopenDurability proves the durability
// boundary: a failure fact is durable only after its transaction commits, and a
// reopened store still sees exactly one immutable row with the frozen expiry.
// A replay after the reopen is a no-op.
func TestPhaseFailureEvidence_CommittedReopenDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "proxy-memory-reopen.db")
	open := func() *bun.DB {
		t.Helper()
		dsn := fmt.Sprintf("file:%s?cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		require.NoError(t, proxymemorystore.EnsureSchema(ctx, bunDB))
		return bunDB
	}

	owner := proxymemory.Owner{TenantID: "tenant-reopen", PrincipalID: "principal-reopen", ProjectKey: "project-reopen"}
	evidence := proxymemory.FailureEvidence{
		Owner:         owner,
		Phase:         proxymemory.FailurePhaseExtraction,
		StoreID:       "store-reopen",
		BillingCallID: "call-reopen",
		ClaimID:       "a_leg_reopen",
		ClaimRevision: 3,
		LeaseOwner:    "generation-reopen",
		LeaseUntilMS:  9_000,
		ErasureEpoch:  0,
		RetryAtMS:     8_000,
		Kind:          proxymemory.FailureKindInvalidResult,
		CompletedAtMS: 1_500,
		ExpiresAtMS:   7_500,
		RecordedAtMS:  1_600,
	}

	first := open()
	store := proxymemorystore.New(first, proxymemorystore.NewALegReader(first))
	outcome, err := store.RecordPhaseFailure(ctx, evidence)
	require.NoError(t, err)
	require.Equal(t, proxymemory.FailureRecordedStale, outcome,
		"the fact commits even when the claim row is already gone; it mutates no successor")
	require.NoError(t, first.Close())

	second := open()
	t.Cleanup(func() { _ = second.Close() })
	reopened := proxymemorystore.New(second, proxymemorystore.NewALegReader(second))
	var (
		count     int
		kind      string
		expiresAt int64
	)
	require.NoError(t, second.NewRaw(`SELECT COUNT(*) FROM proxy_memory_phase_failures`).Scan(ctx, &count))
	assert.Equal(t, 1, count, "the committed fact survives a store reopen exactly once")
	require.NoError(t, second.NewRaw(`SELECT failure_kind, expires_at_ms FROM proxy_memory_phase_failures WHERE store_id = ? AND billing_call_id = ? AND phase = ?`,
		evidence.StoreID, evidence.BillingCallID, evidence.Phase).Scan(ctx, &kind, &expiresAt))
	assert.Equal(t, proxymemory.FailureKindInvalidResult, kind)
	assert.Equal(t, int64(7_500), expiresAt, "the fixed expiry is not recalculated on reload")

	replay := evidence
	replay.RecordedAtMS = 2_000
	outcome, err = reopened.RecordPhaseFailure(ctx, replay)
	require.NoError(t, err)
	assert.Equal(t, proxymemory.FailureDuplicate, outcome, "an identical replay after reopen adds no row")
	require.NoError(t, second.NewRaw(`SELECT COUNT(*) FROM proxy_memory_phase_failures`).Scan(ctx, &count))
	assert.Equal(t, 1, count)
}
