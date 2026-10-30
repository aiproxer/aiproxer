// Package augmentation owns the deliberately narrow response-side
// continuation-carrier allowlist.
package augmentation

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// Capability identifies a canonical carrier that has been verified to be
// mutable plaintext continuation state. The current canonical compaction
// contract exposes only encrypted and opaque fields, so the allowlist is
// intentionally empty.
type Capability struct {
	ID string
}

// Capabilities returns the verified plaintext carrier allowlist. A fresh nil
// slice is returned until aipapi exposes a carrier with an explicit plaintext
// contract; native/unknown payloads must not be guessed to be text.
func Capabilities() []Capability { return nil }

// Match reports whether event contains one of the verified mutable plaintext
// carriers. It deliberately does not inspect, decode, or rewrite Opaque,
// EncryptedContent, signatures, or unknown extension bytes.
func Match(_ *aipapi.Event) (Capability, bool) {
	return Capability{}, false
}
