package journalstore

import (
	"strings"
	"testing"
)

func TestPostgresStoreScopedSourceKeySQL_ToleratesDuplicateRelation(t *testing.T) {
	t.Parallel()
	// Fresh installs create metering_facts_store_source_event_key_key in the
	// baseline DDL. Re-adding that UNIQUE constraint raises SQLSTATE 42P07
	// (duplicate_table / relation already exists), not only 42710 duplicate_object.
	sql := strings.Join(postgresStoreScopedSourceKeyStmts(), "\n")
	if !strings.Contains(sql, "duplicate_table") {
		t.Fatal("PG store-scoped source key migration must tolerate SQLSTATE 42P07 duplicate_table when baseline already created metering_facts_store_source_event_key_key")
	}
}
