package acp

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// resolveSessionID returns an existing ACP session id from Call.Extensions, or allocates
// a new session via session/new when the extension is absent.
func resolveSessionID(ctx context.Context, c *client, call *aipapi.Call, hp HandshakeProfile) (string, error) {
	if sid := sessionIDFromExtensions(call); sid != "" {
		return sid, nil
	}
	sid, err := c.sessionNew(ctx, hp)
	if err != nil {
		return "", fmt.Errorf("acp: session: %w", err)
	}
	return sid, nil
}
