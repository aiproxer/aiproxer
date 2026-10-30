package billing_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// TestAdmissionRouteFactValidation locks the public route-fact contract: a
// route always names its backend instance, the kind is optional but bounded,
// and an invalid fact is refused with the typed admission error.
func TestAdmissionRouteFactValidation(t *testing.T) {
	t.Parallel()
	valid := billing.AdmissionRouteFact{BackendInstanceID: "backend-a", BackendKind: "custom-openai-legacy-compatible"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixture route fact: %v", err)
	}
	noInstance := valid
	noInstance.BackendInstanceID = ""
	if err := noInstance.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("missing backend instance: err=%v, want ErrInvalidAdmission", err)
	}
	untrimmed := valid
	untrimmed.BackendInstanceID = " backend-a"
	if err := untrimmed.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("untrimmed backend instance: err=%v, want ErrInvalidAdmission", err)
	}
	oversizedKind := valid
	oversizedKind.BackendKind = strings.Repeat("k", billing.MaxIdentityBytes+1)
	if err := oversizedKind.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("oversized backend kind: err=%v, want ErrInvalidAdmission", err)
	}
	// An unresolved kind is a legal fact: the host states that it resolved no
	// concrete kind, and a consumer that needs the kind fails closed on it.
	unresolved := billing.AdmissionRouteFact{BackendInstanceID: "backend-a"}
	if err := unresolved.Validate(); err != nil {
		t.Fatalf("unresolved kind: %v", err)
	}
}

// TestExposureAdmissionInputCarriesOrderedRouteFacts locks the additive
// carriage: an admission with no route facts stays valid, the ordered facts
// survive validation and cloning, and the clone is independent.
func TestExposureAdmissionInputCarriesOrderedRouteFacts(t *testing.T) {
	t.Parallel()
	valid := billing.ExposureAdmissionInput{
		Subject:   validCallSubject(),
		Scope:     validScope(),
		AccountID: "acct-1",
		Quote:     validQuote(t),
		ExecutionLimits: []economics.Limit{
			{Name: "max-output", Value: 100, Unit: "token"},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixture admission input: %v", err)
	}
	routed := valid
	routed.Routes = []billing.AdmissionRouteFact{
		{BackendInstanceID: "backend-a", BackendKind: "kind-a"},
		{BackendInstanceID: "backend-b"},
		{BackendInstanceID: "backend-a", BackendKind: "kind-a"},
	}
	if err := routed.Validate(); err != nil {
		t.Fatalf("routed admission input: %v", err)
	}
	clone := routed.Clone()
	if len(clone.Routes) != 3 {
		t.Fatalf("cloned routes = %d, want 3", len(clone.Routes))
	}
	if clone.Routes[0].BackendInstanceID != "backend-a" || clone.Routes[1].BackendInstanceID != "backend-b" ||
		clone.Routes[2].BackendInstanceID != "backend-a" {
		t.Fatalf("cloned routes lost their order or multiplicity: %+v", clone.Routes)
	}
	if clone.Routes[1].BackendKind != "" {
		t.Fatalf("cloned route 1 kind = %q, want the unresolved empty kind", clone.Routes[1].BackendKind)
	}
	clone.Routes[0].BackendKind = "mutated"
	if routed.Routes[0].BackendKind != "kind-a" {
		t.Fatal("the clone shares its route slice with the original")
	}
	invalid := routed
	invalid.Routes = []billing.AdmissionRouteFact{{BackendKind: "kind-a"}}
	if err := invalid.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("route without an instance: err=%v, want ErrInvalidAdmission", err)
	}
	oversized := routed
	oversized.Routes = make([]billing.AdmissionRouteFact, billing.MaxAdmissionRoutes+1)
	for i := range oversized.Routes {
		oversized.Routes[i] = billing.AdmissionRouteFact{BackendInstanceID: "backend-a"}
	}
	if err := oversized.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("oversized route list: err=%v, want ErrInvalidAdmission", err)
	}
}

// TestExposureAdmissionInputOmitsAbsentRoutes locks the empty-versus-null
// JSON semantics: an admission without route facts marshals without the
// field, and the present list keeps its exact order.
func TestExposureAdmissionInputOmitsAbsentRoutes(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(billing.ExposureAdmissionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "routes") {
		t.Fatalf("an admission without route facts marshaled the field: %s", raw)
	}
	raw, err = json.Marshal(billing.ExposureAdmissionInput{
		Routes: []billing.AdmissionRouteFact{{BackendInstanceID: "backend-a", BackendKind: "kind-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"routes":[{"backend_instance_id":"backend-a","backend_kind":"kind-a"}]`) {
		t.Fatalf("the present route list did not marshal as expected: %s", raw)
	}
}

// TestExposureAdmissionPreflightInputValidation locks the pre-quote contract:
// the preflight input carries the route facts alone, its bound is enforced,
// and its clone is independent.
func TestExposureAdmissionPreflightInputValidation(t *testing.T) {
	t.Parallel()
	in := billing.ExposureAdmissionPreflightInput{
		Routes: []billing.AdmissionRouteFact{{BackendInstanceID: "backend-a", BackendKind: "kind-a"}},
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("fixture preflight input: %v", err)
	}
	empty := billing.ExposureAdmissionPreflightInput{}
	if err := empty.Validate(); err != nil {
		t.Fatalf("preflight input without routes: %v", err)
	}
	invalid := billing.ExposureAdmissionPreflightInput{
		Routes: []billing.AdmissionRouteFact{{BackendKind: "kind-a"}},
	}
	if err := invalid.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("route without an instance: err=%v, want ErrInvalidAdmission", err)
	}
	clone := in.Clone()
	clone.Routes[0].BackendKind = "mutated"
	if in.Routes[0].BackendKind != "kind-a" {
		t.Fatal("the preflight clone shares its route slice with the original")
	}
}
