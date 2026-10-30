package openaicompat

import "testing"

func TestAttemptCacheTargetFingerprint_StableAndFieldScoped(t *testing.T) {
	t.Parallel()
	base := attemptCacheTargetFingerprint("https://api.example.test", "chat", "us-east-1")
	if base == "" {
		t.Fatal("expected a non-empty fingerprint")
	}
	if again := attemptCacheTargetFingerprint("https://api.example.test", "chat", "us-east-1"); again != base {
		t.Fatalf("fingerprint is not stable: %q != %q", again, base)
	}
	for name, got := range map[string]string{
		"base_url": attemptCacheTargetFingerprint("https://other.example.test", "chat", "us-east-1"),
		"flavor":   attemptCacheTargetFingerprint("https://api.example.test", "responses", "us-east-1"),
		"region":   attemptCacheTargetFingerprint("https://api.example.test", "chat", "eu-west-1"),
	} {
		if got == base {
			t.Fatalf("%s change did not change the fingerprint", name)
		}
	}
	if got := attemptCacheTargetFingerprint("", "", ""); got != "" {
		t.Fatalf("empty target fingerprint = %q, want empty", got)
	}
}
