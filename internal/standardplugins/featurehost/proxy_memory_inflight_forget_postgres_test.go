//go:build integration

package featurehost

// PostgreSQL composed in-flight forget fencing for issue #793.
//
// This runs the same composed scenario as TestProxyMemoryInflightForgetFencing
// (real generation, real worker claims, projected HTTP forget) against two
// independent PostgreSQL pools inside the shared disposable schema fixture.
// It is stub-backed composed evidence, not a live-provider or native-client
// session-continuity certificate.
//
// It is an opt-in acceptance run, not a CI gate: no workflow invokes it. It
// requires a direct endpoint and the fail-closed gate:
//
//	AIP_REQUIRE_POSTGRES=1 \
//	AIP_TEST_POSTGRES_DSN='postgres://.../disposable_test_database?sslmode=disable' \
//	AIP_TEST_POSTGRES_ADMIN_DSN="$AIP_TEST_POSTGRES_DSN" \
//	go test -tags=integration -run '^TestProxyMemoryInflightForgetFencing_PostgresDirect$' \
//	  -count=1 -v ./internal/standardplugins/featurehost
//
// The schema/DSN lifecycle and its fail-closed DSN validation belong to the
// shared fixture (proxyMemoryPostgresSchema / proxyMemoryPostgresPool in
// proxy_memory_postgres_acceptance_test.go) provided by the predecessor
// branch of PR #1778 (pinned head c10a7adc55afb8e7a31c965528e18f88603cf5e7,
// this PR's base). This file owns only the test-specific prefix and pool
// names.

import (
	"testing"
)

// TestProxyMemoryInflightForgetFencing_PostgresDirect runs the composed
// in-flight forget fencing scenario on direct PostgreSQL.
func TestProxyMemoryInflightForgetFencing_PostgresDirect(t *testing.T) {
	scopedDSN, schema := proxyMemoryPostgresSchema(t, "pm_inflight_forget")
	continuityDB := proxyMemoryPostgresPool(t, scopedDSN, schema, "continuity", inflightPostgresPoolSize)
	memoryDB := proxyMemoryPostgresPool(t, scopedDSN, schema, "memory", inflightPostgresPoolSize)

	runProxyMemoryInflightForgetFencing(t, continuityDB, memoryDB)
}
