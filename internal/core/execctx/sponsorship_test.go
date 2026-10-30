package execctx

import (
	"context"
	"testing"
)

// TestSponsorshipReadIsImmutable proves that the trusted funding declaration
// is immutable through its reads: a caller that mutates the declared key set
// it received must not change what the next read accepts. The declaration is
// the authority the funding gate consults, so a read must hand out ownership,
// never an alias of the stored key set.
func TestSponsorshipReadIsImmutable(t *testing.T) {
	ctx := WithSponsorship(context.Background(), Sponsorship{
		Mode:                SponsorshipModeOperatorSponsored,
		SponsorKey:          "outside",
		DeclaredSponsorKeys: []string{"declared"},
	})
	before, ok := SponsorshipFromContext(ctx)
	if !ok {
		t.Fatal("the declaration is missing from the context")
	}
	if before.SponsorKeyDeclared() {
		t.Fatal("invalid setup: an undeclared sponsor key must not be accepted")
	}
	before.DeclaredSponsorKeys[0] = "outside"
	after, ok := SponsorshipFromContext(ctx)
	if !ok {
		t.Fatal("the declaration is missing from the context")
	}
	if after.SponsorKeyDeclared() {
		t.Fatal("mutating a read changed the trusted declaration from refused to admitted")
	}
}

// TestSponsorshipReadOwnsItsKeySet proves that every read owns the declared
// key set it returns, so two readers can never share one slice.
func TestSponsorshipReadOwnsItsKeySet(t *testing.T) {
	ctx := WithSponsorship(context.Background(), Sponsorship{
		Mode:                SponsorshipModeOperatorSponsored,
		SponsorKey:          "memory-team",
		DeclaredSponsorKeys: []string{"memory-team"},
	})
	first, _ := SponsorshipFromContext(ctx)
	second, _ := SponsorshipFromContext(ctx)
	if len(first.DeclaredSponsorKeys) != 1 || len(second.DeclaredSponsorKeys) != 1 {
		t.Fatalf("the declared key set is missing: %v %v", first.DeclaredSponsorKeys, second.DeclaredSponsorKeys)
	}
	first.DeclaredSponsorKeys[0] = "other-team"
	if !second.SponsorKeyDeclared() {
		t.Fatal("two readers share one declared key set")
	}
	third, _ := SponsorshipFromContext(ctx)
	if !third.SponsorKeyDeclared() {
		t.Fatal("mutating a read changed the trusted declaration")
	}
}
