//go:build integration

package journalstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestStoreScopedSourceKeyUp_Postgres_IdempotentWhenBaselineConstraintExists(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
	t.Cleanup(func() { _ = bunDB.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Fresh installs create metering_facts_store_source_event_key_key in baseline
	// DDL before 20260718000000 runs. Migrate leaves that constraint in place;
	// re-running the Up must tolerate SQLSTATE 42P07 (duplicate_table).
	if err := Migrate(ctx, bunDB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := storeScopedSourceKeyUp(ctx, bunDB); err != nil {
		if strings.Contains(err.Error(), "42P07") || strings.Contains(err.Error(), "already exists") {
			t.Fatalf("store-scoped source key migration must tolerate baseline constraint collision: %v", err)
		}
		t.Fatalf("storeScopedSourceKeyUp: %v", err)
	}
	if err := storeScopedSourceKeyUp(ctx, bunDB); err != nil {
		t.Fatalf("second storeScopedSourceKeyUp must stay idempotent: %v", err)
	}
	if err := VerifySchema(ctx, bunDB); err != nil {
		t.Fatalf("VerifySchema after idempotent Up: %v", err)
	}
}
