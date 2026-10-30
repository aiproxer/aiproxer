package openaicred

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

func TestCredentialFingerprint_StableAndFieldScoped(t *testing.T) {
	t.Parallel()
	base := credentialFingerprint(credpool.Credential{
		ID: "cred-a", RemoteOrgID: "org-a", RemoteAccountID: "acct-a", RemoteRegion: "us-east-1",
	})
	if base == "" {
		t.Fatal("expected a non-empty fingerprint")
	}
	if again := credentialFingerprint(credpool.Credential{
		ID: "cred-a", RemoteOrgID: "org-a", RemoteAccountID: "acct-a", RemoteRegion: "us-east-1",
	}); again != base {
		t.Fatalf("fingerprint is not stable: %q != %q", again, base)
	}
	for name, cred := range map[string]credpool.Credential{
		"id":      {ID: "cred-b", RemoteOrgID: "org-a", RemoteAccountID: "acct-a", RemoteRegion: "us-east-1"},
		"org":     {ID: "cred-a", RemoteOrgID: "org-b", RemoteAccountID: "acct-a", RemoteRegion: "us-east-1"},
		"account": {ID: "cred-a", RemoteOrgID: "org-a", RemoteAccountID: "acct-b", RemoteRegion: "us-east-1"},
		"region":  {ID: "cred-a", RemoteOrgID: "org-a", RemoteAccountID: "acct-a", RemoteRegion: "eu-west-1"},
	} {
		if got := credentialFingerprint(cred); got == base {
			t.Fatalf("%s change did not change the fingerprint", name)
		}
	}
	if got := credentialFingerprint(credpool.Credential{}); got != "" {
		t.Fatalf("empty credential fingerprint = %q, want empty", got)
	}
}

func TestCredentialFingerprint_IgnoresSecret(t *testing.T) {
	t.Parallel()
	a := credentialFingerprint(credpool.Credential{ID: "cred-a", Secret: "secret-a"})
	b := credentialFingerprint(credpool.Credential{ID: "cred-a", Secret: "secret-b"})
	if a != b {
		t.Fatal("the fingerprint must not depend on secret material")
	}
}
