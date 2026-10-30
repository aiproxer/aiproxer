package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
)

type client struct {
	t      Transport
	log    *slog.Logger
	nextID atomic.Int64
}

func newClient(baseURL string, hc *http.Client, log *slog.Logger) (*client, error) {
	t, err := newHTTPTransport(baseURL, hc)
	if err != nil {
		return nil, err
	}
	return &client{t: t, log: log}, nil
}

// newClientFromTransport constructs a client from any Transport implementation,
// enabling stdio and future transport variants to share the same client logic
// (handshake, session/prompt, cancel) without re-implementing the HTTP transport.
func newClientFromTransport(t Transport, log *slog.Logger) *client {
	return &client{t: t, log: log}
}

func (c *client) rpcID() int64 {
	return c.nextID.Add(1)
}

func (c *client) sessionPrompt(ctx context.Context, params map[string]any, rpcID int64) (io.ReadCloser, error) {
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("acp: session/prompt marshal params: %w", err)
	}
	reqBody := rpcRequest{
		JSONRPC: "2.0",
		ID:      jsonRPCNumericID(rpcID),
		Method:  "session/prompt",
		Params:  pb,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("acp: session/prompt marshal request: %w", err)
	}
	rc, err := c.t.CallPromptStream(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("acp: session/prompt call: %w", err)
	}
	return rc, nil
}
