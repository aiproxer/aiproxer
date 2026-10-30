//go:build ignore

// Manual fixture regenerator for golden legacy SQLite compat tests.
//
// This program OVERWRITES the committed testdata files:
//   - internal/core/securesession/adapters/bunstore/testdata/legacy_sqlite.db
//   - internal/core/continuity/bunstore/testdata/legacy_sqlite.db
//
// Run only when intentionally refreshing fixtures after a schema change — not
// during normal test runs (compat tests copy fixtures into t.TempDir() so they
// never mutate the committed files).
//
// From repo root:
//
//	go run ./internal/core/testdata/genlegacyfixtures/main.go
//
// Note: requires the legacy hand-written sqlite adapters; if those are removed,
// regenerate fixtures via an equivalent one-off script or restore adapters temporarily.
package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	continuitysqlite "github.com/aiproxer/aiproxer/internal/core/continuity/sqlitestore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/sqlite"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func main() {
	root := filepath.Join("internal", "core")
	if err := genSecureSession(filepath.Join(root, "securesession", "adapters", "bunstore", "testdata", "legacy_sqlite.db")); err != nil {
		panic(err)
	}
	if err := genContinuity(filepath.Join(root, "continuity", "bunstore", "testdata", "legacy_sqlite.db")); err != nil {
		panic(err)
	}
}

func genSecureSession(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Remove(path)
	s, err := sqlite.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	var fp domain.TokenFingerprint
	fp[0] = 0xab
	cr := domain.CreateRecord{
		SessionID: "legacy-sess-1", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "owner"}, Workspace: domain.WorkspaceRef{ID: "ws"},
		ClientHints: domain.ClientHints{ClientSessionID: "hint"},
		Policy:      domain.PolicyMetadata{PolicyVersion: "v1", TranscriptEnabled: true, AuditMode: "optional"},
		ALegID:      "a-leg-legacy", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	}
	rec, err := s.Create(ctx, cr)
	if err != nil {
		return err
	}
	return s.Quarantine(ctx, domain.QuarantineInput{
		SessionID: rec.SessionID, TurnID: "turn-1",
		ReasonCode: "legacy-fixture", EventID: "evt-1", At: time.Unix(2, 0),
	})
}

func genContinuity(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	_ = os.Remove(path)
	s, err := continuitysqlite.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	leg, err := s.CreateALeg(ctx, "legacy-ck")
	if err != nil {
		return err
	}
	bl, err := s.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		return err
	}
	return s.RecordAttempt(ctx, aipapi.AttemptRecord{
		BLegID: bl.BLegID, ALegID: leg.ALegID, Seq: bl.Seq,
		BackendID: "stub", EffectiveModel: "m",
		StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
		Outcome: aipapi.AttemptSuccess, Reason: "ok",
	})
}
