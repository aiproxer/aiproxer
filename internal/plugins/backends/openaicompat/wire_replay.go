package openaicompat

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// wireNoRetryError fences a wire failure that must never rotate credentials
// or trigger another provider attempt: a replay-source failure, a request
// construction failure, or any failure observed after the provider accepted
// the request with HTTP 2xx (output or an irreversible effect may have
// started). It is an internal control signal only and is stripped before the
// error crosses the adapter boundary, so the external error contract is
// identical to a path that never retried.
type wireNoRetryError struct {
	cause error
}

func newWireNoRetryError(cause error) *wireNoRetryError {
	if cause == nil {
		cause = errors.New("openaicompat: wire attempt cannot be retried")
	}
	return &wireNoRetryError{cause: cause}
}

func (e *wireNoRetryError) Error() string {
	return e.cause.Error()
}

func (e *wireNoRetryError) Unwrap() error {
	return e.cause
}

// openWireReplay opens a wire attempt with adapter-local credential rotation
// over exact replayable bytes (requirements 3.4, 3.5, 4.3). Each coordinator
// attempt opens a fresh offset-zero reader from the largebody-owned replay
// factory and closes it once the HTTP client has the response; the shared
// credential coordinator decides rotation with the configured quota-switch
// threshold, exactly as on the canonical paths. The B-leg identity and core
// routing are untouched: all attempts share the single inbound WireOpenRequest.
//
// Only pre-output HTTP failures (*HTTPError, classified 429/auth-invalid)
// advance credentials. Anything observed after HTTP 2xx, or any replay
// failure, returns without another provider request; coordinator terminal
// states map to recoverable pre-output errors for core-owned routing.
func (p WireOpenPrimitives) openWireReplay(ctx context.Context, targetURL string, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	streaming := req.WireRequest.Delivery == aipapi.DeliveryModeStreaming
	// The wire lane has no canonical Call. Record only the bounded final
	// request-byte representation once per wire open, not once per credential
	// attempt; adapters with richer post-transform facts may replace this summary.
	coremetering.ObservePreparedInput(ctx, coremetering.PreparedInputSummary{
		PayloadBytes: req.ContentLength, PayloadBytesPresent: req.ContentLength >= 0,
		MethodRef: "adapter:openaicompat.wire_payload.v1",
	})

	client := p.Client()
	stream, err := openaicred.ExecuteWithCredentialPool(ctx, p.ProviderID, p.Pool, p.QuotaSwitchThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		body, length, openErr := req.Replay.NewReader()
		if openErr != nil {
			return nil, newWireNoRetryError(openErr)
		}
		httpReq, buildErr := NewOutboundRequest(ctx, targetURL, body, length, cred.Secret, streaming, req.Header)
		if buildErr != nil {
			_ = body.Close()
			return nil, newWireNoRetryError(buildErr)
		}
		resp, doErr := client.Do(httpReq)
		// The request body is fully sent once Do returns a response; close
		// the per-attempt reader on every path so source refcounts release
		// even when the attempt fails before any output.
		_ = body.Close()
		if doErr != nil {
			return nil, doErr
		}
		attempt, parseErr := p.ParseAndPeekStream(ctx, resp)
		if parseErr != nil {
			if _, ok := errors.AsType[*HTTPError](parseErr); ok {
				// Bounded pre-output HTTP failure with the response body
				// already drained and closed: safe for coordinator rotation.
				return nil, parseErr
			}
			// Post-2xx stream failure: the provider accepted the full
			// request, so output or an effect may have started. Never
			// rotate credentials or execute the provider request again.
			return nil, newWireNoRetryError(parseErr)
		}
		return attempt, nil
	})
	if err != nil {
		if aipapi.IsRecoverablePreOutput(err) {
			// Coordinator terminal state (transient throttle, quota
			// exhaustion, upstream retryable): keep the classified error
			// for core-owned routing, exactly as on canonical paths.
			return nil, err
		}
		if fenced, ok := errors.AsType[*wireNoRetryError](err); ok {
			return nil, fenced.cause
		}
		return nil, err
	}
	return stream, nil
}
