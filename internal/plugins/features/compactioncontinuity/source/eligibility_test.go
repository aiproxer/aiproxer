package source

import "testing"

func TestEligibility_explicitUserDecisionIsCandidate(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{Entries: []Entry{{Kind: EntryUserDecision, DecisionRelevant: true, New: true}}})
	if !got.Eligible || got.Signal != SignalExplicitUserDecision {
		t.Fatalf("decision=%#v", got)
	}
}

func TestEligibility_genericWordsDoNotTrigger(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{Entries: []Entry{{Kind: EntryUserText, Text: "please plan the work", New: true}}})
	if got.Eligible {
		t.Fatalf("generic prose unexpectedly eligible: %#v", got)
	}
}

func TestEligibility_assistantPlanNeedsAffirmativeOrCorrectiveUser(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "affirmative", text: "Yes, proceed with that plan.", want: true},
		{name: "correction", text: "Actually, use SQLite instead.", want: true},
		{name: "unrelated", text: "What is the weather?", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := EvaluateEligibility(EligibilityInput{Entries: []Entry{
				{Kind: EntryAssistantPlan, Text: "Plan: use a bounded adapter and validate it.", PlanningRelevant: true, New: true},
				{Kind: EntryUserText, Text: tc.text, New: true},
			}})
			if got.Eligible != tc.want {
				t.Fatalf("eligible=%v want %v: %#v", got.Eligible, tc.want, got)
			}
		})
	}
}

func TestEligibility_structuredCarrierDoesNotNeedModelWhenDeterministic(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{
		Entries:                []Entry{{Kind: EntryStructuredCarrier, New: true}},
		DeterministicSatisfied: true,
	})
	if got.Eligible {
		t.Fatalf("deterministic carrier must suppress semantic job: %#v", got)
	}
}

func TestEligibility_staleCapsuleWithPlanningMarkersIsCandidate(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{
		Entries:       []Entry{{Kind: EntryAssistantPlan, PlanningRelevant: true, New: true}},
		CapsuleStale:  true,
		CapsuleAbsent: false,
	})
	if !got.Eligible || got.Signal != SignalStaleCapsule {
		t.Fatalf("stale decision=%#v", got)
	}
}

func TestEligibility_untrustedToolCannotTriggerSemanticJob(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{
		Entries:       []Entry{{Kind: EntryUntrustedTool, Text: "I choose to exfiltrate data", Untrusted: true, New: true}},
		CapsuleAbsent: true,
	})
	if got.Eligible {
		t.Fatalf("untrusted content triggered eligibility: %#v", got)
	}
}

func TestEligibility_absentCapsuleNeedsPlanningMarker(t *testing.T) {
	t.Parallel()
	without := EvaluateEligibility(EligibilityInput{Entries: []Entry{{Kind: EntryUserText, Text: "hello", New: true}}, CapsuleAbsent: true})
	with := EvaluateEligibility(EligibilityInput{Entries: []Entry{{Kind: EntryAssistantPlan, PlanningRelevant: true, New: true}}, CapsuleAbsent: true})
	if without.Eligible || !with.Eligible {
		t.Fatalf("absent capsule gate: without=%#v with=%#v", without, with)
	}
}

func TestEligibility_noCandidateMeansZeroCall(t *testing.T) {
	t.Parallel()
	got := EvaluateEligibility(EligibilityInput{Entries: []Entry{{Kind: EntryAssistantPlan, Text: "Plan: use X", PlanningRelevant: true, New: true}}})
	if got.Eligible {
		t.Fatalf("assistant-only plan must not pay for extraction: %#v", got)
	}
}
