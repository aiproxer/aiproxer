package conversationview_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newTestBunStore(t *testing.T) (*conversationview.BunStore, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, conversationview.EnsureSchema(ctx, bunDB))

	cleanup := func() {
		_ = bunDB.Close()
	}
	return conversationview.NewBunStore(bunDB), cleanup
}

func newBunStoreDeps(t *testing.T) storecontract.Deps {
	t.Helper()
	bs, cleanup := newTestBunStore(t)
	t.Cleanup(cleanup)
	return storecontract.Deps{
		Store: bs,
		CreateALeg: func(ctx context.Context, aLegID string) error {
			return bs.CreateALeg(ctx, aLegID)
		},
		DeleteALeg: func(ctx context.Context, aLegID string) error {
			return bs.DeleteALeg(ctx, aLegID)
		},
		GetOverlay: func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error) {
			return bs.GetOverlay(ctx, aLegID, overlayID)
		},
	}
}

func TestConversationView_BunContract_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.Run(t, storecontract.Env{
		New: func(t *testing.T) storecontract.Deps {
			t.Helper()
			return newBunStoreDeps(t)
		},
		Spawn: func(fn func()) { go fn() },
	})
}

func TestConversationView_Schema_SQLite(t *testing.T) {
	t.Parallel()
	bs, cleanup := newTestBunStore(t)
	defer cleanup()
	ctx := context.Background()
	for _, tbl := range []string{"a_leg_conversation_view_state", "a_leg_never_backend_messages", "a_leg_steering_overlays"} {
		var n int
		err := bs.DB().NewRaw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(ctx, &n)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "table %s missing", tbl)
	}
}

func TestConversationView_RestartSurvival_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cv_restart.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, conversationview.EnsureSchema(ctx, bunDB))

	bs1 := conversationview.NewBunStore(bunDB)
	aLegID := "a_restart_cv_12345678901234567890123456789012"
	require.NoError(t, bs1.CreateALeg(ctx, aLegID))

	id := conversationview.MessageIdentity("v1:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err = bs1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "test_reason"}})
	require.NoError(t, err)
	_, err = bs1.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov_restart",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleAssistant, Text: "steer text"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "r",
	})
	require.NoError(t, err)
	snapBefore, err := bs1.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snapBefore.NeverBackend, 1)
	require.Len(t, snapBefore.Steering, 1)
	require.NoError(t, bunDB.Close())

	sqlDB2, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB2.Close() })
	sqlDB2.SetMaxOpenConns(1)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB2.Close() })

	bs2 := conversationview.NewBunStore(bunDB2)
	snapAfter, err := bs2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, snapBefore.StateRevision, snapAfter.StateRevision)
	require.Len(t, snapAfter.NeverBackend, 1)
	assert.Equal(t, id, snapAfter.NeverBackend[0].Identity)
	require.Len(t, snapAfter.Steering, 1)
	assert.Equal(t, "ov_restart", snapAfter.Steering[0].OverlayID)
	assert.Equal(t, "steer text", snapAfter.Steering[0].Message.Text)
}
