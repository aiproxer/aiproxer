package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

type fakeOSIdentity struct {
	snap OSIdentitySnapshot
	err  error
}

func (f fakeOSIdentity) Current(ctx context.Context) (OSIdentitySnapshot, error) {
	_ = ctx
	return f.snap, f.err
}

func TestLocalNoOpAuthenticator_Authenticate_usesOSPrincipal(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{snap: OSIdentitySnapshot{
			PrincipalID:  "alice",
			DisplayName:  "Alice Example",
			FallbackUsed: false,
		}},
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{TraceID: "t1"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.SatisfiedLevel != sdkauth.LevelNone {
		t.Fatalf("SatisfiedLevel: got %v want none", d.SatisfiedLevel)
	}
	if d.Principal.ID != "alice" || d.Principal.DisplayName != "Alice Example" {
		t.Fatalf("Principal: %+v", d.Principal)
	}
}

func TestLocalNoOpAuthenticator_Authenticate_fallbackPrincipalStillNonEmpty(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{snap: OSIdentitySnapshot{
			PrincipalID:  LocalUnknownOSPrincipalID,
			DisplayName:  "",
			FallbackUsed: true,
		}},
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Principal.ID == "" {
		t.Fatal("principal must not be empty (no silent anonymous pass-through)")
	}
	if d.Principal.ID != LocalUnknownOSPrincipalID {
		t.Fatalf("Principal.ID: got %q", d.Principal.ID)
	}
}

func TestLocalNoOpAuthenticator_Authenticate_emptySnapshotIDUsesUnknown(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{snap: OSIdentitySnapshot{
			PrincipalID:  "   ",
			DisplayName:  "",
			FallbackUsed: false,
		}},
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if d.Principal.ID != LocalUnknownOSPrincipalID {
		t.Fatalf("empty OS id should map to unknown: got %q", d.Principal.ID)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
}

func TestLocalNoOpAuthenticator_Authenticate_osErrorUsesUnknown(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{err: context.Canceled},
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatalf("Authenticate should swallow OS error for stable allow: %v", err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Principal.ID != LocalUnknownOSPrincipalID {
		t.Fatalf("Principal.ID: got %q", d.Principal.ID)
	}
}

func TestLocalNoOpAuthenticator_Authenticate_osErrorInvokesFallbackCallback(t *testing.T) {
	t.Parallel()
	var gotErr error
	var gotHad bool
	a := LocalNoOpAuthenticator{
		OS: fakeOSIdentity{err: errors.New("lookup failed")},
		OnOSIdentityFallback: func(_ context.Context, lookupErr error, hadProvider bool) {
			gotErr, gotHad = lookupErr, hadProvider
		},
	}
	if _, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{TraceID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if !gotHad || gotErr == nil {
		t.Fatalf("callback: err=%v hadProvider=%v", gotErr, gotHad)
	}
}

func TestLocalNoOpAuthenticator_implementsAuthenticator(t *testing.T) {
	t.Parallel()
	var _ Authenticator = LocalNoOpAuthenticator{OS: fakeOSIdentity{snap: OSIdentitySnapshot{PrincipalID: "x"}}}
}

func TestLocalNoOpAuthenticator_neverReturnsZeroPrincipalOnAllow(t *testing.T) {
	t.Parallel()
	a := LocalNoOpAuthenticator{OS: fakeOSIdentity{snap: OSIdentitySnapshot{}}}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatal("expected allow")
	}
	if strings.TrimSpace(d.Principal.ID) == "" {
		t.Fatal("principal id must not be empty on allow (no silent anonymous pass-through)")
	}
}
