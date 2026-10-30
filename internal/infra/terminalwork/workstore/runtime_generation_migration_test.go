package workstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestRuntimeGeneration_Migration_OldRowsDecodeAsLegacy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy-rows.db")
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Pre-3.6 schema without runtime identity columns.
	if _, err := sqlDB.Exec(`
CREATE TABLE economic_terminal_work (
  store_id TEXT NOT NULL,
  work_id TEXT NOT NULL,
  source_key TEXT NOT NULL,
  identity_version INTEGER NOT NULL,
  payload_version INTEGER NOT NULL,
  kind TEXT NOT NULL,
  state TEXT NOT NULL,
  provider_id TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  attempt_id TEXT NOT NULL DEFAULT '',
  trace_id TEXT NOT NULL DEFAULT '',
  generation_id TEXT NOT NULL DEFAULT '',
  bound_provider_id TEXT NOT NULL DEFAULT '',
  rating_id TEXT NOT NULL DEFAULT '',
  fact_id TEXT NOT NULL DEFAULT '',
  lease_set_id TEXT NOT NULL DEFAULT '',
  payload_json TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_retry_at_unix INTEGER NOT NULL DEFAULT 0,
  claim_owner_id TEXT NOT NULL DEFAULT '',
  claim_expires_at_unix INTEGER NOT NULL DEFAULT 0,
  error_code TEXT NOT NULL DEFAULT '',
  error_permanent INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  created_at_unix INTEGER NOT NULL,
  updated_at_unix INTEGER NOT NULL,
  PRIMARY KEY (store_id, work_id),
  UNIQUE (store_id, source_key)
)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixNano()
	if _, err := sqlDB.Exec(
		`
INSERT INTO economic_terminal_work(
  store_id, work_id, source_key, identity_version, payload_version, kind, state,
  provider_id, generation_id, bound_provider_id, payload_json, created_at_unix, updated_at_unix
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"legacy", "tw_legacy", "v1:sk_legacy", 1, 1,
		string(sdk.WorkKindSettleRequestProvider), string(sdk.WorkStatePending),
		"quota", "exec-1", "quota", `{"handles":["h"]}`, now, now,
	); err != nil {
		t.Fatal(err)
	}

	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	ctx := context.Background()
	if err := workstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	store, err := workstore.NewDurableStore(ctx, bunDB, workstore.DurableConfig{StoreID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.GetByWorkID(ctx, "tw_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Versions.RuntimeIdentity() != terminalwork.RuntimeIdentityLegacy {
		t.Fatalf("identity=%v want legacy; versions=%+v", rec.Versions.RuntimeIdentity(), rec.Versions)
	}
	if rec.Versions.RuntimeInstanceID != "" || rec.Versions.RuntimeGenerationID != "" {
		t.Fatalf("expected empty runtime identity, got %+v", rec.Versions)
	}
	if rec.Versions.ExecutableGenerationID() != "exec-1" {
		t.Fatalf("executable=%q", rec.Versions.ExecutableGenerationID())
	}
}
