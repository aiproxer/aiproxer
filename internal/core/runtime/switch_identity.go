package runtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

const (
	switchCoreDomain            = "aiproxer/switch/core/v1\x00"
	switchCacheTargetDomain     = "aiproxer/switch/cache-target/v1\x00"
	switchOpenCredentialDomain  = "aiproxer/switch/open-credential/v1\x00"
	switchProviderAccountDomain = "aiproxer/switch/provider-account/v1\x00"
)

// attemptSwitchIdentity is the content-free evidence of one committed B-leg's
// downstream target. It is captured at successful open and rides the attempt's
// own usage samples. The prompt-cache collapse diagnostic owns the comparison:
// it stores the evidence of the latest accepted healthy sample in launch order
// and compares it with the collapse-triggering leg.
//
// Core identity (backend plus resolved native model) is always present when the
// diagnostic feed is armed. The adapter-reported cache target, the credential
// selected at open, and the provider account that arrives in usage accounting
// are each optional: a zero component means the backend did not report it, so
// the diagnostic must not treat unknown and known values as a change. Keeping
// the components separate is deliberate: a core provider change is always
// comparable, while a target change requires target evidence on both sides.
type attemptSwitchIdentity struct {
	core           [32]byte
	cacheTarget    [32]byte
	openCredential [32]byte
}

// newAttemptSwitchIdentity reduces the core provider choice and the adapter's
// optional observations to keyed comparison tokens. A zero key disables the
// feed entirely: no externally observable fingerprint is published rather
// than a publicly reproducible hash.
func newAttemptSwitchIdentity(cand routing.AttemptCandidate, observed execbackend.AttemptIdentity, key SeamDigestKey) attemptSwitchIdentity {
	if key.IsZero() {
		return attemptSwitchIdentity{}
	}
	nativeModel := strings.TrimSpace(cand.Primary.NativeModel)
	if nativeModel == "" {
		nativeModel = strings.TrimSpace(cand.Primary.Model)
	}
	identity := attemptSwitchIdentity{
		core: switchIdentityDigest(key, switchCoreDomain,
			strings.TrimSpace(cand.Primary.Backend), nativeModel),
	}
	if target := strings.TrimSpace(observed.Cache); target != "" {
		identity.cacheTarget = switchIdentityDigest(key, switchCacheTargetDomain, target)
	}
	if credential := strings.TrimSpace(observed.Credential); credential != "" {
		identity.openCredential = switchIdentityDigest(key, switchOpenCredentialDomain, credential)
	}
	return identity
}

// providerAccountSwitchDigest reduces the provider account carried by one
// usage sample's accounting metadata to a keyed comparison token. It is
// evaluated on that sample, so a connector that reports the account only in
// usage (and not at open) still participates in credential attribution.
//
// The account key is often a low-entropy provider identifier such as a pool
// slot. It is never published raw and is not compared unless both the healthy
// reference and the trigger carry this component.
func providerAccountSwitchDigest(key SeamDigestKey, accountKey string) [32]byte {
	accountKey = strings.TrimSpace(accountKey)
	if key.IsZero() || accountKey == "" {
		return [32]byte{}
	}
	return switchIdentityDigest(key, switchProviderAccountDomain, accountKey)
}

// switchIdentityDigest is domain-separated HMAC-SHA256 over length-prefixed
// fields under the process-stable secret. Length framing keeps concatenated
// fields unambiguous; domain separation keeps the switch feeds independent of
// the seam and instruction feeds that borrow the same process key. No part or
// key is retained or published raw.
func switchIdentityDigest(key SeamDigestKey, domain string, parts ...string) [32]byte {
	if key.IsZero() {
		return [32]byte{}
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(domain))
	for _, part := range parts {
		writeIdentityField(mac, part)
	}
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

type identityWriter interface {
	Write([]byte) (int, error)
}

func writeIdentityField(w identityWriter, value string) {
	var length [8]byte
	n := uint64(len(value))
	for i := range length {
		length[i] = byte(n >> (8 * (7 - i)))
	}
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(value))
}
