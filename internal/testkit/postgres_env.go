package testkit

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Environment variable names for optional PostgreSQL integration tests.
const (
	// AIPTestPostgresDSN is the preferred runtime DSN for integration tests.
	// Dual-plane pooled gates treat this as the transaction-pooled runtime endpoint.
	AIPTestPostgresDSN = "AIP_TEST_POSTGRES_DSN"
	// AIPTestPostgresAdminDSN is the direct admin/bootstrap/cleanup/migration endpoint.
	// When unset, tests may reuse the runtime DSN if that endpoint has admin rights.
	AIPTestPostgresAdminDSN = "AIP_TEST_POSTGRES_ADMIN_DSN"
	// AIPManagedPostgresDSN is a legacy alias still accepted by [PostgresTestDSN] /
	// [PostgresRuntimeDSN] only. It is not an admin endpoint.
	AIPManagedPostgresDSN = "AIP_MANAGED_POSTGRES_DSN"
	// AIPRequirePostgres turns an otherwise skippable integration test into a
	// hard failure. CI and the explicit make target use it as the proof gate.
	AIPRequirePostgres = "AIP_REQUIRE_POSTGRES"
	// AIPRequirePostgresPooler turns pooled dual-endpoint integration tests into
	// hard failures when either admin or runtime DSN is missing.
	AIPRequirePostgresPooler = "AIP_REQUIRE_POSTGRES_POOLER"
	// AIPTestPostgresRuntimeIsPooler attests that AIP_TEST_POSTGRES_DSN reaches a
	// transaction pooler. Required by the pooled release gate; never inferred.
	AIPTestPostgresRuntimeIsPooler = "AIP_TEST_POSTGRES_RUNTIME_IS_POOLER"
)

// PostgresTestDSN returns a non-empty managed PostgreSQL DSN when AIPTestPostgresDSN or
// AIPManagedPostgresDSN is set. If both are set, AIPTestPostgresDSN wins.
func PostgresTestDSN() (dsn string, ok bool) {
	a := strings.TrimSpace(os.Getenv(AIPTestPostgresDSN))
	if a != "" {
		return a, true
	}
	b := strings.TrimSpace(os.Getenv(AIPManagedPostgresDSN))
	if b != "" {
		return b, true
	}
	return "", false
}

// PostgresRuntimeDSN is the dual-plane name for the transaction-pooled runtime DSN.
// It shares precedence with [PostgresTestDSN] (preferred test name, then legacy alias).
func PostgresRuntimeDSN() (dsn string, ok bool) {
	return PostgresTestDSN()
}

// PostgresAdminDSN returns the direct admin/bootstrap/cleanup DSN. Tests may
// reuse the runtime DSN when it has admin rights; an explicit admin DSN wins.
// No hostname inference or DSN rewriting is performed.
func PostgresAdminDSN() (dsn string, ok bool) {
	a := strings.TrimSpace(os.Getenv(AIPTestPostgresAdminDSN))
	if a != "" {
		return a, true
	}
	return PostgresRuntimeDSN()
}

// PostgresRequired reports whether AIP_REQUIRE_POSTGRES requests fail-closed behavior.
func PostgresRequired() bool {
	return envTruthy(AIPRequirePostgres)
}

// PostgresPoolerRequired reports whether AIP_REQUIRE_POSTGRES_POOLER requests
// fail-closed dual-endpoint pooled-gate behavior.
func PostgresPoolerRequired() bool {
	return envTruthy(AIPRequirePostgresPooler)
}

func envTruthy(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
}

// LookupDualPlanePostgresDSNs returns admin and runtime DSNs without logging them.
// Missing endpoints yield an actionable error that names env vars only.
func LookupDualPlanePostgresDSNs() (adminDSN, runtimeDSN string, err error) {
	admin, adminOK := PostgresAdminDSN()
	runtime, runtimeOK := PostgresRuntimeDSN()
	var missing []string
	if !adminOK {
		missing = append(missing, AIPTestPostgresAdminDSN)
	}
	if !runtimeOK {
		missing = append(missing, AIPTestPostgresDSN+" (or legacy "+AIPManagedPostgresDSN+")")
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("pooled PostgreSQL gate requires admin and runtime endpoints; missing: %s", strings.Join(missing, ", "))
	}
	return admin, runtime, nil
}

// SkipUnlessPostgres skips the test when no integration DSN is configured or when
// PostgreSQL integration tests are not requested via AIP_REQUIRE_POSTGRES=1.
func SkipUnlessPostgres(t *testing.T) string {
	t.Helper()
	dsn, ok := PostgresTestDSN()
	if !ok || !PostgresRequired() {
		if PostgresRequired() && !ok {
			t.Fatalf("PostgreSQL DSN is required: set %s or %s", AIPTestPostgresDSN, AIPManagedPostgresDSN)
		}
		if !ok {
			t.Skipf("set %s (or legacy %s) to run PostgreSQL integration test", AIPTestPostgresDSN, AIPManagedPostgresDSN)
		}
		t.Skipf("set %s=1 and %s to run PostgreSQL integration test", AIPRequirePostgres, AIPTestPostgresDSN)
	}
	return dsn
}

// DualPlanePostgresGate is the non-fatal decision for pooled dual-endpoint tests.
type DualPlanePostgresGate struct {
	AdminDSN   string
	RuntimeDSN string
	// Err is non-nil when either endpoint is missing (env names only; never DSNs).
	Err error
	// FailClosed is true when AIP_REQUIRE_POSTGRES_POOLER requests hard failure.
	FailClosed bool
}

// EvaluateDualPlanePostgresGate looks up admin/runtime DSNs without logging them.
// Explicit AIP_TEST_POSTGRES_RUNTIME_IS_POOLER=1 attestation is always required
// before pooled helpers may proceed — ambient admin/runtime DSNs alone are not
// enough (make qa must skip, not hang, against a direct endpoint).
func EvaluateDualPlanePostgresGate() DualPlanePostgresGate {
	admin, runtime, err := LookupDualPlanePostgresDSNs()
	if err == nil && !envTruthy(AIPTestPostgresRuntimeIsPooler) {
		err = fmt.Errorf("pooled PostgreSQL gate requires explicit runtime topology attestation: set %s=1", AIPTestPostgresRuntimeIsPooler)
	}
	return DualPlanePostgresGate{
		AdminDSN:   admin,
		RuntimeDSN: runtime,
		Err:        err,
		FailClosed: PostgresPoolerRequired(),
	}
}

// SkipUnlessPostgresPooled skips (or fails closed) unless both admin and runtime
// DSNs are configured and the runtime endpoint is explicitly attested as a
// transaction pooler. When AIP_REQUIRE_POSTGRES_POOLER is set, missing endpoints
// or missing attestation fail with an actionable message and never
// infer/rewrite hostnames.
func SkipUnlessPostgresPooled(t *testing.T) (adminDSN, runtimeDSN string) {
	t.Helper()
	gate := EvaluateDualPlanePostgresGate()
	if gate.Err == nil {
		return gate.AdminDSN, gate.RuntimeDSN
	}
	if gate.FailClosed {
		t.Fatalf("%v", gate.Err)
	}
	t.Skipf("%v", gate.Err)
	return "", ""
}
