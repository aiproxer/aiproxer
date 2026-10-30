package execview_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func TestPrincipalContext_roundTrip(t *testing.T) {
	t.Parallel()
	want := execview.PrincipalView{
		ID:          "u1",
		DisplayName: "User One",
		Roles:       []string{"admin"},
		Claims:      map[string]string{"tenant": "a"},
	}
	ctx := execview.WithPrincipal(context.Background(), want)
	got, ok := execview.PrincipalFromContext(ctx)
	if !ok {
		t.Fatal("expected principal")
	}
	if got.ID != want.ID || got.DisplayName != want.DisplayName {
		t.Fatalf("principal mismatch: %+v vs %+v", got, want)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("roles: %+v", got.Roles)
	}
	if got.Claims["tenant"] != "a" {
		t.Fatalf("claims: %+v", got.Claims)
	}
}

func TestPrincipalFromContext_missing(t *testing.T) {
	t.Parallel()
	_, ok := execview.PrincipalFromContext(context.Background())
	if ok {
		t.Fatal("expected no principal")
	}
	_, ok = execview.PrincipalFromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok {
		t.Fatal("nil context")
	}
}

func TestWithPrincipal_nilParent_usesTODO(t *testing.T) {
	t.Parallel()
	ctx := execview.WithPrincipal(
		nil, //nolint:staticcheck // SA1012: intentional nil parent contract
		execview.PrincipalView{ID: "x"},
	)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := execview.PrincipalFromContext(ctx)
	if !ok || got.ID != "x" {
		t.Fatalf("principal %+v ok=%v", got, ok)
	}
}

func TestFrontendIDContext_roundTrip(t *testing.T) {
	t.Parallel()
	ctx := execview.WithFrontendID(context.Background(), "anthropic")
	got, ok := execview.FrontendIDFromContext(ctx)
	if !ok || got != "anthropic" {
		t.Fatalf("frontend id: got %q ok=%v", got, ok)
	}
}

func TestFrontendIDFromContext_trimsWhitespace(t *testing.T) {
	t.Parallel()
	ctx := execview.WithFrontendID(context.Background(), "  gemini  ")
	got, ok := execview.FrontendIDFromContext(ctx)
	if !ok || got != "gemini" {
		t.Fatalf("frontend id: got %q ok=%v", got, ok)
	}
}

func TestFrontendIDFromContext_missing(t *testing.T) {
	t.Parallel()
	_, ok := execview.FrontendIDFromContext(context.Background())
	if ok {
		t.Fatal("expected no frontend id")
	}
	_, ok = execview.FrontendIDFromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok {
		t.Fatal("nil context")
	}
}

func TestFrontendIDFromContext_emptyStored(t *testing.T) {
	t.Parallel()
	ctx := execview.WithFrontendID(context.Background(), "   ")
	_, ok := execview.FrontendIDFromContext(ctx)
	if ok {
		t.Fatal("expected empty-after-trim to be missing")
	}
}

func TestWithFrontendID_whitespaceDoesNotClobberParent(t *testing.T) {
	t.Parallel()
	parent := execview.WithFrontendID(context.Background(), "openai")
	ctx := execview.WithFrontendID(parent, "   ")
	id, ok := execview.FrontendIDFromContext(ctx)
	if !ok || id != "openai" {
		t.Fatalf("whitespace id must not clear parent, got %q ok=%v", id, ok)
	}
}

func TestWithFrontendID_nilParent_usesTODO(t *testing.T) {
	t.Parallel()
	ctx := execview.WithFrontendID(
		nil, //nolint:staticcheck // SA1012: intentional nil parent contract
		"openai_compatible",
	)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := execview.FrontendIDFromContext(ctx)
	if !ok || got != "openai_compatible" {
		t.Fatalf("frontend id %+v ok=%v", got, ok)
	}
}
