package oauthcred

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// GeneratePKCE creates a cryptographically random verifier, its S256 challenge, and random state.
// Verifier and state are 32 random bytes, base64url-encoded without padding (RFC 7636).
func GeneratePKCE() (verifier, challenge, state string, err error) {
	vBytes := make([]byte, 32)
	if _, err := rand.Read(vBytes); err != nil {
		return "", "", "", fmt.Errorf("oauthcred: generate verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(vBytes)
	challenge = ComputePKCEChallenge(verifier)

	sBytes := make([]byte, 32)
	if _, err := rand.Read(sBytes); err != nil {
		return "", "", "", fmt.Errorf("oauthcred: generate state: %w", err)
	}
	state = base64.RawURLEncoding.EncodeToString(sBytes)

	return verifier, challenge, state, nil
}

// ComputePKCEChallenge computes the code_challenge using SHA256 of the verifier,
// base64url-encoded without padding per RFC 7636 code_challenge_method=S256.
func ComputePKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ValidateState checks that the received state matches the expected state using constant-time comparison.
func ValidateState(expected, got string) error {
	if expected == "" || got == "" {
		return fmt.Errorf("oauthcred: state cannot be empty")
	}
	// subtle.ConstantTimeCompare short-circuits on length mismatch, leaking
	// the expected state length via timing. Hash both sides first so the
	// compared digests are always fixed-length (cf. internal/core/diag).
	expectedHash := sha256.Sum256([]byte(expected))
	gotHash := sha256.Sum256([]byte(got))
	if subtle.ConstantTimeCompare(expectedHash[:], gotHash[:]) != 1 {
		return fmt.Errorf("oauthcred: state mismatch")
	}
	return nil
}
