package execctx_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestWithViews_carriesScope proves Views carries the authoritative scope alongside the
// existing principal/session/attempt/workspace/annotations fields (requirement 4.6, 5.1).
func TestWithViews_carriesScope(t *testing.T) {
	t.Parallel()
	want := scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		PrincipalID:  scope.Known("u1"),
		TenantID:     scope.Known("t1"),
		Roles:        []string{"admin"},
		SafeClaims:   map[string]string{"team": "core"},
		PolicyLabels: map[string]string{"env": "prod"},
		Origin:       scope.OriginClient,
	}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Scope: want})
	got, ok := execctx.FromContext(ctx)
	if !ok {
		t.Fatal("expected views")
	}
	if !got.Scope.PrincipalID.Equal(want.PrincipalID) {
		t.Fatalf("Scope.PrincipalID: %+v", got.Scope.PrincipalID)
	}
	if got.Scope.SubjectKind != want.SubjectKind {
		t.Fatalf("SubjectKind: got %v want %v", got.Scope.SubjectKind, want.SubjectKind)
	}
	if !got.Scope.TenantID.Equal(want.TenantID) {
		t.Fatalf("TenantID: %+v", got.Scope.TenantID)
	}
}

// TestWithViews_scopeMapSliceIsolation proves the scope snapshot is deep-copied on attach
// so callers cannot mutate the stored scope through their original slices/maps (req 5.5, 4.2).
func TestWithViews_scopeMapSliceIsolation(t *testing.T) {
	t.Parallel()
	in := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("u1"),
		Roles:        []string{"admin"},
		SafeClaims:   map[string]string{"k": "v"},
		PolicyLabels: map[string]string{"env": "prod"},
	}
	ctx := execctx.WithViews(context.Background(), execctx.Views{Scope: in})
	in.Roles[0] = "mutated"
	in.SafeClaims["k"] = "mutated"
	in.PolicyLabels["env"] = "mutated"
	got, _ := execctx.FromContext(ctx)
	if got.Scope.Roles[0] == "mutated" {
		t.Fatal("mutating input Roles affected stored scope")
	}
	if got.Scope.SafeClaims["k"] == "mutated" {
		t.Fatal("mutating input SafeClaims affected stored scope")
	}
	if got.Scope.PolicyLabels["env"] == "mutated" {
		t.Fatal("mutating input PolicyLabels affected stored scope")
	}
}

// TestWithViews_scopeReadIsCopy proves FromContext returns a copy of the stored scope so
// callers cannot mutate the stored snapshot through the retrieved view (requirement 5.5).
func TestWithViews_scopeReadIsCopy(t *testing.T) {
	t.Parallel()
	ctx := execctx.WithViews(context.Background(), execctx.Views{Scope: scope.PrincipalScopeView{
		PrincipalID: scope.Known("u1"),
		Roles:       []string{"admin"},
		SafeClaims:  map[string]string{"k": "v"},
	}})
	got, _ := execctx.FromContext(ctx)
	got.Scope.Roles[0] = "mutated"
	got.Scope.SafeClaims["k"] = "mutated"
	got2, _ := execctx.FromContext(ctx)
	if got2.Scope.Roles[0] == "mutated" {
		t.Fatal("mutating retrieved Roles affected stored scope")
	}
	if got2.Scope.SafeClaims["k"] == "mutated" {
		t.Fatal("mutating retrieved SafeClaims affected stored scope")
	}
}

// TestWithViews_annotationsSeparateFromScope proves lifecycle annotations remain separate
// from trusted attribution and do not modify scope fields (requirement 4.3, 4.2).
func TestWithViews_annotationsSeparateFromScope(t *testing.T) {
	t.Parallel()
	sc := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("u1"),
		PolicyLabels: map[string]string{"env": "prod"},
	}
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:       sc,
		Annotations: map[string]string{"env": "annotated", "note": "x"},
	})
	got, _ := execctx.FromContext(ctx)
	if got.Annotations["env"] != "annotated" {
		t.Fatalf("annotations env: %q", got.Annotations["env"])
	}
	if got.Scope.PolicyLabels["env"] != "prod" {
		t.Fatalf("scope policy label env must remain %q, got %q", "prod", got.Scope.PolicyLabels["env"])
	}
	if got.Annotations["note"] != "x" {
		t.Fatalf("annotations note: %q", got.Annotations["note"])
	}
}
