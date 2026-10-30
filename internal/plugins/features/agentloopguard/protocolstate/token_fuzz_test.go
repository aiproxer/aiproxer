package protocolstate

import (
	"encoding/base64"
	"strings"
	"testing"
)

// FuzzDecodeRejectsMutation feeds codec mutations through the strict decoder.
// Structural validation is corruption detection for trusted platform lineage,
// not authentication: any accepted token must round-trip to the encoded state.
func FuzzDecodeRejectsMutation(f *testing.F) {
	fp := Fingerprint(fingerprintInput())
	valid, err := Encode(State{Reprompts: 1, LastFingerprint: fp, ConsecutiveNoProgress: 1})
	if err != nil {
		f.Fatalf("Encode: %v", err)
	}
	payload := strings.TrimPrefix(valid, TokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		f.Fatalf("seed decode: %v", err)
	}
	f.Add(valid)
	f.Add(TokenPrefix)
	f.Add("alg-state-v1." + payload)
	f.Add(TokenPrefix + strings.Repeat("A", MaxTokenBytes))
	for _, state := range []State{
		{},
		{Terminal: true},
		{Reprompts: MaxReprompts, LastFingerprint: fp, ConsecutiveNoProgress: MaxConsecutiveNoProgress},
	} {
		seed, err := Encode(state)
		if err != nil {
			f.Fatalf("Encode seed: %v", err)
		}
		f.Add(seed)
	}
	f.Add(TokenPrefix + "!!!!")
	f.Add(TokenPrefix + payload + "AA")

	// Single-byte payload mutations exercise every header field and the
	// fingerprint length/trailing payload checks.
	for i := range raw {
		for _, delta := range []byte{0x01, 0x7f, 0x80, 0xff} {
			clone := append([]byte(nil), raw...)
			clone[i] ^= delta
			f.Add(TokenPrefix + base64.RawURLEncoding.EncodeToString(clone))
		}
	}

	f.Fuzz(func(t *testing.T, token string) {
		state, err := Decode(token)
		if err != nil {
			if state != (State{}) {
				t.Fatalf("Decode(%q) returned state %+v with an error", token, state)
			}
			return
		}
		reencoded, err := Encode(state)
		if err != nil {
			t.Fatalf("Decode accepted %q but Encode rejected the state %+v: %v", token, state, err)
		}
		if reencoded != token {
			t.Fatalf("Decode accepted noncanonical token %q, canonical form is %q", token, reencoded)
		}
	})
}
