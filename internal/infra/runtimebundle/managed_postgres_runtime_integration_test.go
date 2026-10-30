//go:build integration

package runtimebundle_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// TestBuild_postgresBothStores_processOwnsPersistenceLifecycle requires PostgreSQL;
// see testkit.AIPTestPostgresDSN (or legacy testkit.AIPManagedPostgresDSN).
//
// Certifies the process-owned continuity and secure-session PostgreSQL stores:
// candidate generation close must not dispose them, and ProcessServices.Close
// must. This replaces a stale CandidateHTTPCompile.Ledger().Len()==2 assertion
// that counted generation resources, not process persistence closers.
//
// Not parallel: shares the package PostgreSQL fixture and must isolate the
// process-owned close transaction from other package-parallel tests.
func TestBuild_postgresBothStores_processOwnsPersistenceLifecycle(t *testing.T) {
	dsn, ok := testkit.PostgresTestDSN()
	if !ok {
		t.Skipf("set %s (or legacy %s) to run integration test", testkit.AIPTestPostgresDSN, testkit.AIPManagedPostgresDSN)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:  config.ServerConfig{Address: "127.0.0.1:0"},
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: testRuntimeBundlePlugins(),
		Database: config.DatabaseConfig{
			MaxOpenConns: 2,
		},
		Continuity: config.ContinuityConfig{
			InMemory:    false,
			Store:       "postgres",
			PostgresDSN: dsn,
		},
		SecureSession: config.SecureSessionConfig{
			Store:               "postgres",
			PostgresDSN:         dsn,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}

	ps, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if ps == nil || cand == nil {
		t.Fatal("expected process and candidate owners")
	}
	if ps.Continuity == nil || ps.SecureSessions == nil {
		t.Fatal("expected process-owned continuity and secure-session stores")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()

	// 1) Both process-owned stores are usable after construction.
	contKey := fmt.Sprintf("rtbundle-pg-both-cont-%d", suffix)
	aleg, err := ps.Continuity.CreateALeg(ctx, contKey)
	if err != nil {
		t.Fatalf("continuity CreateALeg after build: %v", err)
	}
	if aleg.ALegID == "" {
		t.Fatal("continuity CreateALeg returned empty ALegID")
	}
	gotALeg, err := ps.Continuity.FetchALeg(ctx, aleg.ALegID)
	if err != nil {
		t.Fatalf("continuity FetchALeg after build: %v", err)
	}
	if gotALeg.ALegID != aleg.ALegID || gotALeg.ContinuityKey != contKey {
		t.Fatalf("continuity FetchALeg = %+v want id=%q key=%q", gotALeg, aleg.ALegID, contKey)
	}

	sessionID := domain.SessionID(fmt.Sprintf("rtbundle-pg-both-ss-%d", suffix))
	fp := resumeFingerprintForTest("rtbundle-pg-both-fp|", sessionID)
	ssRec, err := ps.SecureSessions.Create(ctx, domain.CreateRecord{
		SessionID:         sessionID,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "owner-pg-both", Issuer: "iss", Tenant: "ten"},
		Workspace:         domain.WorkspaceRef{ID: "ws-pg-both"},
		Policy:            domain.PolicyMetadata{PolicyVersion: "v1", AuditMode: "durable"},
		ALegID:            aleg.ALegID,
		ResumeEligible:    true,
		CreatedAt:         time.Unix(50_000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("secure-session Create after build: %v", err)
	}
	if ssRec.SessionID != sessionID {
		t.Fatalf("secure-session Create id = %q want %q", ssRec.SessionID, sessionID)
	}
	gotSS, err := ps.SecureSessions.LoadByID(ctx, sessionID)
	if err != nil {
		t.Fatalf("secure-session LoadByID after build: %v", err)
	}
	if gotSS.SessionID != sessionID || gotSS.ALegID != aleg.ALegID {
		t.Fatalf("secure-session LoadByID = %+v want id=%q aleg=%q", gotSS, sessionID, aleg.ALegID)
	}

	// 2) Candidate generation cleanup does not own process persistence.
	if err := cand.Close(); err != nil {
		t.Fatalf("candidate Close: %v", err)
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must remain open after candidate Close")
	}

	contKeyAfterCand := fmt.Sprintf("rtbundle-pg-both-cont-after-cand-%d", suffix)
	aleg2, err := ps.Continuity.CreateALeg(ctx, contKeyAfterCand)
	if err != nil {
		t.Fatalf("continuity CreateALeg after candidate Close: %v", err)
	}
	if _, err := ps.Continuity.FetchALeg(ctx, aleg2.ALegID); err != nil {
		t.Fatalf("continuity FetchALeg after candidate Close: %v", err)
	}
	if _, err := ps.Continuity.ResolveALeg(ctx, contKey); err != nil {
		t.Fatalf("continuity ResolveALeg of pre-close record after candidate Close: %v", err)
	}

	sessionID2 := domain.SessionID(fmt.Sprintf("rtbundle-pg-both-ss-after-cand-%d", suffix))
	fp2 := resumeFingerprintForTest("rtbundle-pg-both-fp-after-cand|", sessionID2)
	if _, err := ps.SecureSessions.Create(ctx, domain.CreateRecord{
		SessionID:         sessionID2,
		ResumeFingerprint: fp2,
		Owner:             domain.PrincipalRef{ID: "owner-pg-both-2", Issuer: "iss", Tenant: "ten"},
		Workspace:         domain.WorkspaceRef{ID: "ws-pg-both"},
		Policy:            domain.PolicyMetadata{PolicyVersion: "v1", AuditMode: "durable"},
		ALegID:            aleg2.ALegID,
		ResumeEligible:    true,
		CreatedAt:         time.Unix(50_001, 0).UTC(),
	}); err != nil {
		t.Fatalf("secure-session Create after candidate Close: %v", err)
	}
	if _, err := ps.SecureSessions.LoadByID(ctx, sessionID); err != nil {
		t.Fatalf("secure-session LoadByID of pre-close record after candidate Close: %v", err)
	}
	if _, err := ps.SecureSessions.LoadByID(ctx, sessionID2); err != nil {
		t.Fatalf("secure-session LoadByID after candidate Close: %v", err)
	}

	// 3) ProcessServices.Close disposes both persistence handles.
	if err := ps.Close(); err != nil {
		t.Fatalf("ProcessServices.Close: %v", err)
	}
	if !ps.Closed() {
		t.Fatal("ProcessServices must report Closed after Close")
	}

	// Fresh context so pre-close timeout/cancellation cannot satisfy closed proofs.
	closedCtx, closedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closedCancel()
	if err := closedCtx.Err(); err != nil {
		t.Fatalf("post-close observation context already done before probes: %v", err)
	}

	assertStoreClosed(t, "continuity FetchALeg after process Close", func() error {
		_, err := ps.Continuity.FetchALeg(closedCtx, aleg.ALegID)
		return err
	})
	assertStoreClosed(t, "secure-session LoadByID after process Close", func() error {
		_, err := ps.SecureSessions.LoadByID(closedCtx, sessionID)
		return err
	})
}

// resumeFingerprintForTest derives a collision-resistant TokenFingerprint from a
// per-run session ID and domain separator so repeated executions against the
// same durable PostgreSQL database do not violate resume-fingerprint uniqueness.
func resumeFingerprintForTest(domainSep string, sessionID domain.SessionID) domain.TokenFingerprint {
	sum := sha256.Sum256([]byte(domainSep + string(sessionID)))
	return domain.TokenFingerprint(sum)
}

// assertStoreClosed requires the actual database/sql closed sentinel (or a
// wrapper around it); arbitrary non-nil or same-text errors are rejected.
func assertStoreClosed(t *testing.T, op string, call func() error) {
	t.Helper()
	err := call()
	if err == nil {
		t.Fatalf("%s: want closed-handle error, got nil", op)
	}
	if class, ok := closedHandleClassification(err); !ok {
		t.Fatalf("%s: want closed-handle error, got unexpected classification %s (err=%v)", op, class, err)
	}
}

func closedHandleClassification(err error) (classification string, ok bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return "context.Canceled (not closed-handle proof)", false
	case errors.Is(err, context.DeadlineExceeded):
		return "context.DeadlineExceeded (not closed-handle proof)", false
	case errors.Is(err, sql.ErrNoRows):
		return "sql.ErrNoRows (not closed-handle proof)", false
	}

	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "no rows") {
		return fmt.Sprintf("live-store miss %q", msg), false
	}

	// database/sql's errDBClosed is unexported. The integration test captures
	// its process-wide sentinel identity from a closed sql.DB; only that exact
	// error (or an ordinary %w wrapper around it) proves this handle closed.
	if errors.Is(err, actualDatabaseSQLClosedError) {
		return "closed-handle (database/sql sentinel)", true
	}
	return fmt.Sprintf("unclassified/non-closed error %q", msg), false
}
