package proxymemory

import (
	"errors"
	"strings"
	"testing"
)

// decodeModelPolicyConfig decodes a model-policy configuration on top of the
// required keys and requires it to decode.
func decodeModelPolicyConfig(t *testing.T, raw string) Config {
	t.Helper()
	cfg, err := DecodeConfig(mapToNode(t, configRequired+raw))
	if err != nil {
		t.Fatalf("DecodeConfig() error: %v", err)
	}
	return cfg
}

// lockOneField returns a copy of cfg whose named phase locks exactly the
// named field. The lock set is per phase and per field, so the matrix over
// both phases and all three fields needs no second fixture.
func lockOneField(cfg Config, phase Phase, field string) Config {
	policy := &cfg.Models.Extraction
	if phase == PhaseConsolidation {
		policy = &cfg.Models.Consolidation
	}
	policy.Locks = TargetLocks{}
	switch field {
	case FieldBackend:
		policy.Locks.Backend = true
	case FieldModel:
		policy.Locks.Model = true
	case FieldReasoningEffort:
		policy.Locks.ReasoningEffort = true
	}
	return cfg
}

// TestModelPolicyLockedWriteIsRefused pins Requirement 1.2 over both phases
// and all three fields: a write that changes a locked field is refused
// explicitly, naming the phase and the field and never the value, and the
// refusal is atomic, so no partial state is returned.
func TestModelPolicyLockedWriteIsRefused(t *testing.T) {
	t.Parallel()

	base := decodeModelPolicyConfig(t, configModelsLocks)
	cases := []struct {
		name     string
		phase    Phase
		field    string
		proposed TargetOverrides
	}{
		{name: "the extraction backend", phase: PhaseExtraction, field: FieldBackend, proposed: TargetOverrides{Backend: "forbidden-backend"}},
		{name: "the extraction model", phase: PhaseExtraction, field: FieldModel, proposed: TargetOverrides{Model: "forbidden-model"}},
		{name: "the extraction reasoning effort", phase: PhaseExtraction, field: FieldReasoningEffort, proposed: TargetOverrides{ReasoningEffort: "forbidden-effort"}},
		{name: "the consolidation backend", phase: PhaseConsolidation, field: FieldBackend, proposed: TargetOverrides{Backend: "forbidden-backend"}},
		{name: "the consolidation model", phase: PhaseConsolidation, field: FieldModel, proposed: TargetOverrides{Model: "forbidden-model"}},
		{name: "the consolidation reasoning effort", phase: PhaseConsolidation, field: FieldReasoningEffort, proposed: TargetOverrides{ReasoningEffort: "forbidden-effort"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := lockOneField(base, tc.phase, tc.field)
			got, err := cfg.ApplyOwnerOverrides(tc.phase, tc.proposed)
			if err == nil {
				t.Fatal("ApplyOwnerOverrides() = nil error, want a refusal for the locked field")
			}
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) {
				t.Fatalf("ApplyOwnerOverrides() error %v is not *PolicyError", err)
			}
			if policyErr.Phase != tc.phase {
				t.Errorf("refusal phase = %q, want %q", policyErr.Phase, tc.phase)
			}
			if policyErr.Field != tc.field {
				t.Errorf("refusal field = %q, want %q", policyErr.Field, tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("refusal %q does not name the offending field", err)
			}
			if strings.Contains(err.Error(), "forbidden") {
				t.Errorf("refusal %q echoes the offending value", err)
			}
			if got != (PhaseSettings{}) {
				t.Errorf("ApplyOwnerOverrides() = %+v, want the zero result on a refusal", got)
			}
		})
	}
}

// TestModelPolicyLockedWriteOfTheSameValueIsIdempotent pins the one write a
// lock accepts: a write that repeats the locked operator value changes
// nothing and is idempotent, and the locked field still carries no owner
// value in the returned state.
func TestModelPolicyLockedWriteOfTheSameValueIsIdempotent(t *testing.T) {
	t.Parallel()

	base := decodeModelPolicyConfig(t, configModelsLocks)
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		for _, field := range []string{FieldBackend, FieldModel, FieldReasoningEffort} {
			t.Run(string(phase)+"/"+field, func(t *testing.T) {
				t.Parallel()
				cfg := lockOneField(base, phase, field)
				operator, err := cfg.EffectiveTarget(phase)
				if err != nil {
					t.Fatalf("EffectiveTarget() error: %v", err)
				}
				identical := TargetOverrides{}
				switch field {
				case FieldBackend:
					identical.Backend = operator.Backend
				case FieldModel:
					identical.Model = operator.Model
				case FieldReasoningEffort:
					identical.ReasoningEffort = operator.ReasoningEffort
				}
				settings, err := cfg.ApplyOwnerOverrides(phase, identical)
				if err != nil {
					t.Fatalf("ApplyOwnerOverrides() error: %v", err)
				}
				if settings.Effective != operator {
					t.Errorf("effective target = %+v, want %+v", settings.Effective, operator)
				}
				if want := (TargetOverrides{}); settings.Overrides != want {
					t.Errorf("returned overrides = %+v, want %+v: the locked field carries no owner value", settings.Overrides, want)
				}
			})
		}
	}
}

// TestModelPolicyLockIntroducedAfterAnOverrideMasksTheOwnerValue pins the
// pure projection of Requirement 1.2 and the exact boundary of what this
// subtree delivers.
//
// While the lock applies, the effective target is the locked operator value
// and the returned state omits the locked field. The projection is pure: it
// changes no stored row, and the caller's own value is unchanged after the
// call.
//
// The last step characterizes that boundary, and it is not the product
// contract: the projection is not a durable invalidation. An owner value that
// is still stored and is read again after the lock is removed becomes
// effective again, while the returned sanitized state keeps the operator's
// default. The durable handling of a stale override, and its reconciliation
// with the stored unprojected overrides, is not implemented yet and remains
// required for the persistence, read and reload acceptance.
func TestModelPolicyLockIntroducedAfterAnOverrideMasksTheOwnerValue(t *testing.T) {
	t.Parallel()

	// The owner stored an override before the operator locked the field.
	stored := TargetOverrides{Model: "gpt-5"}
	locked := decodeModelPolicyConfig(t, configModelsLocks)
	settings, err := locked.OwnerSettings(PhaseExtraction, stored)
	if err != nil {
		t.Fatalf("OwnerSettings() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"}); settings.Effective != want {
		t.Errorf("effective target = %+v, want %+v", settings.Effective, want)
	}
	if want := (TargetOverrides{}); settings.Overrides != want {
		t.Errorf("returned overrides = %+v, want %+v: the locked field is omitted", settings.Overrides, want)
	}
	if want := (TargetLocks{Model: true, ReasoningEffort: true}); settings.Locks != want {
		t.Errorf("lock flags = %+v, want %+v", settings.Locks, want)
	}
	if want := (TargetOverrides{Model: "gpt-5"}); stored != want {
		t.Errorf("the caller's own value = %+v, want %+v: the projection must not change it", stored, want)
	}

	// The lock is removed later.
	unlocked := locked
	unlocked.Models.Extraction.Locks = TargetLocks{}

	// Reusing the returned sanitized state keeps the operator's default.
	restored, err := unlocked.OwnerSettings(PhaseExtraction, settings.Overrides)
	if err != nil {
		t.Fatalf("OwnerSettings() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"}); restored.Effective != want {
		t.Errorf("effective target from the sanitized state = %+v, want %+v", restored.Effective, want)
	}

	// Reading the unchanged stored value again makes it effective: the
	// projection is not a durable invalidation, and the durable handling of a
	// stale override is not implemented yet.
	revived, err := unlocked.OwnerSettings(PhaseExtraction, stored)
	if err != nil {
		t.Fatalf("OwnerSettings() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"}); revived.Effective != want {
		t.Errorf("effective target from the unchanged stored value = %+v, want %+v", revived.Effective, want)
	}
}

// TestModelPolicyUnlockedFieldKeepsThePermittedOwnerValue pins the permitted
// path: a field the operator did not lock still takes the owner value the
// inventory permits, and the two phases are governed independently.
func TestModelPolicyUnlockedFieldKeepsThePermittedOwnerValue(t *testing.T) {
	t.Parallel()

	cfg := decodeModelPolicyConfig(t, configModelsLocks)
	settings, err := cfg.ApplyOwnerOverrides(PhaseExtraction, TargetOverrides{Backend: "anthropic"})
	if err != nil {
		t.Fatalf("ApplyOwnerOverrides() error: %v", err)
	}
	if want := (TargetOverrides{Backend: "anthropic"}); settings.Overrides != want {
		t.Errorf("returned overrides = %+v, want %+v", settings.Overrides, want)
	}
	if want := (PhaseTarget{Backend: "anthropic", Model: "gpt-5-mini", ReasoningEffort: "low"}); settings.Effective != want {
		t.Errorf("effective target = %+v, want %+v", settings.Effective, want)
	}

	// The consolidation phase locks nothing, so the same owner value is
	// permitted there.
	other, err := cfg.OwnerSettings(PhaseConsolidation, TargetOverrides{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("OwnerSettings() error: %v", err)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"}); other.Effective != want {
		t.Errorf("consolidation effective target = %+v, want %+v", other.Effective, want)
	}
	if want := (TargetOverrides{Model: "gpt-5"}); other.Overrides != want {
		t.Errorf("consolidation returned overrides = %+v, want %+v", other.Overrides, want)
	}
}

// TestModelPolicyWriteRefusesAnOutOfInventoryValue pins that the write path
// reuses the same authority as the runtime: a value outside the phase's
// allowed inventory is refused explicitly, naming the phase and the field and
// never the value (Requirement 1.4).
func TestModelPolicyWriteRefusesAnOutOfInventoryValue(t *testing.T) {
	t.Parallel()

	cfg := decodeModelPolicyConfig(t, configModelsLocks)
	_, err := cfg.ApplyOwnerOverrides(PhaseExtraction, TargetOverrides{Backend: "gemini"})
	if err == nil {
		t.Fatal("ApplyOwnerOverrides() = nil error, want a refusal for the out-of-inventory backend")
	}
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("ApplyOwnerOverrides() error %v is not *PolicyError", err)
	}
	if policyErr.Phase != PhaseExtraction {
		t.Errorf("refusal phase = %q, want %q", policyErr.Phase, PhaseExtraction)
	}
	if policyErr.Field != FieldBackend {
		t.Errorf("refusal field = %q, want %q", policyErr.Field, FieldBackend)
	}
	if strings.Contains(err.Error(), "gemini") {
		t.Errorf("refusal %q echoes the offending value", err)
	}
}

// TestModelPolicyDefaultRequestConstructionUsesOperatorDefaults pins the
// default request construction: the phase request carries the operator's
// declared target and effort.
//
// The request builder takes the operator's configuration only, and it has no
// owner input in this slice, so this is not a runtime-bypass certification.
// The owner-selected dispatch path, where an owner's value reaches the
// request, belongs to task 3.2; the checks against the final compiled actual
// target belong to task 2.2.
func TestModelPolicyDefaultRequestConstructionUsesOperatorDefaults(t *testing.T) {
	t.Parallel()

	cfg := decodeModelPolicyConfig(t, configModelsLocks)
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
