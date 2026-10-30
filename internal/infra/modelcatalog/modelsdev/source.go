package modelsdev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

// maxCatalogHTTPResponseBytes caps the catalog snapshot GET body. This bounds memory use from a
// malicious or buggy upstream regardless of configured client timeouts (defense-in-depth).
const maxCatalogHTTPResponseBytes = 64 << 20

// ErrExternalUpdatesDisabled is returned by [HTTPSource.Fetch] when external catalog fetches are disabled.
var ErrExternalUpdatesDisabled = errors.New("modelsdev: external catalog updates are disabled")

// HTTPSource implements [modelcatalog.SnapshotSource] using GET requests (no body, no user content).
type HTTPSource struct {
	client       *http.Client
	url          string
	enabled      bool
	fetchTimeout time.Duration
}

var _ modelcatalog.SnapshotSource = (*HTTPSource)(nil)

// NewHTTPSource returns a snapshot source. When enabled is false, [HTTPSource.Fetch] returns
// [ErrExternalUpdatesDisabled] without performing network I/O.
// fetchTimeout, when positive, applies context.WithTimeout only if ctx has no deadline before Do+read.
func NewHTTPSource(client *http.Client, url string, enabled bool, fetchTimeout time.Duration) *HTTPSource {
	if client == nil {
		client = http.DefaultClient
	}
	return &HTTPSource{client: client, url: url, enabled: enabled, fetchTimeout: fetchTimeout}
}

// Fetch implements [modelcatalog.SnapshotSource].
func (h *HTTPSource) Fetch(ctx context.Context) (modelcatalog.Snapshot, error) {
	var zero modelcatalog.Snapshot
	if !h.enabled {
		return zero, ErrExternalUpdatesDisabled
	}
	if h.url == "" {
		return zero, errors.New("modelsdev source: empty url")
	}
	opCtx := ctx
	if h.fetchTimeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			opCtx, cancel = context.WithTimeout(ctx, h.fetchTimeout)
			defer cancel()
		}
	}
	req, err := http.NewRequestWithContext(opCtx, http.MethodGet, h.url, nil)
	if err != nil {
		return zero, fmt.Errorf("modelsdev source: request: %w", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("modelsdev source: http: %w", err)
	}
	if resp == nil || resp.Body == nil {
		return zero, errors.New("modelsdev source: empty http response")
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitedReader{R: resp.Body, N: maxCatalogHTTPResponseBytes + 1}
	body, err := io.ReadAll(&limited)
	if err != nil {
		return zero, fmt.Errorf("modelsdev source: read body: %w", err)
	}
	if int64(len(body)) > maxCatalogHTTPResponseBytes {
		return zero, fmt.Errorf("modelsdev source: response body exceeds %d bytes", maxCatalogHTTPResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("modelsdev source: http status %d", resp.StatusCode)
	}
	return ParseSnapshot(body, time.Now().UTC())
}
