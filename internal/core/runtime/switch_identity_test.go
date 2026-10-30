package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func switchIdentityTestKey(fill byte) SeamDigestKey {
	var key SeamDigestKey
	for i := range key {
		key[i] = fill
	}
	return key
}

func TestNewAttemptSwitchIdentity_StableAndKeyScoped(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{
		Backend: "backend", Model: "logical", NativeModel: "native-a",
	}}
	key := switchIdentityTestKey(7)
	base := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-a"}, key)
	var none [32]byte
	if base.core == none || base.cacheTarget == none || base.openCredential == none {
		t.Fatalf("identity = %#v, want all reported components", base)
	}
	if again := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-a"}, key); again != base {
		t.Fatal("identity is not deterministic for the same key and inputs")
	}
	otherKey := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-a"}, switchIdentityTestKey(8))
	if otherKey == base {
		t.Fatal("a different process key must produce different identities")
	}
	if otherKey.core == base.core || otherKey.cacheTarget == base.cacheTarget || otherKey.openCredential == base.openCredential {
		t.Fatal("key separation must cover every identity component")
	}
	if keyless := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-a"}, SeamDigestKey{}); keyless != (attemptSwitchIdentity{}) {
		t.Fatalf("keyless identity = %#v, want zero evidence", keyless)
	}
}

func TestNewAttemptSwitchIdentity_CoreIdentityAndParams(t *testing.T) {
	t.Parallel()
	key := switchIdentityTestKey(9)
	base := routing.AttemptCandidate{Primary: routing.Primary{
		Backend: "backend", Model: "logical", NativeModel: "native-a",
	}}
	baseIdentity := newAttemptSwitchIdentity(base, execbackend.AttemptIdentity{}, key)

	backendChanged := base
	backendChanged.Primary.Backend = "other"
	if got := newAttemptSwitchIdentity(backendChanged, execbackend.AttemptIdentity{}, key); got.core == baseIdentity.core {
		t.Fatal("a backend change must change the core identity")
	}
	nativeChanged := base
	nativeChanged.Primary.NativeModel = "native-b"
	if got := newAttemptSwitchIdentity(nativeChanged, execbackend.AttemptIdentity{}, key); got.core == baseIdentity.core {
		t.Fatal("a native model change must change the core identity")
	}

	for name, params := range map[string]map[string][]string{
		"temperature":          {"temperature": {"0.9"}},
		"top_p":                {"top_p": {"0.5"}},
		"max_output_tokens":    {"max_output_tokens": {"4096"}},
		"reasoning_effort":     {"reasoning_effort": {"high"}},
		"unknown_selector_key": {"unused": {"value"}},
	} {
		withParams := base
		withParams.Primary.Params = params
		if got := newAttemptSwitchIdentity(withParams, execbackend.AttemptIdentity{}, key); got != baseIdentity {
			t.Fatalf("%s must not change the switch identity", name)
		}
	}
}

func TestNewAttemptSwitchIdentity_OptionalComponents(t *testing.T) {
	t.Parallel()
	key := switchIdentityTestKey(3)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "logical", NativeModel: "native"}}

	withoutTarget := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Credential: "cred-a"}, key)
	var none [32]byte
	if withoutTarget.cacheTarget != none {
		t.Fatal("an unreported adapter target must stay zero, not become a known core target")
	}
	withTargetA := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-a"}, key)
	withTargetB := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-b", Credential: "cred-a"}, key)
	if withTargetA.cacheTarget == withTargetB.cacheTarget {
		t.Fatal("a reported target change must change the target identity")
	}
	if withTargetA.core != withTargetB.core || withTargetA.openCredential != withTargetB.openCredential {
		t.Fatal("a target change must not change core or credential identity")
	}
	if withoutTarget.core != withTargetA.core {
		t.Fatal("an unreported target must not change the core identity")
	}

	withoutCredential := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a"}, key)
	if withoutCredential.openCredential != none {
		t.Fatal("an unreported open credential must stay zero")
	}
	credentialB := newAttemptSwitchIdentity(cand, execbackend.AttemptIdentity{Cache: "target-a", Credential: "cred-b"}, key)
	if credentialB.openCredential == withTargetA.openCredential {
		t.Fatal("an open credential change must change the credential identity")
	}
	if credentialB.core != withTargetA.core || credentialB.cacheTarget != withTargetA.cacheTarget {
		t.Fatal("an open credential change must not change core or target identity")
	}
}

func TestProviderAccountSwitchDigest(t *testing.T) {
	t.Parallel()
	key := switchIdentityTestKey(5)
	var none [32]byte
	if got := providerAccountSwitchDigest(SeamDigestKey{}, "account-a"); got != none {
		t.Fatal("a keyless executor must publish no account identity")
	}
	if got := providerAccountSwitchDigest(key, "  "); got != none {
		t.Fatal("an empty account key must publish no account identity")
	}
	a := providerAccountSwitchDigest(key, "account-a")
	b := providerAccountSwitchDigest(key, "account-b")
	if a == none || b == none {
		t.Fatal("a reported account key must produce an identity")
	}
	if a == b {
		t.Fatal("an account change must change the account identity")
	}
	if again := providerAccountSwitchDigest(key, "account-a"); again != a {
		t.Fatal("the account identity is not deterministic")
	}
	if other := providerAccountSwitchDigest(switchIdentityTestKey(6), "account-a"); other == a {
		t.Fatal("a different process key must change the account identity")
	}
}

func TestSwitchIdentityDigest_DomainAndLengthFraming(t *testing.T) {
	t.Parallel()
	key := switchIdentityTestKey(4)
	if got := switchIdentityDigest(SeamDigestKey{}, "domain", "value"); got != ([32]byte{}) {
		t.Fatal("keyless digest must be zero")
	}
	first := switchIdentityDigest(key, "domain-a", "ab", "c")
	second := switchIdentityDigest(key, "domain-a", "a", "bc")
	if first == second {
		t.Fatal("length framing must distinguish concatenated fields")
	}
	if third := switchIdentityDigest(key, "domain-b", "ab", "c"); third == first {
		t.Fatal("domain separation must distinguish feeds")
	}
}
