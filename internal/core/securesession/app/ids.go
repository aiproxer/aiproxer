package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

const (
	resumeTokenRandBytes = 48
	sessionIDRandBytes   = 32
)

// RandGenerator creates opaque ids/tokens from crypto/rand and HMAC-SHA-256 fingerprints.
type RandGenerator struct {
	fpKey []byte
}

// NewRandGenerator returns a generator that fingerprints resume tokens with fpKey.
// fpKey must be non-empty for production use; tests may use any fixed slice.
func NewRandGenerator(fpKey []byte) *RandGenerator {
	k := make([]byte, len(fpKey))
	copy(k, fpKey)
	return &RandGenerator{fpKey: k}
}

// NewSessionID returns a high-entropy opaque session id independent of material.
func (g *RandGenerator) NewSessionID(ctx context.Context, material EntropyMaterial) (domain.SessionID, error) {
	_ = material
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var buf [sessionIDRandBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("securesession: session id entropy: %w", err)
	}
	return domain.SessionID(base64.RawURLEncoding.EncodeToString(buf[:])), nil
}

// NewResumeToken returns a random bearer token and its stored fingerprint (never persist the token).
func (g *RandGenerator) NewResumeToken(ctx context.Context, material EntropyMaterial) (domain.ResumeToken, domain.TokenFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return "", domain.TokenFingerprint{}, err
	}
	var raw [resumeTokenRandBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", domain.TokenFingerprint{}, fmt.Errorf("securesession: resume token entropy: %w", err)
	}
	tok := domain.ResumeToken(base64.RawURLEncoding.EncodeToString(raw[:]))
	fp := resumeTokenFingerprint(g.fpKey, []byte(tok), material)
	return tok, fp, nil
}

// resumeTokenFingerprint is HMAC-SHA256 over a versioned context, domain material, and raw token bytes.
func resumeTokenFingerprint(key, token []byte, m EntropyMaterial) domain.TokenFingerprint {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "aip-secure-resume-v1\x00")
	_, _ = io.WriteString(mac, m.PrincipalID)
	mac.Write([]byte{0})
	_, _ = io.WriteString(mac, m.AgentDigest)
	mac.Write([]byte{0})
	_, _ = io.WriteString(mac, m.FirstMessageDigest)
	mac.Write([]byte{0})
	_, _ = mac.Write(token)
	var out domain.TokenFingerprint
	copy(out[:], mac.Sum(nil))
	return out
}

// FingerprintResumeToken computes the stored fingerprint for an existing raw token (tests, resume validation).
func FingerprintResumeToken(key []byte, tok domain.ResumeToken, m EntropyMaterial) domain.TokenFingerprint {
	return resumeTokenFingerprint(key, []byte(tok), m)
}
