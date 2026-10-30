package scope

import (
	"context"
	"testing"
)

func TestScopeContext_roundTrip(t *testing.T) {
	t.Parallel()
	want := PrincipalScopeView{
		SubjectKind: SubjectHuman,
		PrincipalID: Known("u1"),
		Roles:       []string{"admin"},
		SafeClaims:  map[string]string{"team": "core"},
	}
	ctx := WithScope(context.Background(), want)
	got, ok := ScopeFromContext(ctx)
	if !ok {
		t.Fatal("expected scope")
	}
	if !got.PrincipalID.Equal(want.PrincipalID) {
		t.Fatalf("PrincipalID: %+v", got.PrincipalID)
	}
	if got.SubjectKind != want.SubjectKind {
		t.Fatalf("SubjectKind: got %v", got.SubjectKind)
	}
}

func TestScopeFromContext_missing(t *testing.T) {
	t.Parallel()
	if _, ok := ScopeFromContext(context.Background()); ok {
		t.Fatal("expected no scope on bare context")
	}
	if _, ok := ScopeFromContext(nil); ok { //nolint:staticcheck // SA1012: intentional nil context contract
		t.Fatal("expected no scope on nil context")
	}
}

func TestWithScope_nilParent_usesTODO(t *testing.T) {
	t.Parallel()
	ctx := WithScope(nil, PrincipalScopeView{PrincipalID: Known("x")}) //nolint:staticcheck // SA1012: intentional nil parent
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := ScopeFromContext(ctx)
	if !ok || !got.PrincipalID.Equal(Known("x")) {
		t.Fatalf("scope %+v ok=%v", got, ok)
	}
}

// TestScopeContext_returnsCopy proves the value retrieved from context is a copy so callers
// cannot mutate the stored scope through the returned view (requirement 5.5).
func TestScopeContext_returnsCopy(t *testing.T) {
	t.Parallel()
	want := PrincipalScopeView{
		PrincipalID: Known("u1"),
		Roles:       []string{"admin"},
		SafeClaims:  map[string]string{"k": "v"},
	}
	ctx := WithScope(context.Background(), want)
	got, _ := ScopeFromContext(ctx)
	got.Roles[0] = "mutated"
	got.SafeClaims["k"] = "mutated"
	got2, _ := ScopeFromContext(ctx)
	if got2.Roles[0] == "mutated" {
		t.Fatal("mutating retrieved Roles affected stored scope")
	}
	if got2.SafeClaims["k"] == "mutated" {
		t.Fatal("mutating retrieved SafeClaims affected stored scope")
	}
}
