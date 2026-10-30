package proxymemory

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// modelPolicyTestPolicy is the operator policy the resolution table uses: one
// default target and one allowed inventory per field, with the allowed
// reasoning efforts declared per target.
func modelPolicyTestPolicy() PhasePolicy {
	return PhasePolicy{
		Defaults: PhaseTarget{
			Backend:         "openai",
			Model:           "gpt-5-mini",
			ReasoningEffort: "low",
		},
		Inventory: PhaseInventory{
			Backends: []string{"openai", "anthropic"},
			Models:   []string{"gpt-5-mini", "gpt-5"},
			Targets: []TargetPolicy{
				{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
				{Backend: "anthropic", Model: "gpt-5", ReasoningEfforts: []string{"high"}},
			},
		},
	}
}

// modelPolicyTestInherited is the target the operator's default route names:
// a field that neither the operator nor the owner named keeps it.
var modelPolicyTestInherited = PhaseTarget{Backend: "openai", Model: "gpt-5-mini"}

// TestResolveEffectiveTargetPrecedence pins the field precedence of design §3:
// the locked operator value, else the permitted owner value, else the
// operator default. A partial override changes only the fields it names, and a
// lock introduced after an override existed wins: the effective value falls
// back to the operator's value and the stale override does not survive.
func TestResolveEffectiveTargetPrecedence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		policy   PhasePolicy
		locks    TargetLocks
		override TargetOverrides
		want     PhaseTarget
	}{
		{
			name:   "no override keeps every operator default",
			policy: modelPolicyTestPolicy(),
			want:   PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"},
		},
		{
			name:     "a partial override changes only the fields it names",
			policy:   modelPolicyTestPolicy(),
			override: TargetOverrides{Model: "gpt-5"},
			want:     PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"},
		},
		{
			name:     "a permitted owner value wins over the operator default",
			policy:   modelPolicyTestPolicy(),
			override: TargetOverrides{Backend: "anthropic", Model: "gpt-5", ReasoningEffort: "high"},
			want:     PhaseTarget{Backend: "anthropic", Model: "gpt-5", ReasoningEffort: "high"},
		},
		{
			name:     "a locked field falls back to the operator value",
			policy:   modelPolicyTestPolicy(),
			locks:    TargetLocks{Model: true},
			override: TargetOverrides{Model: "gpt-5"},
			want:     PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"},
		},
		{
			name:     "a lock on one field leaves the other fields alone",
			policy:   modelPolicyTestPolicy(),
			locks:    TargetLocks{ReasoningEffort: true},
			override: TargetOverrides{Model: "gpt-5", ReasoningEffort: "high"},
			want:     PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"},
		},
		{
			name:   "an operator that named no default effort keeps today's value",
			policy: PhasePolicy{Defaults: PhaseTarget{Backend: "openai"}},
			want:   PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: defaultPhaseEffort(PhaseExtraction)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveEffectiveTarget(PhaseExtraction, tc.policy, modelPolicyTestInherited, tc.locks, tc.override)
			if err != nil {
				t.Fatalf("ResolveEffectiveTarget() error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveEffectiveTarget() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestResolveEffectiveTargetAllowedEffortsFollowTheEffectiveTarget pins the
// per-target effort policy of design §3: the effort values are
// provider-specific, so the allowed efforts are declared per phase and per
// target. The same effort value is allowed for one target and refused for
// another, and the policy that applies is the *effective* target's.
func TestResolveEffectiveTargetAllowedEffortsFollowTheEffectiveTarget(t *testing.T) {
	t.Parallel()

	policy := modelPolicyTestPolicy()
	got, err := ResolveEffectiveTarget(PhaseExtraction, policy, modelPolicyTestInherited, TargetLocks{}, TargetOverrides{
		Backend:         "anthropic",
		Model:           "gpt-5",
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("ResolveEffectiveTarget() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "anthropic", Model: "gpt-5", ReasoningEffort: "high"}); got != want {
		t.Errorf("ResolveEffectiveTarget() = %+v, want %+v", got, want)
	}

	// The same effort value is not allowed for the other target of the phase.
	_, err = ResolveEffectiveTarget(PhaseExtraction, policy, modelPolicyTestInherited, TargetLocks{}, TargetOverrides{
		Backend:         "openai",
		Model:           "gpt-5",
		ReasoningEffort: "high",
	})
	if err == nil {
		t.Fatal("ResolveEffectiveTarget() = nil error, want a refusal for the other target")
	}
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("ResolveEffectiveTarget() error %v is not *PolicyError", err)
	}
	if policyErr.Field != FieldReasoningEffort {
		t.Errorf("refusal field = %q, want %q", policyErr.Field, FieldReasoningEffort)
	}
}

// TestModelPolicyAllowedEffortsAreDeclaredPerTarget pins the configuration
// shape of the per-target effort policy: one phase declares two targets with
// different allowed efforts.
func TestModelPolicyAllowedEffortsAreDeclaredPerTarget(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(mapToNode(t, configRequired+configModelsPolicy))
	if err != nil {
		t.Fatalf("DecodeConfig() error: %v", err)
	}
	policy, ok := cfg.Models.For(PhaseExtraction)
	if !ok {
		t.Fatal("the extraction policy is missing")
	}
	if got, want := policy.Inventory.AllowedEfforts("openai", "gpt-5-mini"), []string{"low", "medium"}; !slices.Equal(got, want) {
		t.Errorf("allowed efforts for openai:gpt-5-mini = %v, want %v", got, want)
	}
	if got, want := policy.Inventory.AllowedEfforts("anthropic", "gpt-5"), []string{"high"}; !slices.Equal(got, want) {
		t.Errorf("allowed efforts for anthropic:gpt-5 = %v, want %v", got, want)
	}
	if got := policy.Inventory.AllowedEfforts("openai", "gpt-5"); got != nil {
		t.Errorf("allowed efforts for an undeclared target = %v, want none", got)
	}
}

// TestModelPolicyLegacyConfigKeepsTheOperatorRoute pins the legacy
// compatibility: a configuration without the models subtree keeps the
// operator's route, the route's target and today's effort, and locks no
// field.
func TestModelPolicyLegacyConfigKeepsTheOperatorRoute(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(mapToNode(t, configRequired))
	if err != nil {
		t.Fatalf("DecodeConfig() error: %v", err)
	}
	policy, ok := cfg.Models.For(PhaseExtraction)
	if !ok {
		t.Fatal("the extraction policy is missing")
	}
	if want := (TargetLocks{}); policy.Locks != want {
		t.Errorf("extraction locks = %+v, want %+v: a legacy configuration locks no field", policy.Locks, want)
	}
	target, err := cfg.EffectiveTarget(PhaseExtraction)
	if err != nil {
		t.Fatalf("EffectiveTarget() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"}); target != want {
		t.Errorf("EffectiveTarget() = %+v, want %+v", target, want)
	}
	req, err := BuildPhase1Request(cfg, Phase1Input{
		SessionRef:   "session-0123456789ab",
		ProjectLabel: "/w/project-sentinel",
		Transcript:   "[]",
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if got, want := req.Call.Route.Selector, "openai:gpt-5-mini"; got != want {
		t.Errorf("Route.Selector = %q, want %q", got, want)
	}
	if got, want := req.Call.Options.ReasoningEffort, "low"; got != want {
		t.Errorf("ReasoningEffort = %q, want %q", got, want)
	}
}

// TestModelPolicyDeclaredPolicyRefusesAMultiTargetRoute pins the refusal
// scope of design §2: the guard is per attempt, so a declared policy needs a
// single-target phase route even when it declares no target, while a
// configuration without the policy keeps the operator's failover route.
func TestModelPolicyDeclaredPolicyRefusesAMultiTargetRoute(t *testing.T) {
	t.Parallel()

	const routes = "principals: [alice]\nextract_route: \"openai:gpt-5-mini|anthropic:claude\"\nconsolidate_route: openai:gpt-5\n"
	const declared = routes +
		"models:\n  extraction:\n    defaults:\n      reasoning_effort: high\n    locks: {}\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n"
	_, err := DecodeConfig(mapToNode(t, declared))
	if err == nil || !strings.Contains(err.Error(), "extract_route: must name a single target") {
		t.Fatalf("DecodeConfig() error = %v, want the extract_route single-target refusal", err)
	}

	cfg, err := DecodeConfig(mapToNode(t, routes))
	if err != nil {
		t.Fatalf("DecodeConfig() without a policy: %v", err)
	}
	req, err := BuildPhase1Request(cfg, Phase1Input{
		SessionRef:   "session-0123456789ab",
		ProjectLabel: "/w/project-sentinel",
		Transcript:   "[]",
	})
	if err != nil {
		t.Fatalf("BuildPhase1Request: %v", err)
	}
	if got, want := req.Call.Route.Selector, "openai:gpt-5-mini|anthropic:claude"; got != want {
		t.Errorf("Route.Selector = %q, want %q", got, want)
	}
}

// TestResolveEffectiveTargetRefusesAnOutOfInventoryValue pins Requirement 1.4
// at the policy level: a value outside the phase's allowed inventory is
// refused explicitly, naming the phase and the field, and the refusal never
// echoes the offending value.
func TestResolveEffectiveTargetRefusesAnOutOfInventoryValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		phase     Phase
		override  TargetOverrides
		wantField string
	}{
		{
			name:      "a backend outside the inventory is refused",
			phase:     PhaseExtraction,
			override:  TargetOverrides{Backend: "gemini"},
			wantField: FieldBackend,
		},
		{
			name:      "a model outside the inventory is refused",
			phase:     PhaseConsolidation,
			override:  TargetOverrides{Model: "gpt-4"},
			wantField: FieldModel,
		},
		{
			name:      "a reasoning effort outside the inventory is refused",
			phase:     PhaseExtraction,
			override:  TargetOverrides{ReasoningEffort: "xhigh"},
			wantField: FieldReasoningEffort,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveEffectiveTarget(tc.phase, modelPolicyTestPolicy(), modelPolicyTestInherited, TargetLocks{}, tc.override)
			if err == nil {
				t.Fatal("ResolveEffectiveTarget() = nil error, want a refusal")
			}
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) {
				t.Fatalf("ResolveEffectiveTarget() error %v is not *PolicyError", err)
			}
			if policyErr.Phase != tc.phase {
				t.Errorf("refusal phase = %q, want %q", policyErr.Phase, tc.phase)
			}
			if policyErr.Field != tc.wantField {
				t.Errorf("refusal field = %q, want %q", policyErr.Field, tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("refusal %q does not name the offending field", err)
			}
			if strings.Contains(err.Error(), "gemini") || strings.Contains(err.Error(), "gpt-4") || strings.Contains(err.Error(), "xhigh") {
				t.Errorf("refusal %q echoes the offending value", err)
			}
		})
	}
}

// TestResolveEffectiveTargetEmptyInventoryPermitsNoOverride pins the empty
// inventory rule of design §3: an empty inventory means "no user-selectable
// override", not "everything allowed".
func TestResolveEffectiveTargetEmptyInventoryPermitsNoOverride(t *testing.T) {
	t.Parallel()

	policy := PhasePolicy{Defaults: PhaseTarget{Backend: "openai", Model: "gpt-5-mini"}}
	got, err := ResolveEffectiveTarget(PhaseExtraction, policy, modelPolicyTestInherited, TargetLocks{}, TargetOverrides{})
	if err != nil {
		t.Fatalf("ResolveEffectiveTarget() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: defaultPhaseEffort(PhaseExtraction)}); got != want {
		t.Errorf("ResolveEffectiveTarget() = %+v, want %+v", got, want)
	}
	if _, err := ResolveEffectiveTarget(PhaseExtraction, policy, modelPolicyTestInherited, TargetLocks{}, TargetOverrides{Backend: "openai"}); err == nil {
		t.Fatal("ResolveEffectiveTarget() = nil error, want a refusal for an empty inventory")
	}
}

// TestDeriveRouteSelector pins the derived selector of design §3: the
// operator's default route is used verbatim while the effective target names
// neither a backend nor a model, the effective backend and model replace the
// ones the default route names, and an absent one keeps the operator's
// default.
func TestDeriveRouteSelector(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		route   string
		backend string
		model   string
		want    string
	}{
		{
			name:  "no effective target keeps the operator route",
			route: "openai:gpt-5-mini",
			want:  "openai:gpt-5-mini",
		},
		{
			name:    "a declared backend and model replace the route target",
			route:   "openai:gpt-5-mini",
			backend: "anthropic",
			model:   "claude-sonnet-4",
			want:    "anthropic:claude-sonnet-4",
		},
		{
			name:  "an absent backend keeps the operator default backend",
			route: "openai:gpt-5-mini",
			model: "gpt-5",
			want:  "openai:gpt-5",
		},
		{
			name:    "an absent model keeps the operator default model",
			route:   "openai:gpt-5-mini",
			backend: "anthropic",
			want:    "anthropic:gpt-5-mini",
		},
		{
			name:  "a model-only operator route stays model-only",
			route: "gpt-5-mini",
			model: "gpt-5",
			want:  "gpt-5",
		},
		{
			name:  "the operator annotation and query survive the derivation",
			route: "[ttft_timeout=5s]openai:gpt-5-mini?reasoning_effort=medium",
			model: "gpt-5",
			want:  "[ttft_timeout=5s]openai:gpt-5?reasoning_effort=medium",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := DeriveRouteSelector(PhaseExtraction, tc.route, tc.backend, tc.model)
			if err != nil {
				t.Fatalf("DeriveRouteSelector() error: %v", err)
			}
			if got != tc.want {
				t.Errorf("DeriveRouteSelector() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDeriveRouteSelectorRefusesAnUnusableOperatorRoute pins the refusals of
// the derivation: a default route that names more than one target has no
// single operator default to keep, and a model name with a ':' needs an
// explicit backend.
func TestDeriveRouteSelectorRefusesAnUnusableOperatorRoute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		route   string
		backend string
		model   string
	}{
		{name: "a failover route has no single default", route: "openai:gpt-5|anthropic:claude", model: "gpt-5"},
		{name: "a weighted route has no single default", route: "openai:gpt-5^anthropic:claude", model: "gpt-5"},
		{name: "a parallel route has no single default", route: "openai:gpt-5!anthropic:claude", model: "gpt-5"},
		{name: "a model-only route cannot carry a tagged model", route: "gpt-5-mini", model: "qwen2.5-coder:7b"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DeriveRouteSelector(PhaseExtraction, tc.route, tc.backend, tc.model); err == nil {
				t.Fatal("DeriveRouteSelector() = nil error, want a refusal")
			}
		})
	}
}

// TestPhaseTargetTokensAreBounded pins the lexical bounds of the three
// fields: a backend is an identifier, a model is a provider model name and a
// reasoning effort is a provider effort token. The effort is checked
// lexically only: the feature neither invents a closed list of provider effort
// values nor claims that a token is provider-verified (design §2).
func TestPhaseTargetTokensAreBounded(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		kind   string
		value  string
		wantOK bool
	}{
		{name: "a backend identifier", kind: "backend", value: "openai", wantOK: true},
		{name: "a backend with a dash", kind: "backend", value: "claude-agent-acp", wantOK: true},
		{name: "a backend with a colon", kind: "backend", value: "openai:x", wantOK: false},
		{name: "an empty backend", kind: "backend", value: "", wantOK: false},
		{name: "a namespaced model", kind: "model", value: "openai/gpt-oss-120b", wantOK: true},
		{name: "a tagged model", kind: "model", value: "qwen2.5-coder:7b", wantOK: true},
		{name: "a model with a space", kind: "model", value: "gpt 5", wantOK: false},
		{name: "an effort token", kind: "effort", value: "xhigh", wantOK: true},
		{name: "an effort with a space", kind: "effort", value: "very high", wantOK: false},
		{name: "an effort with a colon", kind: "effort", value: "high:1", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var ok bool
			switch tc.kind {
			case "backend":
				ok = validBackend(tc.value)
			case "model":
				ok = validModel(tc.value)
			case "effort":
				ok = validEffort(tc.value)
			}
			if ok != tc.wantOK {
				t.Errorf("%s %q accepted = %v, want %v", tc.kind, tc.value, ok, tc.wantOK)
			}
		})
	}
}
