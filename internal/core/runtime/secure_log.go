package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashOpaqueIDForLog returns a short stable hex digest of opaque strings (session ids, user ids) for log attributes.
// It does not replace access control; it limits raw identifier leakage in structured logs.
func HashOpaqueIDForLog(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
