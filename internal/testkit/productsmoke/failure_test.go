//go:build productsmoke

package productsmoke

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	primaryBackendID  = "primary"
	fallbackBackendID = "fallback"

	failureCanonical       = "failure/chat"
	failureNativePrimary   = "failure-native-primary"
	failureNativeFallback  = "failure-native-fallback"
	failureRoute           = primaryBackendID + ":" + failureCanonical + "|" + fallbackBackendID + ":" + failureCanonical
	failurePrimaryContent  = "the primary provider answered."
	failureFallbackContent = "the fallback provider answered."
	failureRecoveryContent = "the same proxy answered after the failure."

	// cancelObservationBound bounds how long the fake provider may take to see
	// its upstream request cancelled after the client cancels.
	cancelObservationBound = 10 * time.Second
	// cancelReadBound bounds reading the first streamed event before the client
	// cancels, so a silent proxy fails the journey instead of hanging it.
	cancelReadBound = 15 * time.Second
)

// failureScript is the scripted successful answer for one failure-journey
// backend.
func failureScript(content string, usage fakeUsage) fakeScript {
	return fakeScript{
		Model:  failureNativePrimary,
		Deltas: []string{content},
		Usage:  usage,
	}
}

// fallbackScript is the scripted fallback answer.
func fallbackScript() fakeScript {
	return fakeScript{
		Model:  failureNativeFallback,
		Deltas: []string{failureFallbackContent},
		Usage:  fakeUsage{PromptTokens: 17, CompletionTokens: 5, TotalTokens: 22},
	}
}

// startFailureSUT starts the proxy with the ordered primary|fallback route. The
// readiness backend is appended by startSUT and never appears in either
// journey provider's request log.
func startFailureSUT(t *testing.T, canary string, primary, fallback *fakeProvider) *sut {
	t.Helper()
	return startSUT(t, sutConfig{
		Canary:    canary,
		Frontends: []string{"openai-legacy"},
		Backends: []backendWiring{
			{
				ID:      primaryBackendID,
				Kind:    kindOpenAILegacyCompatible,
				BaseURL: primary.OpenAILegacyBaseURL(),
				Models:  []modelWiring{{Canonical: failureCanonical, Native: failureNativePrimary}},
			},
			{
				ID:      fallbackBackendID,
				Kind:    kindOpenAILegacyCompatible,
				BaseURL: fallback.OpenAILegacyBaseURL(),
				Models:  []modelWiring{{Canonical: failureCanonical, Native: failureNativeFallback}},
			},
		},
		DefaultRoute: failureRoute,
		MaxAttempts:  2,
	})
}

// failureRequestBody is one normal client chat request resolved through the
// default primary|fallback route.
func failureRequestBody(stream bool) []byte {
	raw, err := json.Marshal(chatBody{
		Model:    failureCanonical,
		Stream:   stream,
		Messages: smokeMessages(),
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// writeFailureProviderLogs stores both providers' request logs under evidence so
// a failure report can still show the provider the failure context omitted.
func writeFailureProviderLogs(h *sut, primary, fallback *fakeProvider) {
	h.evidence.write("primary-requests.txt", primary.RequestLog())
	h.evidence.write("fallback-requests.txt", fallback.RequestLog())
}

// TestProduct_Failure proves the running proxy fails over only before output,
// ends a post-output failure honestly without a second provider, propagates a
// client cancellation upstream, and serves a new request afterwards.
func TestProduct_Failure(t *testing.T) {
	t.Run("pre_output_fallback", testPreOutputFallback)
	t.Run("post_output_no_retry", testPostOutputNoRetry)
	t.Run("client_cancel", testClientCancel)
}

func testPreOutputFallback(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	primary := newOpenAILegacyProvider(t, canary, failureScript(failurePrimaryContent,
		fakeUsage{PromptTokens: 11, CompletionTokens: 4, TotalTokens: 15}))
	primary.SetFault(faultHTTPErrorBeforeBody, 0)
	fallbackAnswer := fallbackScript()
	fallback := newOpenAILegacyProvider(t, canary, fallbackAnswer)
	h := startFailureSUT(t, canary, primary, fallback)

	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	failures := failureContext{sut: h, provider: fallback, wire: wire}
	writeFailureProviderLogs(h, primary, fallback)
	if err := checkOpenAIStream(wire, fallbackAnswer, h.evidence.redact); err != nil {
		failf(t, failures, "pre_output_fallback: the fallback did not serve a successful client turn: %v", err)
	}
	if got := primary.RequestCount(); got != 1 {
		failf(t, failures, "primary provider received %d request(s), want exactly 1 failed attempt", got)
	}
	if got := fallback.RequestCount(); got != 1 {
		failf(t, failures, "fallback provider received %d request(s), want exactly 1 served attempt", got)
	}
	fallbackRequest := singleProviderRequest(t, failures, fallback)
	if err := checkTranslatedOpenAIChat(fallbackRequest, failureNativeFallback, openAIChatPath, smokeMessages(), nil, h.evidence.redact); err != nil {
		failf(t, failures, "pre_output_fallback: the fallback did not receive the translated client request: %v", err)
	}
	var fallbackBody translatedChat
	if err := json.Unmarshal(fallbackRequest.Body, &fallbackBody); err != nil {
		failf(t, failures, "pre_output_fallback: the fallback request body is not JSON: %v", err)
	}
	if len(fallbackBody.Tools) != 0 {
		failf(t, failures, "pre_output_fallback: the fallback request carried %d tool declaration(s), want none", len(fallbackBody.Tools))
	}
	if request := primary.Requests()[0]; !request.KeyMatched {
		failf(t, failures, "the failed primary request did not carry the canary credential")
	}

	// The same process must serve a new request. Clearing the pre-output fault
	// makes the primary the deterministic winner; a fallback request would mean
	// the journey observed the wrong provider.
	primary.SetFault(faultNone, 0)
	primaryScript := failureScript(failurePrimaryContent,
		fakeUsage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16})
	primary.SetScript(primaryScript)
	followUp := doJSON(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	followUpFailures := failureContext{sut: h, provider: primary, wire: followUp}
	if err := checkOpenAIStream(followUp, primaryScript, h.evidence.redact); err != nil {
		failf(t, followUpFailures, "pre_output_fallback follow-up: %v", err)
	}
	if got := primary.RequestCount(); got != 2 {
		failf(t, followUpFailures, "primary provider received %d request(s), want the failed attempt plus the follow-up", got)
	}
	if got := fallback.RequestCount(); got != 1 {
		failf(t, followUpFailures, "fallback provider received %d request(s), want only the first request's fallback", got)
	}
	summary := fmt.Sprintf("primary=1 fallback=1 follow_up_primary=2 follow_up_fallback=1 client_status=%d\n", wire.Status)
	h.evidence.write("pre-output-fallback.txt", summary)
	writeFailureProviderLogs(h, primary, fallback)
	t.Logf("pre-output fallback passed: primary attempted once, fallback served, follow-up served by primary; evidence %s", h.evidence.dir)
}

func testPostOutputNoRetry(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	primaryScript := failureScript(failurePrimaryContent,
		fakeUsage{PromptTokens: 13, CompletionTokens: 6, TotalTokens: 19})
	primary := newOpenAILegacyProvider(t, canary, primaryScript)
	// Two stream events reach the client (the role chunk and one content
	// delta); the connection aborts before the next event.
	primary.SetFault(faultAbortAfterEvents, 2)
	fallbackAnswer := fallbackScript()
	fallback := newOpenAILegacyProvider(t, canary, fallbackAnswer)
	h := startFailureSUT(t, canary, primary, fallback)

	wire := doJSON(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	failures := failureContext{sut: h, provider: primary, wire: wire}
	writeFailureProviderLogs(h, primary, fallback)
	if err := checkOpenAILegacyStreamError(wire, primaryScript.content(), h.evidence.redact); err != nil {
		failf(t, failures, "post_output_no_retry: the client did not receive a protocol-legal error after output: %v", err)
	}
	if got := fallback.RequestCount(); got != 0 {
		failf(t, failureContext{sut: h, provider: fallback, wire: wire},
			"post_output_no_retry: the fallback provider received %d request(s) after output was committed, want 0", got)
	}
	if got := primary.RequestCount(); got != 1 {
		failf(t, failures, "post_output_no_retry: primary provider received %d request(s), want exactly 1", got)
	}

	// The same process serves a new request after the stream failure.
	primary.SetFault(faultNone, 0)
	followUp := doJSON(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	followUpFailures := failureContext{sut: h, provider: primary, wire: followUp}
	if err := checkOpenAIStream(followUp, primaryScript, h.evidence.redact); err != nil {
		failf(t, followUpFailures, "post_output_no_retry follow-up: %v", err)
	}
	if got := primary.RequestCount(); got != 2 {
		failf(t, followUpFailures, "primary provider received %d request(s), want the aborted attempt plus the follow-up", got)
	}
	if got := fallback.RequestCount(); got != 0 {
		failf(t, followUpFailures, "fallback provider received %d request(s), want 0 for both the failed and the follow-up request", got)
	}
	summary := fmt.Sprintf("primary=1 fallback=0 follow_up_primary=2 follow_up_fallback=0 client_status=%d\n", wire.Status)
	h.evidence.write("post-output-no-retry.txt", summary)
	h.evidence.write("client-wire.txt", wire.render(h.evidence.redact))
	writeFailureProviderLogs(h, primary, fallback)
	t.Logf("post-output no-retry passed: client received an error terminal, fallback untouched, follow-up served; evidence %s", h.evidence.dir)
}

func testClientCancel(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	primaryScript := failureScript(failurePrimaryContent,
		fakeUsage{PromptTokens: 14, CompletionTokens: 6, TotalTokens: 20})
	primary := newOpenAILegacyProvider(t, canary, primaryScript)
	primary.SetFault(faultStallUntilCancelled, 0)
	fallbackAnswer := fallbackScript()
	fallback := newOpenAILegacyProvider(t, canary, fallbackAnswer)
	h := startFailureSUT(t, canary, primary, fallback)

	observed, err := cancelAfterFirstStreamEvent(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	failures := failureContext{sut: h, provider: primary, wire: observed.wire}
	writeFailureProviderLogs(h, primary, fallback)
	if err != nil {
		failf(t, failures, "client_cancel: cancel the streaming client after its first event: %v", err)
	}
	select {
	case <-primary.Cancelled():
	case <-time.After(cancelObservationBound):
		failf(t, failures, "client_cancel: the provider did not observe its upstream request cancelled within %s after the client cancelled (first event %q)", cancelObservationBound, excerpt(h.evidence.redact, observed.firstEvent, wireFailureDetailExcerpt))
	}
	cancelledAt, ok := primary.CancelledAt()
	if !ok {
		failf(t, failures, "client_cancel: the provider observed cancellation but recorded no time")
	}
	if elapsed := cancelledAt.Sub(observed.cancelAt); elapsed < 0 || elapsed > cancelObservationBound {
		failf(t, failures, "client_cancel: provider cancellation was observed at %s, want within %s of the client cancel", elapsed, cancelObservationBound)
	}
	if got := fallback.RequestCount(); got != 0 {
		failf(t, failureContext{sut: h, provider: fallback, wire: observed.wire}, "client_cancel: the fallback provider received %d request(s) after a client cancellation, want 0", got)
	}
	if got := primary.RequestCount(); got != 1 {
		failf(t, failures, "client_cancel: primary provider received %d request(s), want exactly the cancelled one", got)
	}

	// A new request through the same process must succeed.
	primary.SetFault(faultNone, 0)
	recoveryScript := failureScript(failureRecoveryContent,
		fakeUsage{PromptTokens: 15, CompletionTokens: 8, TotalTokens: 23})
	primary.SetScript(recoveryScript)
	followUp := doJSON(t.Context(), h.BaseURL()+openAIChatPath, failureRequestBody(true))
	followUpFailures := failureContext{sut: h, provider: primary, wire: followUp}
	if err := checkOpenAIStream(followUp, recoveryScript, h.evidence.redact); err != nil {
		failf(t, followUpFailures, "client_cancel follow-up: %v", err)
	}
	if got := primary.RequestCount(); got != 2 {
		failf(t, followUpFailures, "primary provider received %d request(s), want the cancelled attempt plus the follow-up", got)
	}
	if got := fallback.RequestCount(); got != 0 {
		failf(t, followUpFailures, "fallback provider received %d request(s), want 0 for both the cancelled request and the follow-up", got)
	}
	summary := fmt.Sprintf("cancel_observed_ms=%d fallback=0 follow_up_primary=2 follow_up_fallback=0 client_status=%d client_capture=partial-intentional-cancel\n",
		cancelledAt.Sub(observed.cancelAt).Milliseconds(), observed.wire.Status)
	h.evidence.write("client-cancel.txt", summary)
	h.evidence.write("client-wire.txt", observed.wire.render(h.evidence.redact))
	writeFailureProviderLogs(h, primary, fallback)
	t.Logf("client cancel passed: provider observed cancellation %s after the client cancel; partial client transcript in %s",
		cancelledAt.Sub(observed.cancelAt).Round(time.Millisecond), h.evidence.dir)
}

// checkOpenAILegacyStreamError proves the client stream ended with the OpenAI
// legacy error convention: exactly the scripted content, then one error frame,
// then [DONE], with no successful finish_reason terminal.
func checkOpenAILegacyStreamError(wire *clientWire, wantContent string, redact redactor) error {
	if wire.Err != nil {
		return fmt.Errorf("client exchange failed instead of receiving the protocol error: %w", wire.Err)
	}
	if wire.Status != http.StatusOK {
		return fmt.Errorf("status %d, want 200 with an in-stream error; body: %s", wire.Status, excerpt(redact, string(wire.Body), wireFailureBodyExcerpt))
	}
	if contentType := wire.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		return fmt.Errorf("content type %q, want text/event-stream", contentType)
	}
	frames, err := parseSSEFrames(wire.Body)
	if err != nil {
		return fmt.Errorf("parse SSE: %w", err)
	}
	payloads := dataPayloads(frames)
	if len(payloads) < 2 {
		return fmt.Errorf("observed %d SSE data event(s), want content, an error frame and [DONE]", len(payloads))
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		return fmt.Errorf("stream ended with %q, want the [DONE] terminal", excerpt(redact, last, wireFailureDetailExcerpt))
	}
	var (
		content   strings.Builder
		sawError  bool
		errorSeen int
	)
	for i, payload := range payloads[:len(payloads)-1] {
		var probe struct {
			Error *struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &probe); err != nil {
			return fmt.Errorf("event %d is not JSON: %v; payload: %s", i, err, excerpt(redact, payload, wireFailureDetailExcerpt))
		}
		if probe.Error != nil {
			if probe.Error.Message == "" || probe.Error.Type == "" {
				return fmt.Errorf("error frame at event %d carries an empty message or type", i)
			}
			if sawError {
				return fmt.Errorf("second error frame at event %d after event %d", i, errorSeen)
			}
			sawError = true
			errorSeen = i
			if i != len(payloads)-2 {
				return fmt.Errorf("error frame at event %d is not the last event before [DONE]", i)
			}
			continue
		}
		if sawError {
			return fmt.Errorf("event %d followed the error frame at event %d", i, errorSeen)
		}
		for _, choice := range probe.Choices {
			if choice.FinishReason != nil {
				return fmt.Errorf("successful finish_reason %q at event %d before the error", *choice.FinishReason, i)
			}
			content.WriteString(choice.Delta.Content)
		}
	}
	if !sawError {
		return fmt.Errorf("no error frame observed in %d event(s)", len(payloads)-1)
	}
	if got := content.String(); got != wantContent {
		return fmt.Errorf("client content before the error %q, want the committed scripted content %q", excerpt(redact, got, wireFailureBodyExcerpt), wantContent)
	}
	return nil
}

// cancelledStream is what one deliberately cancelled streaming exchange
// observed: when the client cancelled, the first dispatched data payload, and
// the client-side transcript captured up to that cancellation.
type cancelledStream struct {
	cancelAt   time.Time
	firstEvent string
	wire       *clientWire
}

// cancelAfterFirstStreamEvent posts a streaming request, reads until the first
// dispatched SSE frame, then cancels the client context and closes the body. The
// returned transcript keeps the request, the observed status and headers, every
// byte read before the intentional cancellation, and any HTTP or read error, so a
// failed journey can render the same client witness as an uncancelled one. The
// deliberately stalled response is never drained to EOF: reading stops at the
// first frame, and the capture is bounded by clientBodyLimit.
func cancelAfterFirstStreamEvent(parent context.Context, url string, body []byte) (cancelledStream, error) {
	observed := cancelledStream{wire: &clientWire{Method: http.MethodPost, URL: url, RequestBody: body}}
	ctx, cancel := context.WithTimeout(parent, cancelReadBound)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		observed.wire.Err = err
		return observed, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{Timeout: cancelReadBound}).Do(req)
	if err != nil {
		observed.wire.Err = err
		return observed, err
	}
	defer func() { _ = resp.Body.Close() }()
	observed.wire.Status = resp.StatusCode
	observed.wire.Header = resp.Header.Clone()
	if resp.StatusCode != http.StatusOK {
		// The response was received but is not a stream. Keep its bounded body
		// so an early HTTP failure still has a client-side witness.
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, clientBodyLimit+1))
		if len(raw) > clientBodyLimit {
			raw = raw[:clientBodyLimit]
			readErr = fmt.Errorf("%w: the body exceeded the %d-byte capture bound; the unread suffix was never inspected", errClientCaptureIncomplete, clientBodyLimit)
		} else if readErr != nil {
			readErr = fmt.Errorf("read the error response body: %w", readErr)
		}
		observed.wire.Body = raw
		observed.wire.Err = readErr
		return observed, fmt.Errorf("status %d, want 200", resp.StatusCode)
	}
	reader := bufio.NewReader(io.LimitReader(resp.Body, clientBodyLimit+1))
	var captured bytes.Buffer
	for {
		line, lineErr := reader.ReadString('\n')
		if captured.Len()+len(line) > clientBodyLimit {
			observed.wire.Body = captured.Bytes()
			observed.wire.Err = fmt.Errorf("%w: the body exceeded the %d-byte capture bound before the first frame ended; the unread suffix was never inspected", errClientCaptureIncomplete, clientBodyLimit)
			return observed, observed.wire.Err
		}
		captured.WriteString(line)
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			observed.firstEvent = strings.TrimSpace(data)
		}
		if line == "\n" {
			break
		}
		if lineErr != nil {
			observed.wire.Body = captured.Bytes()
			observed.wire.Err = fmt.Errorf("read the first SSE frame: %w", lineErr)
			return observed, observed.wire.Err
		}
	}
	observed.cancelAt = time.Now()
	cancel()
	observed.wire.Body = captured.Bytes()
	if observed.firstEvent == "" {
		err := errors.New("the first SSE frame carried no data")
		observed.wire.Err = fmt.Errorf("%w: %s", errClientCancelled, err)
		return observed, err
	}
	observed.wire.Err = fmt.Errorf("%w: the journey stopped reading after the first dispatched SSE frame; the rest of the stalled response was not drained", errClientCancelled)
	return observed, nil
}
