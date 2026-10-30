package identity_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
)

func TestWithClientUserAgent_marksCallPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ua   string
	}{
		{name: "nonempty", ua: "Cursor/1.2"},
		{name: "empty_omittable", ua: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := identity.WithClientUserAgent(context.Background(), tc.ua)
			got, ok := identity.CallClientUserAgent(ctx)
			if !ok {
				t.Fatal("expected call-path marker")
			}
			if got != tc.ua {
				t.Fatalf("ua: got %q want %q", got, tc.ua)
			}
		})
	}
}

func TestCallClientUserAgent_backgroundAbsent(t *testing.T) {
	t.Parallel()
	_, ok := identity.CallClientUserAgent(context.Background())
	if ok {
		t.Fatal("background context must not look like a call path")
	}
}

func TestWithClientUserAgent_nilContextUsesBackground(t *testing.T) {
	t.Parallel()
	ctx := identity.WithClientUserAgent(nil, "Agent/9") //nolint:staticcheck // API contract: nil ctx → Background
	got, ok := identity.CallClientUserAgent(ctx)
	if !ok || got != "Agent/9" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
}

func TestWithBackgroundIdentity_forcesBackgroundOverCallMarker(t *testing.T) {
	t.Parallel()
	call := identity.WithClientUserAgent(context.Background(), "Client/1")
	bg := identity.WithBackgroundIdentity(call)
	if _, ok := identity.CallClientUserAgent(bg); ok {
		t.Fatal("background identity must suppress call-path marker")
	}
	if _, ok := identity.CallClientUserAgent(call); !ok {
		t.Fatal("parent call marker must remain intact")
	}
}

func TestWithBackgroundIdentity_nilSafe(t *testing.T) {
	t.Parallel()
	bg := identity.WithBackgroundIdentity(nil) //nolint:staticcheck // API contract: nil ctx → Background
	if _, ok := identity.CallClientUserAgent(bg); ok {
		t.Fatal("nil-derived background must not be a call path")
	}
}
