package auth

import (
	"context"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestLocalNoOpAuthenticator_buildsLocalScope proves local no-auth requests are marked as
// local single-user scope without inventing org attribution (requirement 1.4, 2.4, 3.5).
func TestLocalNoOpAuthenticator_buildsLocalScope(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{snap: OSIdentitySnapshot{
			PrincipalID: "alice",
			DisplayName: "Alice Example",
		}},
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Scope == nil {
		t.Fatal("expected scope on local no-op allow decision")
	}
	if d.Scope.SubjectKind != scope.SubjectLocal {
		t.Fatalf("SubjectKind: got %v want local", d.Scope.SubjectKind)
	}
	if !d.Scope.PrincipalID.Equal(scope.Known("alice")) {
		t.Fatalf("PrincipalID: %+v", d.Scope.PrincipalID)
	}
	if d.Scope.AuthMethod.String() != "local_noop" {
		t.Fatalf("AuthMethod: got %q", d.Scope.AuthMethod)
	}
	if !d.Scope.TenantID.IsUnknown() {
		t.Fatalf("TenantID must remain unknown, got %+v", d.Scope.TenantID)
	}
	if !d.Scope.ProjectID.IsUnknown() || !d.Scope.DepartmentID.IsUnknown() || !d.Scope.CostCenterID.IsUnknown() {
		t.Fatal("local no-op must not invent org attribution")
	}
}

// TestLocalNoOpAuthenticator_fallbackBuildsLocalScope proves the fallback unknown principal
// still produces a local synthetic scope (requirement 1.4).
func TestLocalNoOpAuthenticator_fallbackBuildsLocalScope(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{OS: fakeOSIdentity{err: context.Canceled}}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if d.Scope == nil {
		t.Fatal("expected scope")
	}
	if d.Scope.SubjectKind != scope.SubjectLocal {
		t.Fatalf("SubjectKind: got %v want local", d.Scope.SubjectKind)
	}
	if d.Scope.PrincipalID.String() != LocalUnknownOSPrincipalID {
		t.Fatalf("PrincipalID: got %q", d.Scope.PrincipalID)
	}
}
