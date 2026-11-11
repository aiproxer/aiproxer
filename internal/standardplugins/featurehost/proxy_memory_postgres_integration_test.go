//go:build integration

package featurehost

// PostgreSQL composed acceptance for Proxy Memory V1, issue #793.
// This reuses the default SQLite sentinel's actual generation, executor,
// capture writer, worker and HTTP projections over two independent PostgreSQL
// pools in one disposable schema. It is stub-backed: it is composed-topology
// evidence, not a live-provider or native-CLI session-continuity certificate.
//
// It is an opt-in acceptance run, not a CI gate. The freeze in
// .kiro/steering/delivery.md defers new gates until 2026-11-21, and the
// canonical PostgreSQL gate (make test-db-parity) already covers the store
// contract. Run it against a direct endpoint:
//
//	AIP_REQUIRE_POSTGRES=1 \
//	AIP_TEST_POSTGRES_DSN='postgres://.../disposable_test_database?sslmode=disable' \
//	AIP_TEST_POSTGRES_ADMIN_DSN="$AIP_TEST_POSTGRES_DSN" \
//	go test -tags=integration -run '^TestProxyMemorySentinel_PostgresDirect$' \
//	  -count=1 -v ./internal/standardplugins/featurehost
//
// The isolated-schema fixture is proxyMemoryPostgresSchema /
// proxyMemoryPostgresPool in proxy_memory_postgres_acceptance_test.go.

import (
	"testing"
)

func TestProxyMemorySentinel_PostgresDirect(t *testing.T) {
	// A uniquely named schema isolates the complete composed topology –
	// continuity, secure-session and memory migrations included – from the
	// shared test database.
	scopedDSN, schema := proxyMemoryPostgresSchema(t, "pm_accept")
	continuityDB := proxyMemoryPostgresPool(t, scopedDSN, schema, "continuity", 4)
	memoryDB := proxyMemoryPostgresPool(t, scopedDSN, schema, "memory", 4)

	runProxyMemorySentinelCrossPrincipalIsolationAndForget(t, continuityDB, memoryDB)
}
