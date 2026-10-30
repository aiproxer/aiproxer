package secretguard_test

import (
	"os"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestRequestMatcherContext_roundTrip(t *testing.T) {
	t.Parallel()
	m := stubMatcher{}
	ctx := secretguard.WithRequestMatcher(t.Context(), m)
	got, ok := secretguard.RequestMatcherFromContext(ctx)
	if !ok {
		t.Fatal("expected matcher")
	}
	if got == nil {
		t.Fatal("expected non-nil matcher")
	}
}

func TestRequestMatcherFromContext_missing(t *testing.T) {
	t.Parallel()
	got, ok := secretguard.RequestMatcherFromContext(t.Context())
	if ok || got != nil {
		t.Fatalf("expected absent matcher, got ok=%v matcher=%v", ok, got)
	}
	got, ok = secretguard.RequestMatcherFromContext(nil) //nolint:staticcheck // SA1012: intentional nil context contract
	if ok || got != nil {
		t.Fatal("nil context must report absent matcher")
	}
}

func TestWithRequestMatcher_nilParent_usesTODO(t *testing.T) {
	t.Parallel()
	ctx := secretguard.WithRequestMatcher(
		nil, //nolint:staticcheck // SA1012: intentional nil parent contract
		stubMatcher{},
	)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	got, ok := secretguard.RequestMatcherFromContext(ctx)
	if !ok || got == nil {
		t.Fatalf("matcher ok=%v got=%v", ok, got)
	}
}

func TestContextMatcherResolver_returnsContextMatcher(t *testing.T) {
	t.Parallel()
	m := stubMatcher{}
	ctx := secretguard.WithRequestMatcher(t.Context(), m)
	var r secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	got, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected matcher from context")
	}
}

func TestContextMatcherResolver_absent_returnsNilNil_noEnvFallback(t *testing.T) {
	// Not parallel: uses t.Setenv to prove absence does not env-fallback.
	const probe = "SECRETGUARD_CTX_RESOLVER_PROBE_MUST_NOT_BE_READ"
	t.Setenv(probe, "should-never-be-read-by-resolver")
	if _, ok := os.LookupEnv(probe); !ok {
		t.Fatal("setup: probe env not set")
	}
	var r secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	got, err := r.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("absent matcher must not env-fallback; want (nil, nil)")
	}
}
