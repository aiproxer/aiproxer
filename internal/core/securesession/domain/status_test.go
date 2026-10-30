package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestSessionStatus_activeConstant(t *testing.T) {
	t.Parallel()
	if domain.SessionStatusActive != "active" {
		t.Fatalf("SessionStatusActive: got %q want active", domain.SessionStatusActive)
	}
	if !domain.SessionStatusActive.IsActive() || domain.SessionStatusActive.IsQuarantined() {
		t.Fatal("active helpers mismatch")
	}
}

func TestSessionStatus_emptyTreatedAsActiveForMigration(t *testing.T) {
	t.Parallel()
	var rec domain.Record
	if rec.Status != "" {
		t.Fatalf("zero Status: got %q", rec.Status)
	}
	if !rec.Status.IsActive() {
		t.Fatal("empty Status must report IsActive for pre-migration rows")
	}
	if rec.Status.IsQuarantined() {
		t.Fatal("empty Status must not report IsQuarantined")
	}
}

func TestSessionStatus_quarantined(t *testing.T) {
	t.Parallel()
	s := domain.SessionStatusQuarantined
	if !s.IsQuarantined() || s.IsActive() {
		t.Fatalf("quarantined status helpers: active=%v quarantined=%v", s.IsActive(), s.IsQuarantined())
	}
}

func TestQuarantineInput_shape(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_700_000_000, 0).UTC()
	in := domain.QuarantineInput{
		SessionID:  "sid",
		TurnID:     "turn",
		ReasonCode: "secret_guard_block",
		EventID:    "evt",
		At:         at,
	}
	if in.SessionID == "" || in.TurnID == "" || in.ReasonCode == "" || in.EventID == "" || in.At.IsZero() {
		t.Fatalf("QuarantineInput incomplete: %+v", in)
	}
}

func TestQuarantineInput_ValidateRejectsIncompleteInput(t *testing.T) {
	t.Parallel()
	base := domain.QuarantineInput{
		SessionID:  "sid",
		TurnID:     "turn",
		ReasonCode: "secret_guard_block",
		EventID:    "evt",
		At:         time.Unix(1_700_000_000, 0).UTC(),
	}
	cases := []struct {
		name string
		in   domain.QuarantineInput
	}{
		{name: "session", in: func() domain.QuarantineInput { in := base; in.SessionID = ""; return in }()},
		{name: "turn", in: func() domain.QuarantineInput { in := base; in.TurnID = " "; return in }()},
		{name: "reason", in: func() domain.QuarantineInput { in := base; in.ReasonCode = ""; return in }()},
		{name: "event", in: func() domain.QuarantineInput { in := base; in.EventID = "\t"; return in }()},
		{name: "time", in: func() domain.QuarantineInput { in := base; in.At = time.Time{}; return in }()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.in.Validate(); !errors.Is(err, domain.ErrInvalidQuarantineInput) {
				t.Fatalf("Validate() = %v want ErrInvalidQuarantineInput", err)
			}
		})
	}
}

func TestPlanQuarantine_activeAndNoopPaths(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_700_000_100, 0).UTC()
	in := domain.QuarantineInput{
		SessionID:  "sid",
		TurnID:     "turn",
		ReasonCode: "secret_guard_block",
		EventID:    "evt",
		At:         at,
	}
	plan, err := domain.PlanQuarantine(domain.Record{}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Apply || plan.Status != domain.SessionStatusQuarantined || plan.ResumeEligible {
		t.Fatalf("active plan mismatch: %+v", plan)
	}
	if plan.QuarantinedAt != at || plan.ReasonCode != in.ReasonCode || plan.EventID != in.EventID {
		t.Fatalf("active plan provenance mismatch: %+v", plan)
	}
	if plan.AuditAction != domain.QuarantineAuditActionSecretGuard || plan.AuditResult != domain.QuarantineAuditResultBlocked {
		t.Fatalf("audit constants mismatch: %+v", plan)
	}

	existing := domain.Record{
		Status:               domain.SessionStatusQuarantined,
		QuarantinedAt:        time.Unix(1_700_000_050, 0).UTC(),
		QuarantineReasonCode: "first-wins",
		QuarantineEventID:    "evt-1",
		ResumeEligible:       false,
	}
	noop, err := domain.PlanQuarantine(existing, in)
	if err != nil {
		t.Fatal(err)
	}
	if noop.Apply {
		t.Fatalf("noop plan must not apply: %+v", noop)
	}
	if noop.QuarantinedAt != existing.QuarantinedAt || noop.ReasonCode != existing.QuarantineReasonCode || noop.EventID != existing.QuarantineEventID {
		t.Fatalf("noop plan must preserve existing provenance: %+v", noop)
	}
}

func TestErrSessionQuarantined_sentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(domain.ErrSessionQuarantined, domain.ErrSessionQuarantined) {
		t.Fatal("ErrSessionQuarantined must be stable sentinel")
	}
	if errors.Is(domain.ErrQuarantineUnimplemented, domain.ErrSessionQuarantined) {
		t.Fatal("unimplemented stub must not equal ErrSessionQuarantined")
	}
}
