//go:build integration

package bunstore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func poolFromConfig(t *testing.T, d config.DatabaseConfig) db.PoolSettings {
	t.Helper()
	poolCfg, err := config.ParseDatabasePoolSettings(d)
	if err != nil {
		t.Fatal(err)
	}
	return db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
}

// TestPostgres_restart_BLEgAttemptsResolve exercises durable continuity against a real PostgreSQL
// instance when AIP_TEST_POSTGRES_DSN (or legacy AIP_MANAGED_POSTGRES_DSN) is set.
func TestPostgres_restart_BLEgAttemptsResolve(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	pool := poolFromConfig(t, config.DatabaseConfig{MaxOpenConns: 2})
	ck := fmt.Sprintf("pg-bun-int-%d", time.Now().UnixNano())

	bunDB1, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := NewWithContext(ctx, bunDB1)
	if err != nil {
		_ = bunDB1.Close()
		t.Fatal(err)
	}
	leg, err := s1.CreateALeg(ctx, ck)
	if err != nil {
		_ = s1.Close()
		t.Fatal(err)
	}
	bleg, err := s1.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		_ = s1.Close()
		t.Fatal(err)
	}
	rec := aipapi.AttemptRecord{
		BLegID: bleg.BLegID, ALegID: leg.ALegID, Seq: bleg.Seq,
		BackendID: "stub-pg", EffectiveModel: "m",
		StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
		Outcome: aipapi.AttemptSuccess, Reason: "ok",
	}
	if err := s1.RecordAttempt(ctx, rec); err != nil {
		_ = s1.Close()
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	bunDB2, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB2.Close() })
	s2, err := NewWithContext(ctx, bunDB2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	got, err := s2.ResolveALeg(ctx, ck)
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != leg.ALegID {
		t.Fatalf("a-leg id %q want %q", got.ALegID, leg.ALegID)
	}
	rows, err := s2.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("attempts %+v", rows)
	}
	if rows[0].BackendID != "stub-pg" || rows[0].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("row %+v", rows[0])
	}
}

func TestPostgres_migrateTwice_idempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	pool := poolFromConfig(t, config.DatabaseConfig{MaxOpenConns: 2})
	bunDB, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	s, err := NewWithContext(ctx, bunDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := runContinuitySchemaMigrate(ctx, bunDB); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var applied int
	if err := bunDB.NewRaw(
		`SELECT count(*) FROM bun_continuity_migrations WHERE name = ?`, BaselineMigrationName,
	).Scan(ctx, &applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("expected one applied baseline migration row, got %d", applied)
	}
}

func TestPostgres_NextBLeg_concurrent_uniqueSeqs(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	pool := poolFromConfig(t, config.DatabaseConfig{MaxOpenConns: 64})
	bunDB, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	s, err := NewWithContext(ctx, bunDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	leg, err := s.CreateALeg(ctx, fmt.Sprintf("pg-bseq-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	const n = 48
	seqs := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			b, err := s.NextBLeg(ctx, leg.ALegID)
			seqs[i] = b.Seq
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d: %v", i, e)
		}
	}
	sort.Ints(seqs)
	for want := 1; want <= n; want++ {
		if seqs[want-1] != want {
			t.Fatalf("seqs=%v want 1..%d unique contiguous", seqs, n)
		}
	}
}
