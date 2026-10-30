package scope_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func sampleView() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-1"),
		DisplayName:    scope.Known("Alice"),
		AuthMethod:     scope.Known("oidc"),
		CredentialID:   scope.Known("key-1"),
		Roles:          []string{"admin", "operator"},
		SafeClaims:     map[string]string{"tenant": "t1", "team": "core"},
		TenantID:       scope.Known("t1"),
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Known("ws-1"),
		ProjectID:      scope.Unknown(),
		DepartmentID:   scope.Unknown(),
		CostCenterID:   scope.Unknown(),
		PolicyLabels:   map[string]string{"env": "prod", "tier": "0"},
		Origin:         scope.OriginClient,
		ParentTraceID:  scope.Unknown(),
	}
}

func TestPrincipalScopeView_NoForbiddenSecretFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"Token", "Secret", "Bearer", "APIKey", "OAuth",
		"Header", "Password", "Raw",
	}
	rt := reflect.TypeFor[scope.PrincipalScopeView]()
	for f := range rt.Fields() {
		for _, bad := range forbidden {
			if strings.Contains(f.Name, bad) {
				t.Fatalf("field %s contains forbidden substring %q (raw secret/transport must not be in scope)", f.Name, bad)
			}
		}
	}
}

func TestPrincipalScopeView_CloneIsolatesRoles(t *testing.T) {
	t.Parallel()
	orig := sampleView()
	clone := orig.Clone()
	clone.Roles[0] = "mutated"
	if orig.Roles[0] == "mutated" {
		t.Fatal("mutating clone Roles affected original")
	}
	clone.Roles = append(clone.Roles, "extra")
	if len(orig.Roles) == len(clone.Roles) {
		t.Fatal("appending to clone Roles affected original length")
	}
}

func TestPrincipalScopeView_CloneIsolatesSafeClaims(t *testing.T) {
	t.Parallel()
	orig := sampleView()
	clone := orig.Clone()
	clone.SafeClaims["tenant"] = "mutated"
	if orig.SafeClaims["tenant"] == "mutated" {
		t.Fatal("mutating clone SafeClaims affected original")
	}
	clone.SafeClaims["new"] = "v"
	if _, ok := orig.SafeClaims["new"]; ok {
		t.Fatal("adding to clone SafeClaims leaked into original")
	}
}

func TestPrincipalScopeView_CloneIsolatesPolicyLabels(t *testing.T) {
	t.Parallel()
	orig := sampleView()
	clone := orig.Clone()
	clone.PolicyLabels["env"] = "mutated"
	if orig.PolicyLabels["env"] == "mutated" {
		t.Fatal("mutating clone PolicyLabels affected original")
	}
	delete(clone.PolicyLabels, "tier")
	if _, ok := orig.PolicyLabels["tier"]; !ok {
		t.Fatal("deleting from clone PolicyLabels leaked into original")
	}
}

func TestPrincipalScopeView_ClonePreservesValues(t *testing.T) {
	t.Parallel()
	orig := sampleView()
	clone := orig.Clone()
	if !clone.PrincipalID.Equal(orig.PrincipalID) {
		t.Fatal("clone PrincipalID mismatch")
	}
	if clone.SubjectKind != orig.SubjectKind {
		t.Fatal("clone SubjectKind mismatch")
	}
	if clone.Origin != orig.Origin {
		t.Fatal("clone Origin mismatch")
	}
	if len(clone.Roles) != len(orig.Roles) {
		t.Fatal("clone Roles length mismatch")
	}
	if len(clone.SafeClaims) != len(orig.SafeClaims) {
		t.Fatal("clone SafeClaims length mismatch")
	}
}

func TestPrincipalScopeView_CloneHandlesNilMapsAndSlices(t *testing.T) {
	t.Parallel()
	v := scope.PrincipalScopeView{PrincipalID: scope.Known("x")}
	clone := v.Clone()
	if clone.Roles != nil {
		t.Fatalf("nil Roles should stay nil, got %v", clone.Roles)
	}
	if clone.SafeClaims != nil {
		t.Fatalf("nil SafeClaims should stay nil, got %v", clone.SafeClaims)
	}
	if clone.PolicyLabels != nil {
		t.Fatalf("nil PolicyLabels should stay nil, got %v", clone.PolicyLabels)
	}
}

func TestPrincipalScopeView_PrincipalProjection(t *testing.T) {
	t.Parallel()
	v := sampleView()
	p := v.Principal()
	if p.ID != "user-1" {
		t.Fatalf("Principal ID: got %q want %q", p.ID, "user-1")
	}
	if p.DisplayName != "Alice" {
		t.Fatalf("Principal DisplayName: got %q want %q", p.DisplayName, "Alice")
	}
	if len(p.Roles) != 2 || p.Roles[0] != "admin" || p.Roles[1] != "operator" {
		t.Fatalf("Principal Roles: got %+v", p.Roles)
	}
	if p.Claims["tenant"] != "t1" || p.Claims["team"] != "core" {
		t.Fatalf("Principal Claims: got %+v", p.Claims)
	}
}

func TestPrincipalScopeView_PrincipalProjectionFromUnknown(t *testing.T) {
	t.Parallel()
	v := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectUnknown,
		PrincipalID: scope.Unknown(),
		DisplayName: scope.Unknown(),
	}
	p := v.Principal()
	if p.ID != "" {
		t.Fatalf("unknown PrincipalID must project to empty ID, got %q", p.ID)
	}
	if p.DisplayName != "" {
		t.Fatalf("unknown DisplayName must project to empty, got %q", p.DisplayName)
	}
	if p.Roles != nil {
		t.Fatalf("nil Roles must project to nil, got %+v", p.Roles)
	}
	if p.Claims != nil {
		t.Fatalf("nil Claims must project to nil, got %+v", p.Claims)
	}
}

func TestPrincipalScopeView_PrincipalProjectionIsCopy(t *testing.T) {
	t.Parallel()
	v := sampleView()
	p := v.Principal()
	p.Roles[0] = "mutated"
	p.Claims["tenant"] = "mutated"
	if v.Roles[0] == "mutated" {
		t.Fatal("mutating projected Roles affected scope")
	}
	if v.SafeClaims["tenant"] == "mutated" {
		t.Fatal("mutating projected Claims affected scope SafeClaims")
	}
}

func TestPrincipalScopeView_PrincipalProjectionKnownEmpty(t *testing.T) {
	t.Parallel()
	v := scope.PrincipalScopeView{
		PrincipalID: scope.Known(""),
		DisplayName: scope.Known(""),
	}
	p := v.Principal()
	if p.ID != "" {
		t.Fatalf("known-empty PrincipalID must project to empty, got %q", p.ID)
	}
	if p.DisplayName != "" {
		t.Fatalf("known-empty DisplayName must project to empty, got %q", p.DisplayName)
	}
}

func TestSubjectKind_Constants(t *testing.T) {
	t.Parallel()
	if scope.SubjectUnknown != "unknown" {
		t.Fatalf("SubjectUnknown = %q", scope.SubjectUnknown)
	}
	if scope.SubjectHuman != "human" {
		t.Fatalf("SubjectHuman = %q", scope.SubjectHuman)
	}
	if scope.SubjectService != "service" {
		t.Fatalf("SubjectService = %q", scope.SubjectService)
	}
	if scope.SubjectLocal != "local" {
		t.Fatalf("SubjectLocal = %q", scope.SubjectLocal)
	}
}

func TestOrigin_Constants(t *testing.T) {
	t.Parallel()
	if scope.OriginClient != "client" {
		t.Fatalf("OriginClient = %q", scope.OriginClient)
	}
	if scope.OriginInternal != "internal" {
		t.Fatalf("OriginInternal = %q", scope.OriginInternal)
	}
}

func TestPrincipalScopeView_CompilesAsPrincipalViewSource(t *testing.T) {
	t.Parallel()
	requirePrincipalView(sampleView().Principal())
}

// requirePrincipalView is a compile-time guard asserting the projected principal satisfies
// execview.PrincipalView; it performs no runtime check.
func requirePrincipalView(_ execview.PrincipalView) {}
