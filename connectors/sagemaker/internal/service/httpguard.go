package service

import (
	"fmt"
	"io"
	"net/http"
)

// httpDoer is the minimal smithy/AWS HTTP client contract:
// Do(*http.Request) (*http.Response, error).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// boundedResponseHTTPClient wraps the AWS HTTP client used by
// DefaultAWSClientFactory so oversized InvokeEndpoint responses fail closed
// BEFORE the SDK deserializer runs. The sagemakerruntime deserializer does
// buf.Grow(int(contentLength)) on the declared Content-Length and then
// buffers the whole body, so a large declared length alone can force a huge
// allocation before the len(resp.Body) check in Open ever runs.
type boundedResponseHTTPClient struct {
	inner httpDoer
}

func (c *boundedResponseHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Fail fast on a declared-huge Content-Length without reading the body.
	if resp.ContentLength > int64(maxSageMakerResponseBytes) {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("sagemaker: response Content-Length %d bytes exceeds limit %d bytes", resp.ContentLength, maxSageMakerResponseBytes)
	}
	// Defensive clamp so a declared length can never size the SDK's
	// pre-deserialization buffer beyond max+1. Negative (unknown/chunked)
	// lengths are left as-is and enforced by the body cap below.
	if resp.ContentLength >= 0 && resp.ContentLength > int64(maxSageMakerResponseBytes)+1 {
		resp.ContentLength = int64(maxSageMakerResponseBytes) + 1
	}
	if resp.Body != nil {
		resp.Body = &cappedResponseBody{rc: resp.Body, remaining: int64(maxSageMakerResponseBytes) + 1}
	}
	return resp, nil
}

// cappedResponseBody is an error-on-overflow reader capped at
// maxSageMakerResponseBytes+1 bytes. A body longer than the cap fails with an
// explicit exceeds-limit error instead of being silently truncated (a
// truncated JSON document must never reach the parser as if it were whole).
// Bodies at or under the limit pass through byte-identical, including EOF.
type cappedResponseBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (b *cappedResponseBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, fmt.Errorf("sagemaker: response body exceeds limit %d bytes", maxSageMakerResponseBytes)
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *cappedResponseBody) Close() error { return b.rc.Close() }
