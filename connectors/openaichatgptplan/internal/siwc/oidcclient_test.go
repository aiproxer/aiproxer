package siwc

import (
	"crypto/rsa"
	"strings"
	"testing"
)

// The provider's key set.
//
// The key set is UNTRUSTED INPUT, and the reader that understands it is
// the place where a malformed document would turn into a panic. The fuzz
// target below is the property that matters: the reader either refuses
// the document or understands it, and it never panics and never repeats a
// part of the document in its failure.

// FuzzParseJWKS_RefusesAnything proves that the key-set reader never
// panics and never renders the document.
//
// The failure is a CONSTANT: the document is untrusted, and a diagnostic
// that repeated a part of it would put unvetted text into an operator- or
// support-facing error.
func FuzzParseJWKS_RefusesAnything(f *testing.F) {
	f.Add(`{"keys":[{"kty":"RSA","kid":"a","use":"sig","alg":"RS256","n":"AQAB","e":"AQAB"}]}`)
	f.Add(`{"keys":[]}`)
	f.Add(`{"keys":null}`)
	f.Add(`{}`)
	f.Add(`not json`)
	f.Add(`{"keys":[{"kty":"RSA","kid":"a","n":"","e":""}]}`)
	f.Add(`{"keys":[{"kty":"RSA","kid":"a","n":123,"e":456}]}`)
	f.Add(`{"keys":"not a list"}`)
	f.Add(`{"keys":[{"kty":"RSA","kid":"a","n":"AQAB","e":"AQAB"},{"kty":"RSA","kid":"a","n":"AQAB","e":"AQAB"}]}`)
	f.Add(strings.Repeat("a", 4096))
	f.Add(strings.Repeat(`{"kty":"RSA"},`, 512))
	// The document below is a SUBSTRING of the constant refusal. It is the
	// regression seed of the test-only collision: a containment oracle
	// reports it as a leak, while the constant itself is what the failure
	// must equal.
	f.Add("the provider's document")

	f.Fuzz(func(t *testing.T, document string) {
		keys, err := parseJWKS([]byte(document))
		if err != nil {
			// The refusal is a CONSTANT and value-free, so the exact
			// equality IS the leak assertion. A containment check on the
			// untrusted document adds nothing and is unsound: a document
			// that is a substring of the constant would be reported as a
			// leak the failure does not carry.
			if err.Error() != errMalformedDocument.Error() {
				t.Fatalf("the failure is not the constant refusal: %v", err)
			}
			return
		}
		for keyID, key := range keys {
			if keyID == "" {
				t.Fatal("the key set holds an empty key id")
			}
			if key == nil || key.N == nil || key.N.BitLen() < minRSAKeyBits {
				t.Fatalf("the key %q is not a usable key", keyID)
			}
			if key.E < 3 || key.E%2 == 0 {
				t.Fatalf("the key %q has an unusable exponent", keyID)
			}
		}
	})
}

// TestParseJWKS_SkipsTheKeysThisConnectorCannotUse proves that a key set
// that publishes keys of other types, other uses or other algorithms is
// understood, and that only the key this connector can verify with is
// offered.
//
// Skipping a key is not sampling: a key of another type or algorithm is
// not a candidate for the accepted algorithm, and the key id of a usable
// key still names exactly one key.
func TestParseJWKS_SkipsTheKeysThisConnectorCannotUse(t *testing.T) {
	key := testRSAKey(t)
	exponent := []byte{0x01, 0x00, 0x01}
	modulus := key.N.Bytes()
	document := `{"keys":[` +
		`{"kty":"EC","kid":"ec-1","use":"sig","alg":"ES256","n":"AQAB","e":"AQAB"},` +
		`{"kty":"RSA","kid":"enc-1","use":"enc","alg":"RS256","n":"` +
		base64RawURL(t, string(modulus)) + `","e":"AQAB"},` +
		`{"kty":"RSA","kid":"other-alg","use":"sig","alg":"RS512","n":"` +
		base64RawURL(t, string(modulus)) + `","e":"AQAB"},` +
		`{"kty":"RSA","kid":"usable","use":"sig","alg":"RS256","n":"` +
		base64RawURL(t, string(modulus)) + `","e":"` +
		base64RawURL(t, string(exponent)) + `"}` +
		`]}`

	keys, err := parseJWKS([]byte(document))
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("the key set holds %d usable keys, want 1", len(keys))
	}
	usable, ok := keys["usable"]
	if !ok {
		t.Fatal("the usable key is missing")
	}
	var expected rsa.PublicKey
	expected.N = key.N
	expected.E = key.E
	if usable.N.Cmp(expected.N) != 0 || usable.E != expected.E {
		t.Fatal("the usable key does not carry the published material")
	}
}
