package openaicompat

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// attemptCacheTargetFingerprint returns an opaque, content-free identity of the
// effective downstream cache target for one OpenAI-compatible attempt: the
// configured base URL, the protocol flavor, and the selected credential's
// non-secret region when one is configured.
//
// The value is an equality token only. It is never logged, emitted on a usage
// sample, or published to the prompt-cache collapse diagnostic; the core
// authority compares it in-process and publishes a fixed label at most.
func attemptCacheTargetFingerprint(baseURL, flavor, region string) string {
	baseURL = strings.TrimSpace(baseURL)
	flavor = strings.TrimSpace(flavor)
	region = strings.TrimSpace(region)
	if baseURL == "" && flavor == "" && region == "" {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte("aiproxer/openai-cache-target/v1\x00"))
	writeFingerprintField(h, baseURL)
	writeFingerprintField(h, flavor)
	writeFingerprintField(h, region)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

type fingerprintWriter interface {
	Write([]byte) (int, error)
}

func writeFingerprintField(w fingerprintWriter, value string) {
	var length [8]byte
	n := uint64(len(value))
	for i := range length {
		length[i] = byte(n >> (8 * (7 - i)))
	}
	_, _ = w.Write(length[:])
	_, _ = w.Write([]byte(value))
}
