package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// advisoryAuthorityState stages the admission state the authority port reports
// for a request matched only by advisory rules: allowed without a reservation,
// with the advisory rule IDs recorded for ApplyUsage.
func advisoryAuthorityState(advisoryRuleIDs ...string) attemptAuthorityState {
	return attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(80),
		admissionResult: usagelimit.AdmissionResult{
			Allowed:         true,
			AdvisoryRuleIDs: append([]string(nil), advisoryRuleIDs...),
		},
	}
}

// TestApplyAdvisoryUsageUpdatesAdvisoryWindowWithoutReservation pins
// requirement 7.7 at the runtime boundary: a request matching only an advisory
// rule (not reserved) still applies final usage through the injected port.
// ApplyAdvisoryUsage is NOT gated by IsActive()/Reserved, and the command
// carries the advisory rule IDs, the event usage, a request count of one, and
// the deterministic lifecycle source key. The advisory-window mutation itself
// is Enterprise rule semantics (TestEnterpriseRuleAdvisoryUsage*).
func TestApplyAdvisoryUsageUpdatesAdvisoryWindowWithoutReservation(t *testing.T) {
	t.Parallel()

	rec := &recordingAuthorityService{}
	lifecycle := newAuthorityLifecycle(rec, nil, advisoryAuthorityState("tenant.input.advisory"), authorityCandidate())

	if lifecycle.IsActive() {
		t.Fatal("IsActive must be false for an unreserved advisory-only request")
	}

	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 150, TotalTokens: 150}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("ApplyAdvisoryUsage = false, want true (advisory window must update)")
	}
	if got := rec.applyUsageCalls.Load(); got != 1 {
		t.Fatalf("ApplyUsage calls = %d, want 1", got)
	}
	cmd := rec.lastApplyUsage()
	if len(cmd.RuleIDs) != 1 || cmd.RuleIDs[0] != "tenant.input.advisory" {
		t.Fatalf("ApplyUsage RuleIDs = %#v, want [tenant.input.advisory]", cmd.RuleIDs)
	}
	if cmd.Kind != usagelimit.SettlementKindFinal {
		t.Fatalf("ApplyUsage kind = %q, want %q", cmd.Kind, usagelimit.SettlementKindFinal)
	}
	if !cmd.UsagePresent {
		t.Fatal("ApplyUsage UsagePresent = false, want true for a usage event")
	}
	if cmd.Usage.InputTokens != 150 || cmd.Usage.TotalTokens != 150 {
		t.Fatalf("ApplyUsage usage = %#v, want input/total 150", cmd.Usage)
	}
	if cmd.RequestCount != (usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1}) {
		t.Fatalf("ApplyUsage request count = %#v, want one request", cmd.RequestCount)
	}
	if cmd.SourceKey != "trace-1|bleg-1|advisory_usage" {
		t.Fatalf("ApplyUsage source key = %q, want the deterministic lifecycle key", cmd.SourceKey)
	}
	if cmd.Correlation.TraceID != "trace-1" || cmd.Correlation.BLegID != "bleg-1" {
		t.Fatalf("ApplyUsage correlation = %#v, want the admission correlation", cmd.Correlation)
	}

	t.Run("no usage falls back to the preflight estimate", func(t *testing.T) {
		t.Parallel()

		rec := &recordingAuthorityService{}
		state := advisoryAuthorityState("tenant.input.advisory")
		state.admissionInput.PreflightUsage = usagelimit.PreflightUsage{InputTokens: 42, TotalTokens: 42}
		lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

		if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}); !applied {
			t.Fatal("ApplyAdvisoryUsage = false, want true (estimate fallback must still apply)")
		}
		cmd := rec.lastApplyUsage()
		if cmd.UsagePresent {
			t.Fatal("UsagePresent = true without a usage event, want false")
		}
		if cmd.Usage != state.admissionInput.PreflightUsage {
			t.Fatalf("ApplyUsage usage = %#v, want the preflight estimate %#v", cmd.Usage, state.admissionInput.PreflightUsage)
		}
	})
}

// Retired monetary advisory behavior has no implementation.
// TestApplyAdvisoryUsageStrictAndAdvisoryMixUpdatesBoth pins requirement 7.7 at
// the runtime boundary: when one request matched a strict rule and an advisory
// rule, the strict reservation settles through Settle while ApplyAdvisoryUsage
// applies usage to the advisory rule IDs only — never to the reserved strict
// rule IDs. The strict/advisory window mutation is Enterprise rule semantics
// (TestEnterpriseRuleStrictAndAdvisoryMixUpdatesBoth).
func TestApplyAdvisoryUsageStrictAndAdvisoryMixUpdatesBoth(t *testing.T) {
	t.Parallel()

	rec := &recordingAuthorityService{}
	state := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(80),
		admissionResult: usagelimit.AdmissionResult{
			Allowed:         true,
			Reserved:        true,
			ReservationID:   "reservation-strict",
			ReservedAmount:  authorityInputAmount(80),
			RuleIDs:         []string{"tenant.input.strict"},
			AdvisoryRuleIDs: []string{"tenant.output.advisory"},
		},
	}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 80, OutputTokens: 120, TotalTokens: 200}
	if applied := lifecycle.Settle(context.Background(), usagelimit.SettlementKindFinal, usageEv, false); !applied {
		t.Fatal("Settle = false, want true (strict reservation must be reconciled)")
	}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("ApplyAdvisoryUsage = false, want true (advisory window must update)")
	}

	if in := rec.lastSettle(); in.Kind != usagelimit.SettlementKindFinal {
		t.Fatalf("settle kind = %q, want %q", in.Kind, usagelimit.SettlementKindFinal)
	}
	cmd := rec.lastApplyUsage()
	if len(cmd.RuleIDs) != 1 || cmd.RuleIDs[0] != "tenant.output.advisory" {
		t.Fatalf("ApplyUsage RuleIDs = %#v, want only the advisory rule [tenant.output.advisory]", cmd.RuleIDs)
	}
	if cmd.Usage.OutputTokens != 120 {
		t.Fatalf("ApplyUsage output tokens = %d, want 120", cmd.Usage.OutputTokens)
	}
}

// TestApplyAdvisoryUsageIdempotentAcrossDuplicateCalls pins requirement 7.8 at
// the runtime boundary: replaying ApplyAdvisoryUsage for the same logical
// request + B-leg submits the same deterministic source key, and the port's
// Applied=false answer is surfaced as a no-op (no double count). The store-side
// idempotence is Enterprise rule semantics
// (TestEnterpriseRuleAdvisoryUsageIdempotentAcrossDuplicateCalls).
func TestApplyAdvisoryUsageIdempotentAcrossDuplicateCalls(t *testing.T) {
	t.Parallel()

	rec := &recordingAuthorityService{applyUsageAppliedScript: []bool{true, false}}
	lifecycle := newAuthorityLifecycle(rec, nil, advisoryAuthorityState("tenant.input.advisory.idem"), authorityCandidate())

	usageEv := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 50, TotalTokens: 50}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); !applied {
		t.Fatal("first ApplyAdvisoryUsage = false, want true")
	}
	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), usageEv); applied {
		t.Fatal("second ApplyAdvisoryUsage = true, want false (idempotent no-op)")
	}
	if got := rec.applyUsageCalls.Load(); got != 2 {
		t.Fatalf("ApplyUsage calls = %d, want 2", got)
	}
	inputs := rec.applyUsageInputs()
	if len(inputs) != 2 {
		t.Fatalf("recorded ApplyUsage inputs = %d, want 2", len(inputs))
	}
	if inputs[0].SourceKey != inputs[1].SourceKey {
		t.Fatalf("source keys diverge across replay: %q vs %q", inputs[0].SourceKey, inputs[1].SourceKey)
	}
	if inputs[0].SourceKey != "trace-1|bleg-1|advisory_usage" {
		t.Fatalf("source key = %q, want the deterministic lifecycle key", inputs[0].SourceKey)
	}
}

// TestApplyAdvisoryUsageNoOpWhenNoAdvisoryRules proves ApplyAdvisoryUsage is a
// no-op when the admission result has no advisory rules (strict-only or no
// rules matched): the port is never called, so strict-only requests pay no
// advisory-apply overhead.
func TestApplyAdvisoryUsageNoOpWhenNoAdvisoryRules(t *testing.T) {
	t.Parallel()

	rec := &recordingAuthorityService{}
	state := attemptAuthorityState{
		admissionInput: testAuthorityAdmissionInput(30),
		admissionResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-strict-noop",
			ReservedAmount: authorityInputAmount(30),
			RuleIDs:        []string{"tenant.input.strict.noop"},
		},
	}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())

	if applied := lifecycle.ApplyAdvisoryUsage(context.Background(), aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 30}); applied {
		t.Fatal("ApplyAdvisoryUsage on strict-only request = true, want false (no advisory rules)")
	}
	if got := rec.applyUsageCalls.Load(); got != 0 {
		t.Fatalf("ApplyUsage calls = %d, want 0 (no advisory rules)", got)
	}
}
