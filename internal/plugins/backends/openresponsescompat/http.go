package openresponsescompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/transporterr"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// maxErrorSnippetBytes bounds how much of a non-OK body is drained before close;
// the snippet is never echoed into errors (secrets/body must not leak).
const maxErrorSnippetBytes = 8192

// resolveCreateEndpoint joins the validated base URL to the pinned create path
// (POST {base}/responses) using the shared deterministic separator policy and
// rejects path traversal segments.
func resolveCreateEndpoint(base string) (string, error) {
	return resolveEndpoint(base, "/responses")
}

// resolveCompactEndpoint joins the validated base URL to the pinned compact path
// (POST {base}/responses/compact) with the same separator/traversal policy.
func resolveCompactEndpoint(base string) (string, error) {
	return resolveEndpoint(base, "/responses/compact")
}

// resolveEndpoint joins a validated base URL to a pinned profile path suffix
// using the shared deterministic separator policy and rejects path traversal
// segments in the configured base path.
func resolveEndpoint(base, suffix string) (string, error) {
	d, err := endpoint.ParseBaseURL(base)
	if err != nil {
		return "", err
	}
	if pathHasTraversal(d.Path()) {
		return "", fmt.Errorf("endpoint path must not contain path traversal segments")
	}
	return strings.TrimRight(d.BaseURL(), "/") + suffix, nil
}

// pathHasTraversal reports whether a URL path contains a ".." segment, either
// literal or percent-encoded.
func pathHasTraversal(path string) bool {
	if segmentTraverses(path) {
		return true
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return false
	}
	return segmentTraverses(decoded)
}

func segmentTraverses(path string) bool {
	for seg := range strings.SplitSeq(path, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func isApplicationJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.EqualFold(mt, "application/json")
}

// readHTTPBodyLimited reads at most maxBytes bytes and always closes the body. An
// oversized body is drained (bounded) and rejected so the connection stays
// reusable without unbounded allocation or an unbounded read from a
// misbehaving upstream that streams forever.
func readHTTPBodyLimited(r io.ReadCloser, maxBytes int) (b []byte, err error) {
	defer func() {
		if cerr := r.Close(); cerr != nil {
			closeErr := fmt.Errorf("openresponsescompat: close response body: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()
	lr := io.LimitReader(r, int64(maxBytes)+1)
	b, err = io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if len(b) > maxBytes {
		// Best-effort drain capped at maxBytes bytes; the deferred Close aborts any
		// remaining body instead of blocking on an endless upstream stream.
		_, _ = io.CopyN(io.Discard, r, int64(maxBytes))
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return b, nil
}

func sanitizeHTTPTransportError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	copyErr := *urlErr
	if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		copyErr.URL = parsed.String()
	} else {
		copyErr.URL = ""
	}
	return &copyErr
}

// doNonStreaming performs the context-aware POST JSON create and applies strict
// status/content-type/body limits. It returns only on a complete bounded body;
// the response body is always closed. Errors never include the response body or
// resolved secrets.
func doNonStreaming(ctx context.Context, hc *http.Client, endpointURL string, body []byte, apiKey string, maxBodyBytes int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openresponsescompat: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := hc.Do(req)
	if err != nil {
		safeErr := sanitizeHTTPTransportError(err)
		if transporterr.IsRetryable(safeErr) {
			return nil, aipapi.RecoverablePreOutputError(fmt.Errorf("openresponsescompat: create request: %w", safeErr))
		}
		return nil, fmt.Errorf("openresponsescompat: create request failed: %w", safeErr)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		return nil, classifyHTTPStatus(resp.StatusCode, resp.Header)
	}
	if !isApplicationJSON(resp.Header.Get("Content-Type")) {
		_, _ = readHTTPBodyLimited(resp.Body, maxErrorSnippetBytes)
		return nil, fmt.Errorf("%s: %w: unexpected content-type %q", ID, ErrMalformedResponse, resp.Header.Get("Content-Type"))
	}
	return readHTTPBodyLimited(resp.Body, maxBodyBytes)
}
