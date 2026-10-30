package auth

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestAuthDecisionEvent_carriesOptionalSafeScope proves audit evidence can include a safe
// principal/scope snapshot from a trusted auth decision (requirements 6.1, 2.6, 5.2).
func TestAuthDecisionEvent_carriesOptionalSafeScope(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectService,
		PrincipalID:  scope.Known("svc-1"),
		CredentialID: scope.Known("key-1"),
	}
	ev := AuthDecisionEvent{
		Outcome: OutcomeAllow,
		Scope:   &trusted,
	}
	if ev.Scope == nil {
		t.Fatal("expected scope carried on auth decision event")
	}
	if !ev.Scope.PrincipalID.Equal(scope.Known("svc-1")) {
		t.Fatalf("event scope PrincipalID: %+v", ev.Scope.PrincipalID)
	}
}

// TestAuthDecisionEvent_scopeFieldNotSecretBearing ensures the new Scope field name does
// not suggest secret storage (requirement 2.6, 5.2).
func TestAuthDecisionEvent_scopeFieldNotSecretBearing(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[AuthDecisionEvent]()
	for field := range typ.Fields() {
		if eventFieldNameForbidden(field.Name) {
			t.Fatalf("field %q looks like a secret-bearing column name", field.Name)
		}
		if strings.Contains(field.Name, "Raw") {
			t.Fatalf("field %q suggests raw material", field.Name)
		}
	}
}

// TestAuthDecisionEvent_omitsRawSecretsFromScope proves scope values never carry bearer
// or api key material even when a trusted provider supplies attribution (requirement 2.6).
func TestAuthDecisionEvent_omitsRawSecretsFromScope(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("svc-1"),
		CredentialID: scope.Known("key-1"),
		SafeClaims:   map[string]string{"team": "core"},
	}
	ev := AuthDecisionEvent{Scope: &trusted}
	raw := strings.Join([]string{
		ev.Scope.PrincipalID.String(), ev.Scope.CredentialID.String(),
		ev.Scope.SafeClaims["team"],
	}, " ")
	for _, bad := range []string{"bearer ", "api_key=", "authorization:", "secret"} {
		if strings.Contains(strings.ToLower(raw), bad) {
			t.Fatalf("scope evidence contains credential-like material %q: %q", bad, raw)
		}
	}
}
