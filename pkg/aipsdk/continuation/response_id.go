package continuation

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	// MinResponseIDEntropyBytes is the minimum raw entropy required for proxy IDs.
	MinResponseIDEntropyBytes = 16
	// MaxResponseIDLength is the maximum allowed string length for proxy continuation IDs.
	MaxResponseIDLength = 512
	// ResponseIDPrefix is the stable external prefix for proxy-issued response IDs.
	ResponseIDPrefix = "resp_"
)

// ResponseID is a proxy-issued opaque continuation identifier.
type ResponseID string

// IsZero reports whether the ID is unset.
func (id ResponseID) IsZero() bool { return id == "" }

// String returns the wire-safe ID string.
func (id ResponseID) String() string { return string(id) }

// Validate checks prefix and minimum encoded entropy for externally supplied IDs.
func (id ResponseID) Validate() error {
	if id.IsZero() {
		return fmt.Errorf("continuation: empty response id")
	}
	s := string(id)
	if len(s) > MaxResponseIDLength {
		return fmt.Errorf("continuation: response id length exceeds limit")
	}
	if !strings.HasPrefix(s, ResponseIDPrefix) {
		return fmt.Errorf("continuation: response id missing %q prefix", ResponseIDPrefix)
	}
	payload := strings.TrimPrefix(s, ResponseIDPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || len(decoded) < MinResponseIDEntropyBytes {
		return fmt.Errorf("continuation: response id entropy too short")
	}
	return nil
}
