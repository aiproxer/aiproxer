package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestResolveRequestScope_trustedScopeWins proves a scope already attached to context is
// returned authoritatively and the principal projection is derived from it, ignoring any
// legacy principal in context (requirements 2.2, 4.6).
func TestResolveRequestScope_trustedScopeWins(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("scope-user"),
		Roles:       []string{"admin"},
	}
	ctx := scope.WithScope(context.Background(), trusted)
	ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "legacy-loses"})
	ex := TestExecutor()
	s, p, ok := ex.resolveRequestScope(ctx)
	if !ok {
		t.Fatal("expected scope resolved")
	}
	if s.PrincipalID.String() != "scope-user" {
		t.Fatalf("PrincipalID: got %q want scope-user", s.PrincipalID)
	}
	if p.ID != "scope-user" {
		t.Fatalf("principal projection must derive from scope, got %q", p.ID)
	}
}

// TestResolveRequestScope_legacyPrincipalFallback proves a legacy principal-only context
// (no scope) derives a scope with unknown optional fields preserved as unknown (req 4.2, 3.5).
func TestResolveRequestScope_legacyPrincipalFallback(t *testing.T) {
	t.Parallel()
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{
		ID:          "legacy-user",
		DisplayName: "Legacy",
		Roles:       []string{"ops"},
		Claims:      map[string]string{"tenant": "a"},
	})
	ex := TestExecutor()
	s, p, ok := ex.resolveRequestScope(ctx)
	if !ok {
		t.Fatal("expected scope resolved from legacy principal")
	}
	if s.PrincipalID.String() != "legacy-user" {
		t.Fatalf("PrincipalID: got %q", s.PrincipalID)
	}
	if s.SubjectKind != scope.SubjectUnknown {
		t.Fatalf("SubjectKind: got %v want unknown (no inference)", s.SubjectKind)
	}
	if s.DisplayName.String() != "Legacy" {
		t.Fatalf("DisplayName: got %q want Legacy (shared helper copies display)", s.DisplayName)
	}
	if len(s.Roles) != 1 || s.Roles[0] != "ops" {
		t.Fatalf("Roles: got %+v want [ops] (shared helper copies roles)", s.Roles)
	}
	if !s.AuthMethod.IsUnknown() {
		t.Fatalf("AuthMethod must remain unknown: runtime has no auth method, got %+v", s.AuthMethod)
	}
	if !s.TenantID.IsUnknown() || !s.ProjectID.IsUnknown() {
		t.Fatalf("optional fields must remain unknown: %+v %+v", s.TenantID, s.ProjectID)
	}
	if p.ID != "legacy-user" {
		t.Fatalf("principal projection ID: got %q", p.ID)
	}
	if p.Claims["tenant"] != "a" {
		t.Fatalf("principal projection claims must copy legacy claims: %v", p.Claims)
	}
}

// TestResolveRequestScope_localSyntheticFallback proves a local-mode executor with no
// identity produces an explicit local single-user scope (requirement 1.4, 2.4, 4.2).
func TestResolveRequestScope_localSyntheticFallback(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SyntheticLocalPrincipal = true
	s, p, ok := ex.resolveRequestScope(context.Background())
	if !ok {
		t.Fatal("expected synthetic local scope")
	}
	if s.SubjectKind != scope.SubjectLocal {
		t.Fatalf("SubjectKind: got %v want local", s.SubjectKind)
	}
	if s.PrincipalID.String() != syntheticLocalPrincipalID {
		t.Fatalf("PrincipalID: got %q want %q", s.PrincipalID, syntheticLocalPrincipalID)
	}
	if s.AuthMethod.String() != "local_noop" {
		t.Fatalf("AuthMethod: got %q", s.AuthMethod)
	}
	if !s.TenantID.IsUnknown() {
		t.Fatal("local synthetic must not invent tenant")
	}
	if p.Claims["issuer"] != syntheticLocalPrincipalIssuer {
		t.Fatalf("issuer claim: got %q want %q", p.Claims["issuer"], syntheticLocalPrincipalIssuer)
	}
}

// TestResolveRequestScope_noIdentityNoSynthetic proves no scope is produced when no
// identity is present and local synthesis is disabled (preserves prior behavior).
func TestResolveRequestScope_noIdentityNoSynthetic(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SyntheticLocalPrincipal = false
	_, _, ok := ex.resolveRequestScope(context.Background())
	if ok {
		t.Fatal("expected no scope when no identity and no synthetic fallback")
	}
}

// TestResolveRequestScope_emptyLegacyPrincipalFallsThrough proves a zero-ID legacy principal
// does not produce a scope; it falls through to the synthetic path when enabled.
func TestResolveRequestScope_emptyLegacyPrincipalFallsThrough(t *testing.T) {
	t.Parallel()
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "  "})
	ex := TestExecutor()
	ex.SyntheticLocalPrincipal = true
	s, _, ok := ex.resolveRequestScope(ctx)
	if !ok {
		t.Fatal("expected synthetic fallback")
	}
	if s.PrincipalID.String() != syntheticLocalPrincipalID {
		t.Fatalf("PrincipalID: got %q want synthetic %q", s.PrincipalID, syntheticLocalPrincipalID)
	}
}

func TestScopeFromCtx_prefersScopeContext(t *testing.T) {
	t.Parallel()
	trusted := scope.PrincipalScopeView{PrincipalID: scope.Known("scope-only")}
	ctx := scope.WithScope(context.Background(), trusted)

	s := scopeFromCtx(ctx)
	if !s.PrincipalID.Equal(scope.Known("scope-only")) {
		t.Fatalf("PrincipalID: got %+v want scope-only", s.PrincipalID)
	}
}
