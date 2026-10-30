package journalstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/stretchr/testify/require"
)

var phase4RedDSN atomic.Int64

// This is intentionally a pre-implementation behavioral check for Phase 4.
// The additive V2 projection table must exist after the normal journal
// migration path has run; it must not require a second migration runner.
func TestPhase4V2ProjectionSchemaExists(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", fmt.Sprintf("file:phase4-red-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", phase4RedDSN.Add(1)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	store, err := NewDurableStore(context.Background(), bunDB, DurableConfig{StoreID: "phase4-red"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	defer func() { _ = store.Close() }()

	var got int
	err = store.db.NewRaw("SELECT COUNT(*) FROM metering_components").Scan(context.Background(), &got)
	require.NoError(t, err)
}
