package httpauth_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// TestScopeContext_httpauthAliasesScope proves the transport auth context aliases carry the
// authoritative scope at the edge (requirement 2.1, 4.1).
func TestScopeContext_httpauthAliasesScope(t *testing.T) {
	t.Parallel()
	want := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("u1"),
	}
	ctx := httpauth.WithScope(context.Background(), want)
	got, ok := httpauth.ScopeFromContext(ctx)
	if !ok {
		t.Fatal("expected scope")
	}
	if !got.PrincipalID.Equal(want.PrincipalID) {
		t.Fatalf("PrincipalID: %+v", got.PrincipalID)
	}
}

func TestScopeContext_httpauthMissing(t *testing.T) {
	t.Parallel()
	if _, ok := httpauth.ScopeFromContext(context.Background()); ok {
		t.Fatal("expected no scope on bare context")
	}
}
