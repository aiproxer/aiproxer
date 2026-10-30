package openaichatgptplan_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNewModuleHasNoLegacyRecoverySemantics is the static architecture proof of
// requirements 2.13, 8.3 and 8.6 and task 6.2.
//
// The new connector serves ONE explicitly selected ChatGPT-plan registration.
// It must not inherit the retired Codex pool recovery machinery: no plan-type
// hint that rewrites the model, no free-plan GPT downgrade, no `x-codex-*`
// recovery heuristics, no multi-account rotation, and no quota-header-driven
// model switch. Those mechanisms are why task 6.2 exists; reintroducing any of
// them would let a usage-exhausted answer silently continue on another model or
// another subscriber.
//
// The check scans the module's PRODUCTION Go files. Test files are excluded on
// purpose: the execution tests name the retired mechanisms in order to prove
// their absence, and a retirement proof is not a production dependency.
//
// The markers are deliberately narrow. Words such as "fallback", "rotate" and
// "quota" are NOT markers: this connector legitimately uses "fallback" for an
// operator-opened URL, "rotation" for token rotation inside one registration,
// and "quota" in documentation. Each marker below names one retired mechanism's
// own identifier or wire literal, so a legitimate mention cannot match it:
//
//   - the plan-hint markers are the legacy configuration field and its
//     serialized/local spellings - the switch that chose a downgraded model;
//   - the downgrade markers are the retired free-plan downgrade policy
//     identifiers, including its plan lookup and its retry predicate;
//   - the x-codex markers are the private header family the legacy connector
//     parsed for plan/quota/recovery hints (both the lowercase wire form and
//     the Go-canonical form);
//   - the pool markers are the legacy multi-account store, account record,
//     rotation loop and cross-account retry budget;
//   - the quota markers are the legacy quota-header persistence and window
//     parsing names - quota data must never choose a model or a registration;
//   - the substitution markers are explicit fallback/substitute model
//     identifiers; a silent model swap must not have a name to live under.
func TestNewModuleHasNoLegacyRecoverySemantics(t *testing.T) {
	t.Parallel()
	files, err := productionGoFiles(".")
	if err != nil {
		t.Fatalf("scan the production files: %v", err)
	}
	// A walk that stops finding files would pass every absence check
	// vacuously. The count and the known files make the proof non-vacuous: if
	// the scanner loses the source tree, the test fails for that reason.
	if len(files) < 20 {
		t.Fatalf("the scan found %d production Go files, want the connector's source tree", len(files))
	}
	for _, required := range []string{
		filepath.Join("internal", "service", "execute.go"),
		filepath.Join("internal", "responses", "classify.go"),
	} {
		if !containsPath(files, required) {
			t.Fatalf("the scan did not find the production file %q", required)
		}
	}

	markers := forbiddenRecoveryMarkers()
	// A proof with an empty or tiny marker list would pass while proving
	// nothing. The floor is the number of distinct retired mechanisms named
	// in the doc comment above (plan hint, downgrade, x-codex, pool, quota,
	// substitution).
	if len(markers) < 12 {
		t.Fatalf("the marker list holds %d entries, want the retired recovery mechanisms", len(markers))
	}
	for _, marker := range markers {
		if strings.TrimSpace(marker.text) == "" || strings.TrimSpace(marker.reason) == "" {
			t.Fatalf("a marker has no text or no reason: %+v", marker)
		}
	}

	// The overlay exists because `go test -overlay` changes what the COMPILER
	// sees, not the files on disk, and this proof reads the production source.
	// A discrimination run sets the same overlay for the test through
	// SIWC_STATIC_CHECK_OVERLAY, so the static check examines the source the
	// build examined.
	overlay := testOverlay(t)
	for _, file := range files {
		text := string(productionSource(t, file, overlay))
		for _, marker := range markers {
			if strings.Contains(text, marker.text) {
				t.Errorf(
					"the production file %q contains %q; the new connector must not carry %s",
					file, marker.text, marker.reason,
				)
			}
		}
	}
}

// recoveryMarker is one forbidden legacy dependency and the mechanism it names.
type recoveryMarker struct {
	// text is the exact substring searched for in production files.
	text string
	// reason names the retired mechanism the marker stands for, so a failure
	// says why the dependency is forbidden and not only that it exists.
	reason string
}

// forbiddenRecoveryMarkers returns the retired recovery mechanisms.
func forbiddenRecoveryMarkers() []recoveryMarker {
	return []recoveryMarker{
		// Plan-type hint: it rewrote the requested model from a plan label.
		{text: "PlanTypeHint", reason: "the legacy plan-type hint that selected a downgraded model"},
		{text: "planTypeHint", reason: "the legacy plan-type hint that selected a downgraded model"},
		{text: "plan_type_hint", reason: "the serialized legacy plan-type hint"},

		// Free-plan GPT downgrade.
		{text: "GPT55Downgrade", reason: "the legacy free-plan GPT downgrade configuration"},
		{text: "downgradePolicy", reason: "the legacy free-plan downgrade policy"},
		{text: "modelForPlan", reason: "the legacy plan-to-model mapping"},
		{text: "isFreePlanType", reason: "the legacy free-plan plan-type test"},
		{text: "maybeRetryGPT55Downgrade", reason: "the legacy reactive downgrade retry"},

		// Private x-codex recovery/plan/quota header heuristics.
		{text: "x-codex-", reason: "the private x-codex header family and its recovery heuristics"},
		{text: "X-Codex-", reason: "the private x-codex header family and its recovery heuristics"},

		// Multi-account pooling and rotation.
		{text: "accountStore", reason: "the legacy multi-account store that pooled registrations"},
		{text: "managedAccount", reason: "the legacy pooled account record"},
		{text: "openManagedAccountLoop", reason: "the legacy cross-account rotation loop"},
		{text: "maxManagedRetries", reason: "the legacy cross-account retry budget"},
		{text: "accountPool", reason: "a pooled multi-account authority"},

		// Quota-header-driven switching.
		{text: "persistQuotaHeaders", reason: "the legacy quota-header persistence"},
		{text: "codexQuotaHeaders", reason: "the legacy quota-header extraction"},
		{text: "quotaHeaders", reason: "legacy quota-header state"},
		{text: "codexWindowHeaderPattern", reason: "the legacy quota-window header parser"},

		// Explicit model substitution.
		{text: "fallbackModel", reason: "an explicit fallback model id"},
		{text: "FallbackModel", reason: "an explicit fallback model id"},
		{text: "substituteModel", reason: "an explicit model substitution"},
		{text: "SubstituteModel", reason: "an explicit model substitution"},
	}
}
