package acp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// JSON-RPC unary responses should be small; cap read size to avoid unbounded
// allocation from a buggy or hostile ACP endpoint (aligned with frontend
// request body caps in spirit).
const maxUnaryHTTPResponseBytes = 8 << 20

// Error and non-OK response bodies only need a short snippet for diagnostics.
const maxErrorSnippetBytes = 8192

func readHTTPBodyLimited(r io.ReadCloser, max int) (b []byte, err error) {
	defer func() {
		if cerr := r.Close(); cerr != nil {
			closeErr := fmt.Errorf("acp: close http body: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	lr := io.LimitReader(r, int64(max)+1)
	b, err = io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("acp: read http body: %w", err)
	}
	if len(b) > max {
		exceed := fmt.Errorf("acp: response body exceeds %d bytes", max)
		if _, copyErr := io.Copy(io.Discard, r); copyErr != nil {
			return nil, errors.Join(exceed, fmt.Errorf("acp: discard oversized body: %w", copyErr))
		}
		return nil, exceed
	}
	return b, nil
}

// Transport abstracts how JSON-RPC is exchanged with an ACP agent. The HTTP
// implementation matches internal/refbackend/acp; a future stdio Conn can implement
// the same surface for newline-delimited JSON-RPC.
type Transport interface {
	// CallUnary posts one JSON-RPC request and reads the full response body when
	// status matches expectStatus (typically 200). For 204, body may be empty.
	// For stdio transports, expectStatus is ignored.
	CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error)
	// CallPromptStream posts session/prompt and returns the response body stream (NDJSON).
	CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error)
	// SendJSONRPC posts an arbitrary JSON-RPC object (e.g. response to an inbound
	// server request during a prompt on HTTP transports that use POST per message).
	SendJSONRPC(ctx context.Context, body []byte) error
	// Close releases transport resources. HTTP transports are no-ops; stdio transports
	// kill the subprocess and close pipes. Safe to call multiple times.
	Close() error
}

// httpTransport implements Transport over POST {origin}/v1/acp (batch HTTP).
type httpTransport struct {
	endpoint string
	hc       *http.Client
}

// newHTTPTransport builds an HTTP [Transport] for POST {origin}/v1/acp.
// HTTPClient must be non-nil; callers own dial/TLS/idle policy.
func newHTTPTransport(baseURL string, hc *http.Client) (*httpTransport, error) {
	u := strings.TrimSpace(baseURL)
	if u == "" {
		return nil, fmt.Errorf("acp: BaseURL is required")
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("acp: BaseURL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("acp: BaseURL must include scheme and host")
	}
	if hc == nil {
		return nil, fmt.Errorf("acp: HTTPClient is required (caller-owned policy)")
	}
	u2 := *parsed
	u2.Path = strings.TrimSuffix(u2.Path, "/") + "/v1/acp"
	u2.RawQuery = ""
	u2.Fragment = ""
	return &httpTransport{endpoint: u2.String(), hc: hc}, nil
}

func (t *httpTransport) CallUnary(ctx context.Context, body []byte, expectStatus int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("acp: unary http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acp: unary http do: %w", err)
	}
	if resp.StatusCode != expectStatus {
		snippet, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return nil, fmt.Errorf("acp: unary http error body: %w", err)
		}
		return nil, &httpStatusError{
			Op:     "unary",
			Status: resp.StatusCode,
			Detail: strings.TrimSpace(string(snippet)),
		}
	}
	return readHTTPBodyLimited(resp.Body, maxUnaryHTTPResponseBytes)
}

func (t *httpTransport) CallPromptStream(ctx context.Context, body []byte) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("acp: prompt stream http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("acp: prompt stream http do: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return nil, fmt.Errorf("acp: prompt stream http error body: %w", err)
		}
		return nil, &httpStatusError{
			Op:     "session/prompt",
			Status: resp.StatusCode,
			Detail: strings.TrimSpace(string(b)),
		}
	}
	return resp.Body, nil
}

func (t *httpTransport) Close() error { return nil }

func (t *httpTransport) SendJSONRPC(ctx context.Context, body []byte) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("acp: send jsonrpc http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return fmt.Errorf("acp: send jsonrpc http do: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, err := readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		if err != nil {
			return fmt.Errorf("acp: send jsonrpc http error body: %w", err)
		}
		return &httpStatusError{
			Op:     "SendJSONRPC",
			Status: resp.StatusCode,
			Detail: strings.TrimSpace(string(b)),
		}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			closeErr := fmt.Errorf("acp: close send jsonrpc body: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	// Discard the success body bounded by maxErrorSnippetBytes so a hostile or
	// buggy endpoint cannot force unbounded allocation. The body is not consumed
	// past this cap; resp.Body.Close() handles any remainder.
	if _, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorSnippetBytes)); copyErr != nil {
		err = fmt.Errorf("acp: discard send jsonrpc body: %w", copyErr)
	}
	return err
}
