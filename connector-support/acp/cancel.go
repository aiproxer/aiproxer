package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CancelProfile configures the ordered cancellation notification methods and
// optional requestId and messageId correlation parameters sent to the agent.
type CancelProfile struct {
	// Methods is the ordered list of JSON-RPC methods to try (notifications, no id).
	Methods []string
	// IncludeRequestID adds requestId (prompt JSON-RPC id) to params when true.
	IncludeRequestID bool
	// IncludeMessageID adds messageId from the session/prompt envelope when non-empty.
	IncludeMessageID bool
}

func defaultCancelProfile() CancelProfile {
	return CancelProfile{
		Methods: []string{"session/cancel", "session/stop", "session/end"},
	}
}

func mergeCancelProfile(cfg Config) CancelProfile {
	p := cfg.Cancel
	if len(p.Methods) == 0 {
		p.Methods = defaultCancelProfile().Methods
	}
	return p
}

func (c *client) cancelSession(ctx context.Context, cp CancelProfile, sessionID string, promptRPCID int64, messageID string) error {
	var lastErr error
	for _, method := range cp.Methods {
		method = strings.TrimSpace(method)
		if method == "" {
			continue
		}
		params := map[string]any{"sessionId": sessionID}
		if cp.IncludeRequestID {
			params["requestId"] = promptRPCID
		}
		if cp.IncludeMessageID && strings.TrimSpace(messageID) != "" {
			params["messageId"] = messageID
		}
		pb, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("acp: cancel marshal params: %w", err)
		}
		req := rpcRequest{JSONRPC: "2.0", Method: method, Params: pb}
		body, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("acp: cancel marshal request: %w", err)
		}
		_, errNC := c.t.CallUnary(ctx, body, http.StatusNoContent)
		if errNC == nil {
			return nil
		}
		_, errOK := c.t.CallUnary(ctx, body, http.StatusOK)
		if errOK == nil {
			return nil
		}
		lastErr = errOK
	}
	if lastErr != nil {
		return fmt.Errorf("acp: cancel: %w", lastErr)
	}
	return fmt.Errorf("acp: cancel: no methods configured")
}
