package proxymemory

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The pre-open invariant of design §2: the phase's policy is re-asserted at
// the provider-open chokepoint, after every mutating transform and before any
// provider I/O.

// guardPolicy is the operator's policy of a phase whose allowed inventory
// covers the "openai:gpt-5" target with the "low" and "medium" efforts.
func guardPolicy() PhasePolicy {
	return PhasePolicy{
		Defaults: PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"},
		Inventory: PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"gpt-5"},
			Targets: []TargetPolicy{
				{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium"}},
			},
		},
	}
}

// guardInput is the content-free input of one pre-open check: the target, the
// frozen expectation and the role label.
func guardInput() AttemptGuardInput {
	return AttemptGuardInput{
		Role:            phase1Role,
		CandidateKey:    "openai:gpt-5",
		Backend:         "openai",
		Model:           "gpt-5",
		BaselineRoute:   "openai:gpt-5",
		BaselineOptions: aipapi.GenerationOptions{ReasoningEffort: "low"},
		ExpectedOptions: aipapi.GenerationOptions{ReasoningEffort: "low"},
		AttemptOptions:  aipapi.GenerationOptions{ReasoningEffort: "low"},
		Installation:    InstallationPresent,
		EffortSupport:   EffortSupportUnknown,
	}
}

// TestAttemptGuardIsInertForOrdinaryTraffic pins the inertness of design §2:
// a request that names no phase of this feature is never judged by the guard,
// so ordinary traffic is untouched.
func TestAttemptGuardIsInertForOrdinaryTraffic(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	in := guardInput()
	in.Role = "ordinary_client_request"
	in.AttemptOptions.ReasoningEffort = "mutated"
	if err := guard.CheckAttempt(context.Background(), in); err != nil {
		t.Fatalf("CheckAttempt: %v", err)
	}
}

// TestAttemptGuardIsInertWithoutAModelPolicy pins the legacy behaviour: a
// configuration that declares no model policy keeps the phase unguarded.
func TestAttemptGuardIsInertWithoutAModelPolicy(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	guard := NewAttemptGuard(cfg, nil, nil)
	in := guardInput()
	in.AttemptOptions.ReasoningEffort = "mutated"
	if err := guard.CheckAttempt(context.Background(), in); err != nil {
		t.Fatalf("CheckAttempt: %v", err)
	}
}

// TestAttemptGuardRefusesADivergedEffort pins the invariant of design §2: the
// attempt must still carry the frozen reasoning effort when it opens, so a
// request transform that rewrote it and the runtime's own negotiated
// downgrade that stripped it are both refused instead of being served.
func TestAttemptGuardRefusesADivergedEffort(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	for _, effort := range []string{"medium", ""} {
		in := guardInput()
		in.AttemptOptions.ReasoningEffort = effort
		refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
		if refusal.Field != FieldReasoningEffort {
			t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
		}
		if refusal.Phase != PhaseExtraction {
			t.Fatalf("refused phase = %q, want %q", refusal.Phase, PhaseExtraction)
		}
	}
}

// TestAttemptGuardAcceptsTheFrozenEffort pins the positive control of the
// same invariant: an attempt that still carries the frozen effort is served.
func TestAttemptGuardAcceptsTheFrozenEffort(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	if err := guard.CheckAttempt(context.Background(), guardInput()); err != nil {
		t.Fatalf("CheckAttempt: %v", err)
	}
}

// TestAttemptGuardRefusesANotInstalledTarget pins the installation fact of
// design §3: a target the registry publication proves absent is refused
// before the provider open.
func TestAttemptGuardRefusesANotInstalledTarget(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	in := guardInput()
	in.Installation = InstallationAbsent
	refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestAttemptGuardServesAnUnprovenInstallation pins the honest unknown state:
// an installation the publication does not prove is served and counted, and
// the refusal is not silent.
func TestAttemptGuardServesAnUnprovenInstallation(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	metrics := NewPrometheusCollector()
	guard := NewAttemptGuard(cfg, metrics, nil)
	in := guardInput()
	in.Installation = InstallationUnknown
	if err := guard.CheckAttempt(context.Background(), in); err != nil {
		t.Fatalf("CheckAttempt: %v", err)
	}
	if got := captureCounter(t, metrics, EventModelPolicyUnverifiedInstallation); got != 1 {
		t.Fatalf("unverified installation counter = %v, want 1", got)
	}
}

// TestAttemptGuardStrictPolicyRefusesTheUnprovenStates pins the operator's
// strict switch: the unproven facts are refused instead of served.
func TestAttemptGuardStrictPolicyRefusesTheUnprovenStates(t *testing.T) {
	t.Parallel()

	policy := guardPolicy()
	policy.Inventory.Models = []string{"gpt-5"}
	cfg := modelPolicyTestConfig(policy, guardPolicy())
	cfg.Models.Strict = true
	guard := NewAttemptGuard(cfg, nil, nil)

	in := guardInput()
	in.Installation = InstallationUnknown
	refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}

	in = guardInput()
	in.EffortSupport = EffortSupportUnknown
	refusal = policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
}

// TestAttemptGuardRefusesAnUnsupportedEffort pins the provider's own answer:
// a value the provider states it does not carry is refused before the
// provider open, and an unknown answer is served and counted.
func TestAttemptGuardRefusesAnUnsupportedEffort(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	metrics := NewPrometheusCollector()
	guard := NewAttemptGuard(cfg, metrics, nil)

	in := guardInput()
	in.EffortSupport = EffortSupportUnsupported
	refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}

	in = guardInput()
	in.EffortSupport = EffortSupportUnknown
	if err := guard.CheckAttempt(context.Background(), in); err != nil {
		t.Fatalf("CheckAttempt: %v", err)
	}
	if got := captureCounter(t, metrics, EventModelPolicyUnverifiedEffort); got != 1 {
		t.Fatalf("unverified effort counter = %v, want 1", got)
	}
}

// TestAttemptGuardRefusesATargetOutsideTheInventory pins the inventory check
// at the provider open: the target that is about to open must be covered.
func TestAttemptGuardRefusesATargetOutsideTheInventory(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	in := guardInput()
	in.Model = "gpt-4"
	refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestAttemptGuardJudgesBothPhases pins the role mapping of design §2: the
// consolidation phase is judged by the same guard.
func TestAttemptGuardJudgesBothPhases(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard := NewAttemptGuard(cfg, nil, nil)
	in := guardInput()
	in.Role = phase2Role
	in.AttemptOptions.ReasoningEffort = "mutated"
	refusal := policyRefusal(t, guard.CheckAttempt(context.Background(), in))
	if refusal.Phase != PhaseConsolidation {
		t.Fatalf("refused phase = %q, want %q", refusal.Phase, PhaseConsolidation)
	}
}

// TestAttemptGuardGovernsRole pins the applicability selector: only declared
// phase roles are governed, and an undeclared policy governs nothing.
func TestAttemptGuardGovernsRole(t *testing.T) {
	t.Parallel()

	declared := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	undeclared := DefaultConfig()

	cases := []struct {
		name string
		cfg  Config
		role string
		want bool
	}{
		{"undeclared empty role", undeclared, "", false},
		{"undeclared unrelated role", undeclared, "ordinary_client_request", false},
		{"undeclared phase1 role", undeclared, phase1Role, false},
		{"undeclared phase2 role", undeclared, phase2Role, false},
		{"declared empty role", declared, "", false},
		{"declared unrelated role", declared, "ordinary_client_request", false},
		{"declared unrelated auxiliary role", declared, "compaction_continuity_extractor", false},
		{"declared phase1 role", declared, phase1Role, true},
		{"declared phase2 role", declared, phase2Role, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			guard, ok := NewAttemptGuard(tc.cfg, nil, nil).(interface{ GovernsRole(string) bool })
			if !ok {
				t.Fatal("the feature guard does not expose the role selector")
			}
			if got := guard.GovernsRole(tc.role); got != tc.want {
				t.Fatalf("GovernsRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

// negotiatedStripInput is the content-free input of one negotiated-downgrade
// check on a governed phase.
func negotiatedStripInput() NegotiatedDowngradeInput {
	return NegotiatedDowngradeInput{
		Role:                    phase1Role,
		CandidateKey:            "openai:gpt-5",
		Backend:                 "openai",
		Model:                   "gpt-5",
		Downgraded:              []aipapi.Capability{aipapi.CapabilityReasoning},
		ReasoningEffort:         "low",
		ExpectedReasoningEffort: "low",
	}
}

// TestAttemptGuardRefusesANegotiatedStrip pins the conversion of the runtime's
// negotiated effort strip at both downgrade sites: a proxy-memory call whose
// effort the negotiation would strip is refused explicitly and counted
// instead of being served as a downgrade.
func TestAttemptGuardRefusesANegotiatedStrip(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	metrics := NewPrometheusCollector()
	guard, ok := NewAttemptGuard(cfg, metrics, nil).(NegotiatedDowngradeGuard)
	if !ok {
		t.Fatal("the feature guard does not implement the negotiated-downgrade extension")
	}

	err := guard.CheckNegotiatedDowngrade(context.Background(), negotiatedStripInput())
	if err == nil {
		t.Fatal("CheckNegotiatedDowngrade: want a refusal, got nil")
	}
	if !errors.Is(err, ErrNegotiatedEffortStrip) {
		t.Fatalf("error = %v, want %v", err, ErrNegotiatedEffortStrip)
	}
	refusal := policyRefusal(t, err)
	if refusal.Phase != PhaseExtraction {
		t.Fatalf("refused phase = %q, want %q", refusal.Phase, PhaseExtraction)
	}
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
	if got := captureCounter(t, metrics, EventModelPolicyRefusedNegotiatedStrip); got != 1 {
		t.Fatalf("negotiated-strip refusals = %v, want 1", got)
	}
}

// TestAttemptGuardDowngradeGuardIsInertForOtherCallers pins the feature scope:
// a call that names no governed phase, a downgrade of another capability and a
// call with no requested effort are all left to the runtime's existing
// behaviour.
func TestAttemptGuardDowngradeGuardIsInertForOtherCallers(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	metrics := NewPrometheusCollector()
	guard, ok := NewAttemptGuard(cfg, metrics, nil).(NegotiatedDowngradeGuard)
	if !ok {
		t.Fatal("the feature guard does not implement the negotiated-downgrade extension")
	}

	cases := []struct {
		name string
		in   NegotiatedDowngradeInput
	}{
		{
			name: "ordinary traffic",
			in: func() NegotiatedDowngradeInput {
				in := negotiatedStripInput()
				in.Role = "ordinary_client_request"
				return in
			}(),
		},
		{
			name: "no auxiliary role",
			in: func() NegotiatedDowngradeInput {
				in := negotiatedStripInput()
				in.Role = ""
				return in
			}(),
		},
		{
			name: "another capability",
			in: func() NegotiatedDowngradeInput {
				in := negotiatedStripInput()
				in.Downgraded = []aipapi.Capability{aipapi.CapabilityParallelToolCalls}
				return in
			}(),
		},
		{
			name: "no requested effort",
			in: func() NegotiatedDowngradeInput {
				in := negotiatedStripInput()
				in.ReasoningEffort = ""
				return in
			}(),
		},
		{
			name: "the route parameters replace the effort",
			in: func() NegotiatedDowngradeInput {
				in := negotiatedStripInput()
				in.ExpectedReasoningEffort = "high"
				return in
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := guard.CheckNegotiatedDowngrade(context.Background(), tc.in); err != nil {
				t.Fatalf("CheckNegotiatedDowngrade: %v", err)
			}
		})
	}
	if got := captureCounter(t, metrics, EventModelPolicyRefusedNegotiatedStrip); got != 0 {
		t.Fatalf("negotiated-strip refusals = %v, want 0", got)
	}
}

// TestAttemptGuardDowngradeGuardJudgesTheConsolidationPhase pins the role
// mapping of the extension: the second phase is covered by the same refusal.
func TestAttemptGuardDowngradeGuardJudgesTheConsolidationPhase(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(guardPolicy(), guardPolicy())
	guard, ok := NewAttemptGuard(cfg, nil, nil).(NegotiatedDowngradeGuard)
	if !ok {
		t.Fatal("the feature guard does not implement the negotiated-downgrade extension")
	}
	in := negotiatedStripInput()
	in.Role = phase2Role
	refusal := policyRefusal(t, guard.CheckNegotiatedDowngrade(context.Background(), in))
	if refusal.Phase != PhaseConsolidation {
		t.Fatalf("refused phase = %q, want %q", refusal.Phase, PhaseConsolidation)
	}
}
