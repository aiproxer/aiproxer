package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Model is one inventory entry from GET /models.
type Model struct {
	ID      string
	OwnedBy string
}

// ListModels fetches GET {base}/models with a bounded response body.
func (c *Client) ListModels(ctx context.Context, limit uint32) ([]Model, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("openaicompat: nil context")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req.Header, aipapi.Call{}, "", FlavorChat)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readHTTPError(resp, c.maxBody())
	}
	limited := &io.LimitedReader{R: resp.Body, N: c.maxBody() + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: list models read: %w", err)
	}
	if int64(len(raw)) > c.maxBody() {
		return nil, fmt.Errorf("openaicompat: list models response exceeds %d bytes", c.maxBody())
	}
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("openaicompat: list models json: %w", err)
	}
	out := make([]Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, Model{ID: id, OwnedBy: m.OwnedBy})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	return out, nil
}
