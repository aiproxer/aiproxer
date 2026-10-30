package journalstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase34_SchemaV2_BackfillIdentityColumnsFromPayload(t *testing.T) {
	t.Parallel()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSNInternal())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	payload := `{"fact_id":"bf-legacy","stream_id":"s","sequence":1,"identity_version":1,"source_revision":9,"source_event_kind":"cumulative","source_id":"src-legacy","kind":"cumulative","perspective":"customer","boundary":"frontend","lifecycle":"request","correlation":{},"source":"observed","authority":"frontend","presence":"complete","recorded_at":"2026-07-18T00:00:00Z"}`
	_, err = bunDB.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json,
	identity_version, source_revision, source_event_kind, source_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, "p34-bf", "bf-legacy", "s", 1, "legacy-key", "cumulative",
		"customer", "frontend", "request", 1, payload,
		0, 0, "", "").Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := BackfillSchemaV2IdentityForTest(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	var ver, rev int64
	var kind, src string
	err = bunDB.NewRaw(`
SELECT identity_version, source_revision, source_event_kind, source_id
FROM metering_facts WHERE store_id = ? AND fact_id = ?
`, "p34-bf", "bf-legacy").Scan(ctx, &ver, &rev, &kind, &src)
	if err != nil {
		t.Fatal(err)
	}
	if ver != metering.IdentityVersionV1 || rev != 9 || kind != "cumulative" || src != "src-legacy" {
		t.Fatalf("backfill ver=%d rev=%d kind=%q src=%q", ver, rev, kind, src)
	}
}
