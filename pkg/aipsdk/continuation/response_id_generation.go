package continuation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// NewResponseID returns a high-entropy proxy response ID suitable for a
// continuation store. The SDK owns this protocol-neutral primitive so frontend
// plugins do not depend on an internal core implementation.
func NewResponseID(ctx context.Context) (ResponseID, error) {
	if ctx == nil {
		return "", context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var raw [MinResponseIDEntropyBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("continuation: response id entropy: %w", err)
	}
	return ResponseID(ResponseIDPrefix + base64.RawURLEncoding.EncodeToString(raw[:])), nil
}
