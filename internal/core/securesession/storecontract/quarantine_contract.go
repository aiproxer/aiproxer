package storecontract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// RunQuarantineContracts exercises Phase-5 quarantine store semantics.
// Exercises durable quarantine store semantics.
func RunQuarantineContracts(t *testing.T, newStore func(*testing.T) app.Store) {
	t.Helper()

	t.Run("Quarantine_idempotent_setsStatusAndResumeIneligible", func(t *testing.T) {
		t.Parallel()
		testQuarantineIdempotent(t, newStore(t))
	})

	t.Run("Quarantine_firstWins_keepsExistingProvenance", func(t *testing.T) {
		t.Parallel()
		testQuarantineFirstWins(t, newStore(t))
	})

	t.Run("Quarantine_invalidInput_rejected", func(t *testing.T) {
		t.Parallel()
		testQuarantineInvalidInput(t, newStore(t))
	})

	t.Run("Quarantine_missingSession_notFound", func(t *testing.T) {
		t.Parallel()
		testQuarantineMissing(t, newStore(t))
	})
}

func testQuarantineIdempotent(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-q", "ws-q", fp, "a-leg-q", "sess-q")
	created, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	in := domain.QuarantineInput{
		SessionID:  created.SessionID,
		TurnID:     "turn-1",
		ReasonCode: "secret_guard_block",
		EventID:    "evt-1",
		At:         at,
	}
	if err := s.Quarantine(ctx, in); err != nil {
		t.Fatalf("Quarantine not implemented (want idempotent quarantine): %v", err)
	}
	got, err := s.LoadByID(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Status.IsQuarantined() {
		t.Fatalf("status want quarantined got %q", got.Status)
	}
	if got.ResumeEligible {
		t.Fatal("ResumeEligible must be false after quarantine")
	}
	if got.QuarantineReasonCode != "secret_guard_block" || got.QuarantineEventID != "evt-1" {
		t.Fatalf("quarantine metadata mismatch: reason=%q event=%q", got.QuarantineReasonCode, got.QuarantineEventID)
	}
	// Idempotent second call.
	if err := s.Quarantine(ctx, in); err != nil {
		t.Fatalf("second Quarantine must succeed idempotently: %v", err)
	}
	audits, err := s.Audit(ctx, created.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var blocked int
	for _, a := range audits {
		if a.Action == domain.QuarantineAuditActionSecretGuard && a.Result == domain.QuarantineAuditResultBlocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("after identical double Quarantine want exactly one secret_guard/blocked audit, got %d (total audits=%d)", blocked, len(audits))
	}
}

func testQuarantineFirstWins(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	fp, _ := twoFingerprints()
	cr := sampleCreate("owner-q", "ws-q", fp, "a-leg-q", "sess-q")
	created, err := s.Create(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Unix(1_800_000_100, 0).UTC()
	if err := s.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  created.SessionID,
		TurnID:     "turn-1",
		ReasonCode: "secret_guard_block",
		EventID:    "evt-1",
		At:         firstAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  created.SessionID,
		TurnID:     "turn-2",
		ReasonCode: "different_reason",
		EventID:    "evt-2",
		At:         time.Unix(1_800_000_200, 0).UTC(),
	}); err != nil {
		t.Fatalf("second distinct quarantine must be idempotent: %v", err)
	}
	got, err := s.LoadByID(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuarantineReasonCode != "secret_guard_block" || got.QuarantineEventID != "evt-1" {
		t.Fatalf("first-wins provenance mismatch: reason=%q event=%q", got.QuarantineReasonCode, got.QuarantineEventID)
	}
	if !got.QuarantinedAt.Equal(firstAt) {
		t.Fatalf("first-wins time mismatch: got %v want %v", got.QuarantinedAt, firstAt)
	}
	audits, err := s.Audit(ctx, created.SessionID, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var blocked int
	for _, a := range audits {
		if a.Action == domain.QuarantineAuditActionSecretGuard && a.Result == domain.QuarantineAuditResultBlocked {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("first-wins wants one blocked audit, got %d (audits=%d)", blocked, len(audits))
	}
}

func testQuarantineMissing(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	err := s.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  "missing-session",
		TurnID:     "t",
		ReasonCode: "secret_guard_block",
		EventID:    "e",
		At:         time.Unix(1, 0).UTC(),
	})
	if errors.Is(err, domain.ErrQuarantineUnimplemented) {
		t.Fatalf("Quarantine not implemented: %v", err)
	}
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("Quarantine missing session: got %v want %v", err, domain.ErrSessionNotFound)
	}
}

func testQuarantineInvalidInput(t *testing.T, s app.Store) {
	t.Helper()
	ctx := context.Background()
	err := s.Quarantine(ctx, domain.QuarantineInput{
		SessionID:  "sid",
		TurnID:     "turn",
		ReasonCode: "secret_guard_block",
		EventID:    "evt",
	})
	if !errors.Is(err, domain.ErrInvalidQuarantineInput) {
		t.Fatalf("Quarantine invalid input: got %v want %v", err, domain.ErrInvalidQuarantineInput)
	}
}
