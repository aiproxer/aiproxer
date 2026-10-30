package auth

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestDecision_carriesOptionalTrustedScope proves a trusted auth provider can attach an
// authoritative safe scope to an allow decision, while legacy principal-only decisions
// still compile and behave unchanged (requirements 2.1, 2.5, 2.6, 7.1).
func TestDecision_carriesOptionalTrustedScope(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known("user-1"),
		CredentialID: scope.Known("key-1"),
		AuthMethod:   scope.Known("oidc"),
	}
	d := Decision{
		Outcome:   OutcomeAllow,
		Principal: execview.PrincipalView{ID: "user-1"},
		Scope:     &trusted,
	}
	if d.Scope == nil {
		t.Fatal("expected scope to be carried on decision")
	}
	if !d.Scope.PrincipalID.Equal(scope.Known("user-1")) {
		t.Fatalf("scope PrincipalID: %+v", d.Scope.PrincipalID)
	}
}

// TestDecision_legacyPrincipalOnlyStillCompiles proves existing principal-only decisions
// remain valid without supplying scope (requirement 7.1 compatibility).
func TestDecision_legacyPrincipalOnlyStillCompiles(t *testing.T) {
	t.Parallel()
	d := Decision{
		Outcome:   OutcomeAllow,
		Principal: execview.PrincipalView{ID: "legacy-user"},
	}
	if d.Scope != nil {
		t.Fatalf("expected nil scope on legacy decision, got %+v", d.Scope)
	}
	if d.Principal.ID != "legacy-user" {
		t.Fatalf("Principal.ID: got %q", d.Principal.ID)
	}
}
