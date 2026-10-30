package auth

import (
	"context"
	"strings"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestNewLocalAPIKeyAuthenticator_rejectsInvalidRecords(t *testing.T) {
	t.Parallel()
	_, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k1", PrincipalID: "p1", Key: ""},
	})
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestValidateLocalAPIKeyRecords_rejectsShortKey(t *testing.T) {
	t.Parallel()
	err := ValidateLocalAPIKeyRecords([]LocalAPIKeyRecord{
		{KeyID: "k1", PrincipalID: "p1", Key: "short"},
	})
	if err == nil {
		t.Fatal("expected error for key shorter than MinLocalAPIKeyRunes")
	}
}

func TestLocalAPIKeyAuthenticator_missingBearer(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "kid1", PrincipalID: "alice", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
}

func TestLocalAPIKeyAuthenticator_unknownKey(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "kid1", PrincipalID: "alice", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "wrong",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
}

func TestLocalAPIKeyAuthenticator_validBearerPrefix(t *testing.T) {
	t.Parallel()
	secret := "my-api-key-value"
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "app1", PrincipalID: "bob", Key: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "Bearer " + secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeAllow {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
	if d.Principal.ID != "bob" {
		t.Fatalf("Principal: %+v", d.Principal)
	}
	if d.Device.KeyID != "app1" {
		t.Fatalf("Device.KeyID: got %q", d.Device.KeyID)
	}
	if strings.Contains(d.Device.Fingerprint, secret) {
		t.Fatalf("fingerprint must not contain raw secret: %q", d.Device.Fingerprint)
	}
	if d.Device.Fingerprint == "" {
		t.Fatal("expected non-empty fingerprint")
	}
	if d.SatisfiedLevel != sdkauth.LevelAPIKey {
		t.Fatalf("SatisfiedLevel: got %v", d.SatisfiedLevel)
	}
}

func TestLocalAPIKeyAuthenticator_wrongKeyDifferentLength(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "1234567890123456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "short_but_wrong_len",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
}

func TestLocalAPIKeyAuthenticator_wrongKeySameLength(t *testing.T) {
	t.Parallel()
	secret := "1234567890123456"
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Authenticate(context.Background(), sdkauth.InboundCallMeta{
		AuthorizationBearer: "9999999999999999",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdkauth.OutcomeDeny {
		t.Fatalf("Outcome: got %v", d.Outcome)
	}
}

func TestLocalAPIKeyAuthenticator_implementsAuthenticator(t *testing.T) {
	t.Parallel()
	a, err := NewLocalAPIKeyAuthenticator([]LocalAPIKeyRecord{
		{KeyID: "k", PrincipalID: "p", Key: "xxxxxxxxxxxxxxxx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ Authenticator = a
}
