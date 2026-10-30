package controlplane_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestEventJSONGoldenRoundTrip_allDetailVariants(t *testing.T) {
	t.Parallel()
	now := time.Date(2020, 1, 1, 0, 0, 1, 0, time.UTC)
	cases := []struct {
		name string
		ev   controlplane.Event
	}{
		{
			name: "auth",
			ev: controlplane.Event{
				Category: controlplane.CategoryAuth, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail:         &controlplane.AuthDetail{Outcome: "allow", ReasonCode: "ok"},
			},
		},
		{
			name: "session",
			ev: controlplane.Event{
				Category: controlplane.CategorySession, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail:         &controlplane.SessionDetail{Action: controlplane.SessionActionCreated, SessionID: "sess-1"},
			},
		},
		{
			name: "attempt",
			ev: controlplane.Event{
				Category: controlplane.CategoryAttempt, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail: &controlplane.AttemptDetail{
					Surfaced: controlplane.AttemptSurfacedSurfaced, Outcome: controlplane.AttemptOutcomeSucceeded,
					BackendID: "openai", Model: "gpt-4o",
				},
			},
		},
		{
			name: "usage",
			ev: controlplane.Event{
				Category: controlplane.CategoryUsage, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail: &controlplane.UsageDetail{
					Plane: controlplane.UsagePlaneObserved, Availability: controlplane.UsageAvailabilityObserved,
					InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
				},
			},
		},
		{
			name: "accounting_authority",
			ev: controlplane.Event{
				Category: controlplane.CategoryAccountingAuthority, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail: &controlplane.AccountingAuthorityDetail{
					RuleID: "tenant.quota", Outcome: controlplane.AccountingOutcomeReserve,
					Authority: controlplane.AccountingAuthoritySourceReserved, Unit: "tokens",
					EvidenceState: controlplane.EvidenceRecorded, RedactionState: controlplane.RedactionNone,
				},
			},
		},
		{
			name: "policy",
			ev: controlplane.Event{
				Category: controlplane.CategoryPolicy, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail:         &controlplane.PolicyDetail{Stage: "admission", Outcome: "deny", Effect: "swallow"},
			},
		},
		{
			name: "audit",
			ev: controlplane.Event{
				Category: controlplane.CategoryAudit, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail:         &controlplane.AuditDetail{Action: "transcript.view", Result: "ok"},
			},
		},
		{
			name: "lifecycle",
			ev: controlplane.Event{
				Category: controlplane.CategoryLifecycle, OccurredAt: now, RecordedAt: now,
				Visibility: controlplane.VisibilityDefault, EvidenceState: controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionNone,
				Detail:         &controlplane.LifecycleDetail{Stage: "shutdown", Action: "drain"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back controlplane.Event
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			raw2, err := json.Marshal(back)
			if err != nil {
				t.Fatalf("remarshal: %v", err)
			}
			if !bytes.Equal(raw, raw2) {
				t.Fatalf("JSON not byte-identical after round-trip\ngot:  %s\nwant: %s", raw2, raw)
			}
			if err := back.Validate(); err != nil {
				t.Fatalf("round-tripped event invalid: %v", err)
			}
		})
	}
}

func TestEventJSONGoldenRoundTrip_richAuthWithCorrelation(t *testing.T) {
	t.Parallel()
	now := time.Time{}.Add(time.Second)
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 7},
		SourceEventKey: "auth:trace-1:allow:ok",
		Category:       controlplane.CategoryAuth,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", SessionID: "sess-1"},
		Source:         controlplane.SourceRef{Name: "authsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail:         &controlplane.AuthDetail{Outcome: "allow", ReasonCode: "ok"},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := string(raw)
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if string(raw2) != want {
		t.Fatalf("byte drift after round-trip\ngot:  %s\nwant: %s", raw2, want)
	}
	if back.Auth() == nil || back.Auth().Outcome != "allow" {
		t.Fatalf("auth detail lost")
	}
	if back.Session() != nil || back.Attempt() != nil {
		t.Fatalf("unexpected extra detail blocks")
	}
}

func TestEventJSONGoldenRoundTrip_accountingAuthorityWithScope(t *testing.T) {
	t.Parallel()
	now := time.Time{}.Add(2 * time.Second)
	ev := controlplane.Event{
		ID:             controlplane.EventID{StoreID: "mem", Sequence: 8},
		SourceEventKey: "accounting:trace-1:reserve:ok",
		Category:       controlplane.CategoryAccountingAuthority,
		OccurredAt:     now,
		RecordedAt:     now,
		Correlation:    controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1", BLegID: "b-1"},
		Scope:          controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal-1")},
		Source:         controlplane.SourceRef{Name: "accountingsink", Version: "v1"},
		Visibility:     controlplane.VisibilityDefault,
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionNone,
		Detail: &controlplane.AccountingAuthorityDetail{
			Correlation: controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1", BLegID: "b-1"},
			Scope:       controlplane.ScopeSnapshot{PrincipalID: scope.Known("principal-1")},
			RuleID:      "tenant.quota", RuleType: "quota",
			Outcome: controlplane.AccountingOutcomeReserve, ReasonCode: "reservation_required",
			Authority: controlplane.AccountingAuthoritySourceReserved, ReservationID: "reservation-1",
			Unit: "tokens", Limit: 1000, Consumed: 750, Reserved: 250, Remaining: 0,
			WindowStart: now, WindowEnd: now.Add(time.Hour), WindowResetAt: now.Add(2 * time.Hour),
			EvidenceState: controlplane.EvidenceRecorded, RedactionState: controlplane.RedactionNone,
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := string(raw)
	var back controlplane.Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if string(raw2) != want {
		t.Fatalf("byte drift after round-trip\ngot:  %s\nwant: %s", raw2, want)
	}
}
