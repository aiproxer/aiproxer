package httpauth_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// TestAuthenticationResult_carriesOptionalScope proves transport auth results can carry a
// trusted safe scope from auth provider code into the middleware (requirement 2.1, 2.5).
func TestAuthenticationResult_carriesOptionalScope(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known("user-1"),
		CredentialID: scope.Known("key-1"),
	}
	r := httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "user-1"},
		Scope:     &trusted,
	}
	if r.Scope == nil {
		t.Fatal("expected scope carried on transport auth result")
	}
	if !r.Scope.PrincipalID.Equal(scope.Known("user-1")) {
		t.Fatalf("result scope PrincipalID: %+v", r.Scope.PrincipalID)
	}
}

// TestAuthenticationResult_legacyPrincipalOnlyStillCompiles proves existing principal-only
// results remain valid without scope (requirement 7.1).
func TestAuthenticationResult_legacyPrincipalOnlyStillCompiles(t *testing.T) {
	t.Parallel()
	r := httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "legacy"},
	}
	if r.Scope != nil {
		t.Fatalf("expected nil scope on legacy result, got %+v", r.Scope)
	}
}
