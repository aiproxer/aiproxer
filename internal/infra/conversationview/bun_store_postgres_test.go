//go:build integration

package conversationview_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConversationView_PostgresContract(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversationview.EnsureSchema(ctx, migrateDB); err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	newDeps := func(t *testing.T) storecontract.Deps {
		t.Helper()
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = bunDB.Close() })
		if err := conversationview.EnsureSchema(openCtx, bunDB); err != nil {
			t.Fatal(err)
		}
		bs := conversationview.NewBunStore(bunDB)
		return storecontract.Deps{
			Store: bs,
			CreateALeg: func(ctx context.Context, aLegID string) error {
				if _, err := bunDB.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLegID).Exec(ctx); err != nil {
					return err
				}
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

	storecontract.Run(t, storecontract.Env{
		New:   newDeps,
		Spawn: func(fn func()) { go fn() },
	})
}

func TestConversationView_PostgresSecondStoreSeesCommittedRevision(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversationview.EnsureSchema(ctx, migrateDB); err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	openStore := func(t *testing.T) *conversationview.BunStore {
		t.Helper()
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
		if err != nil {
			t.Fatal(err)
		}
		if err := conversationview.EnsureSchema(openCtx, bunDB); err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		s := conversationview.NewBunStore(bunDB)
		t.Cleanup(func() { _ = bunDB.Close() })
		return s
	}

	s1 := openStore(t)
	s2 := openStore(t)

	aLegID := fmt.Sprintf("a_pg_committed_rev_%d", time.Now().UnixNano())
	require.NoError(t, s1.CreateALeg(ctx, aLegID))

	// s1 tags identity
	tagID := conversationview.MessageIdentity("v1:" + "1111111111111111111111111111111111111111111111111111111111111111")
	tagRes, err := s1.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: tagID, Reason: "pg_test"}})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), tagRes.StateRevision)

	// s2 sees the revision and tag
	snap2, err := s2.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), snap2.StateRevision)
	require.Len(t, snap2.NeverBackend, 1)
	assert.Equal(t, tagID, snap2.NeverBackend[0].Identity)

	// s2 puts steering overlay
	writer, err := sdkadapter.NewWriter(s2, aLegID, func(ctx context.Context) (aipapi.Call, conversationview.Snapshot, error) {
		return aipapi.Call{}, snap2, nil
	})
	require.NoError(t, err)
	stState, err := writer.Put(ctx, steering.PutRequest{
		OverlayID:           steering.OverlayID("ov1"),
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "sys msg"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              steering.ReasonCode("test"),
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), stState.Revision)

	// s1 sees steering overlay
	snap1, err := s1.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), snap1.StateRevision)
	require.Len(t, snap1.Steering, 1)
	assert.Equal(t, "ov1", snap1.Steering[0].OverlayID)
}
