package openaicred

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
)

// credentialFingerprint returns an opaque, non-secret identity of the
// credential and account that served one attempt. Only operator-facing
// identifiers and non-secret remote identity fields participate; the secret is
// never read. The value is an equality token for the core switch authority and
// is never logged, persisted, emitted on a usage event, or published to the
// prompt-cache collapse diagnostic.
func credentialFingerprint(cred credpool.Credential) string {
	fields := [...]string{
		strings.TrimSpace(cred.ID),
		strings.TrimSpace(cred.RemoteOrgID),
		strings.TrimSpace(cred.RemoteProjectID),
		strings.TrimSpace(cred.RemoteWorkspaceID),
		strings.TrimSpace(cred.RemoteAccountID),
		strings.TrimSpace(cred.RemoteRegion),
	}
	hasIdentity := false
	h := sha256.New()
	_, _ = h.Write([]byte("aiproxer/credential-identity/v1\x00"))
	for _, field := range fields {
		if field != "" {
			hasIdentity = true
		}
		writeCredentialFingerprintField(h, field)
	}
	if !hasIdentity {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

type credentialFingerprintWriter interface {
	Write([]byte) (int, error)
}

func writeCredentialFingerprintField(w credentialFingerprintWriter, value string) {
	var length [8]byte
	n := uint64(len(value))
	for i := range length {
		length[i] = byte(n >> (8 * (7 - i)))
	}
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(value))
}
