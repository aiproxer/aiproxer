package responses_test

// Error-classification proofs for the SIWC failure taxonomy (spec task 6.1).
//
// Every proof drives a REAL provider fixture: the reference server answers the
// documented refusal shape, the transport and the normalizer produce the error
// a caller would receive, and Classify must map it deterministically. The
// fixtures cover the documented 4xx capability/route/input codes, the 429
// usage limit, 5xx and network transients, stream-terminal failures, and the
// terminal/transient refresh outcomes of the 2.5 vocabulary.
//
// Three properties are proven separately:
//
//   - DETERMINISM: one provider answer maps to one class, one phase and one
//     source, and the same answer classifies identically twice.
//   - STRUCTURE: a local discriminator is never read as a provider recovery
//     code. The collision fixture carries the documented usage code as a
//     completed terminal's STATUS, which the normalizer records as a local
//     contradictory-event discriminator; a classifier that matched the string
//     would report usage exhaustion.
//   - NON-LEAKAGE: a provider message that echoes the access token, a raw
//     body member the connector never reads, and a refresh cause that carries
//     credential text never reach the rendered classification.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// documentedUsageCode is the provider's plan-limit code, used both as a real
// provider code and as the local-collision fixture.
const documentedUsageCode = "subscription_sharing_usage_limit_exceeded"

// rawBodyMarker travels in a provider body member the connector never reads.
const rawBodyMarker = "RAW-PROVIDER-BODY-MARKER"

// requireClassified asserts that a failure maps to the named class.
//
// The failure is fatal whenever the class differs, so a classifier that
// returned a different class cannot leave this proof green.
func requireClassified(t *testing.T, err error, want responses.ErrorClass) *responses.ClassifiedError {
	t.Helper()
	if err == nil {
		t.Fatalf("the fixture produced no error, want the %q class", want)
	}
	classified, ok := responses.Classify(err)
	if !ok || classified == nil {
		t.Fatalf("the failure is unclassified (%T), want the %q class: %v", err, want, err)
	}
	if classified.Class != want {
		t.Fatalf("the failure class = %q, want %q: %v", classified.Class, want, err)
	}
	return classified
}

// requireNoLeak asserts the rendered classification carries no secret, for
// every formatting verb the Formatter can reach.
func requireNoLeak(t *testing.T, classified *responses.ClassifiedError, secrets ...string) {
	t.Helper()
	renderings := []string{
		classified.Error(),
		fmt.Sprintf("%v", classified),
		fmt.Sprintf("%+v", classified),
		fmt.Sprintf("%q", classified),
	}
	for _, rendered := range renderings {
		if len(rendered) > 4096 {
			t.Fatalf("the classification is %d bytes, want it bounded: %q", len(rendered), rendered)
		}
		if strings.Contains(rendered, testAccessToken) {
			t.Fatalf("the classification leaked the access token: %q", rendered)
		}
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			if strings.Contains(rendered, secret) {
				t.Fatalf("the classification leaked %q: %q", secret, rendered)
			}
		}
	}
}

// requireNoBodyLeak asserts an unread provider body member never reaches the
// rendered classification.
func requireNoBodyLeak(t *testing.T, classified *responses.ClassifiedError) {
	t.Helper()
	if rendered := classified.Error(); strings.Contains(rendered, rawBodyMarker) {
		t.Fatalf("the classification leaked an unread provider body member: %q", rendered)
	}
}

// admissionAnswer is one provider refusal fixture.
type admissionAnswer struct {
	name      string
	status    int
	code      string
	wantClass responses.ErrorClass
	wantRetry bool
}

// providerRefusalBody builds the documented Responses error document, with a
// token-bearing message and an unread sibling member.
func providerRefusalBody(message, code string) string {
	errorObject := `"message":` + jsonString(message)
	if code != "" {
		errorObject += `,"code":` + jsonString(code)
	}
	return `{"error":{` + errorObject + `,"type":"invalid_request_error"},"carrier":` + jsonString(rawBodyMarker) + `}`
}

// answerProvisionedProvider starts the reference server with one refusal.
func answerProvisionedProvider(t *testing.T, status int, code string) *referenceServer {
	t.Helper()
	reference := newReferenceServer(t)
	reference.requestIDHeader("req-class-1")
	message := "the provider echoed " + testAccessToken + " and refused"
	reference.answer(status, "application/json", providerRefusalBody(message, code))
	return reference
}

// TestClassify_ProviderAdmissionFailures proves each documented admission
// refusal maps to its recovery class with the provider identity preserved.
func TestClassify_ProviderAdmissionFailures(t *testing.T) {
	t.Parallel()
	cases := []admissionAnswer{
		{
			name:      "unsupported capability",
			status:    http.StatusBadRequest,
			code:      "subscription_sharing_unsupported_capability",
			wantClass: responses.ErrorClassUnsupportedCapability,
		},
		{
			name:      "route not supported",
			status:    http.StatusForbidden,
			code:      "subscription_sharing_route_not_supported",
			wantClass: responses.ErrorClassRoute,
		},
		{
			name:      "invalid subscriber",
			status:    http.StatusUnauthorized,
			code:      "subscription_sharing_invalid_user",
			wantClass: responses.ErrorClassInvalidUserContext,
		},
		{
			name:      "usage exhausted",
			status:    http.StatusTooManyRequests,
			code:      documentedUsageCode,
			wantClass: responses.ErrorClassUsageExhausted,
		},
		{
			name:      "usage availability unavailable",
			status:    http.StatusServiceUnavailable,
			code:      "subscription_sharing_usage_unavailable",
			wantClass: responses.ErrorClassTemporaryUnavailable,
			wantRetry: true,
		},
		{
			name:      "subscriber context temporarily unavailable",
			status:    http.StatusServiceUnavailable,
			code:      "subscription_sharing_user_unavailable",
			wantClass: responses.ErrorClassTemporaryUnavailable,
			wantRetry: true,
		},
		{
			name:      "server failure without a code",
			status:    http.StatusBadGateway,
			wantClass: responses.ErrorClassTemporaryUnavailable,
			wantRetry: true,
		},
		{
			name:      "route by status",
			status:    http.StatusNotFound,
			wantClass: responses.ErrorClassRoute,
		},
		{
			name:      "invalid input by status",
			status:    http.StatusBadRequest,
			wantClass: responses.ErrorClassInvalidUserContext,
		},
		{
			name:      "redirect refused",
			status:    http.StatusFound,
			wantClass: responses.ErrorClassRoute,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reference := answerProvisionedProvider(t, tc.status, tc.code)
			client := newReferenceClient(t, reference, newSelectedAuthority())
			stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
			if stream != nil {
				_ = stream.Close()
			}
			classified := requireClassified(t, err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseAdmission {
				t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseAdmission)
			}
			if classified.Source != responses.ErrorSourceProvider {
				t.Fatalf("the failure source = %q, want the provider", classified.Source)
			}
			if classified.Retryable != tc.wantRetry {
				t.Fatalf("the retryable flag = %t, want %t", classified.Retryable, tc.wantRetry)
			}
			if classified.Status != tc.status {
				t.Fatalf("the preserved status = %d, want %d", classified.Status, tc.status)
			}
			if classified.Code != tc.code {
				t.Fatalf("the preserved code = %q, want %q", classified.Code, tc.code)
			}
			if classified.RequestID != "req-class-1" {
				t.Fatalf("the preserved request id = %q, want %q", classified.RequestID, "req-class-1")
			}
			if classified.ProfileID != testProfileID {
				t.Fatalf("the profile provenance = %q, want %q", classified.ProfileID, testProfileID)
			}
			if classified.Diagnostic == "" {
				t.Fatal("the provider diagnostic is absent; the bounded text should travel")
			}
			requireNoLeak(t, classified)
			requireNoBodyLeak(t, classified)
			if !strings.Contains(classified.Diagnostic, "[REDACTED]") {
				t.Fatalf("the echoed credential was not redacted: %q", classified.Diagnostic)
			}
			if !errors.Is(err, responses.ErrUnexpectedStatus) {
				t.Fatalf("the refusal lost ErrUnexpectedStatus: %v", err)
			}
			if !errors.Is(classified, responses.ErrUnexpectedStatus) {
				t.Fatalf("the classification lost the cause: %v", classified)
			}
		})
	}
}

// streamFixture is one provider stream shape and its expected classification.
type streamFixture struct {
	name        string
	body        string
	wantOrigin  responses.FailureOrigin
	wantClass   responses.ErrorClass
	wantCode    string
	wantReason  string
	failureCode string
}

// TestClassify_ProviderStreamFailures proves each stream-terminal shape maps
// to its recovery class and carries who produced it.
func TestClassify_ProviderStreamFailures(t *testing.T) {
	t.Parallel()
	cases := []streamFixture{
		{
			name: "provider error event with the documented usage code",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: error\ndata: {\"type\":\"error\",\"code\":\"" + documentedUsageCode + `","message":"the plan usage limit was reached"}` + "\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassUsageExhausted,
			wantCode:   documentedUsageCode,
			wantReason: "provider_error",
		},
		{
			name: "provider error event with the invalid subscriber code",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: error\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_invalid_user\",\"message\":\"the subscriber context was refused\"}\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantCode:   "subscription_sharing_invalid_user",
			wantReason: "provider_error",
		},
		{
			name: "provider error event with an undocumented code",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: error\ndata: {\"type\":\"error\",\"code\":\"some_new_provider_condition\",\"message\":\"unknown to this build\"}\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassProviderFailure,
			wantCode:   "some_new_provider_condition",
			wantReason: "provider_error",
		},
		{
			name: "failed terminal with the documented unsupported capability code",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\",\"error\":{\"code\":\"subscription_sharing_unsupported_capability\",\"message\":\"unsupported\"}}}\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassUnsupportedCapability,
			wantCode:   "subscription_sharing_unsupported_capability",
			wantReason: "terminal_failed",
		},
		{
			name:        "contradictory completed terminal carrying the usage code as its status",
			body:        "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"" + documentedUsageCode + "\"}}\n\n",
			wantOrigin:  responses.FailureOriginLocal,
			wantClass:   responses.ErrorClassProtocolViolation,
			wantCode:    "",
			wantReason:  "contradictory_event",
			failureCode: documentedUsageCode,
		},
		{
			name:       "malformed event payload",
			body:       "event: response.output_text.delta\ndata: this-is-not-json\n\n",
			wantOrigin: responses.FailureOriginLocal,
			wantClass:  responses.ErrorClassProtocolViolation,
			wantReason: "malformed_event",
		},
		{
			name:       "stream ended without a terminal",
			body:       "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n",
			wantOrigin: responses.FailureOriginLocal,
			wantClass:  responses.ErrorClassProtocolViolation,
			wantReason: "missing_terminal",
		},
		{
			name: "incomplete terminal",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassProviderFailure,
			wantCode:   "max_output_tokens",
			wantReason: "terminal_incomplete",
		},
		{
			name: "cancelled terminal",
			body: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"event: response.cancelled\ndata: {\"type\":\"response.cancelled\",\"response\":{\"id\":\"resp_1\",\"status\":\"cancelled\"}}\n\n",
			wantOrigin: responses.FailureOriginProvider,
			wantClass:  responses.ErrorClassProviderFailure,
			wantReason: "terminal_cancelled",
		},
		{
			name:       "unrecognized event type",
			body:       "event: response.some_new_event\ndata: {\"type\":\"response.some_new_event\"}\n\n",
			wantOrigin: responses.FailureOriginLocal,
			wantClass:  responses.ErrorClassProtocolViolation,
			wantReason: "unrecognized_event",
		},
		{
			name:       "tool content with no identity",
			body:       "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n",
			wantOrigin: responses.FailureOriginLocal,
			wantClass:  responses.ErrorClassProtocolViolation,
			wantReason: "tool_identity_missing",
		},
		{
			name:       "unrepresentable output item",
			body:       "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"image_generation_call\",\"id\":\"ig_1\"}}\n\n",
			wantOrigin: responses.FailureOriginLocal,
			wantClass:  responses.ErrorClassUnsupportedCapability,
			wantReason: "unrepresentable_output_item",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := normalizedProviderFailure(t, tc.body)
			var failure *responses.StreamFailure
			if !errors.As(err, &failure) {
				t.Fatalf("the stream failure is %T, want a *responses.StreamFailure: %v", err, err)
			}
			if failure.Origin != tc.wantOrigin {
				t.Fatalf("the failure origin = %q, want %q", failure.Origin, tc.wantOrigin)
			}
			if failure.ProfileID != testProfileID {
				t.Fatalf("the failure profile = %q, want %q", failure.ProfileID, testProfileID)
			}
			if failure.RequestID != "req-stream-1" {
				t.Fatalf("the failure request id = %q, want %q", failure.RequestID, "req-stream-1")
			}
			classified := requireClassified(t, err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseStream {
				t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseStream)
			}
			wantSource := responses.ErrorSourceLocal
			if tc.wantOrigin == responses.FailureOriginProvider {
				wantSource = responses.ErrorSourceProvider
			}
			if classified.Source != wantSource {
				t.Fatalf("the failure source = %q, want %q", classified.Source, wantSource)
			}
			if classified.Code != tc.wantCode {
				t.Fatalf("the classified provider code = %q, want %q", classified.Code, tc.wantCode)
			}
			if classified.Reason != tc.wantReason {
				t.Fatalf("the classification reason = %q, want %q", classified.Reason, tc.wantReason)
			}
			if classified.Retryable {
				t.Fatal("a stream-terminal failure must not be marked retryable")
			}
			if tc.failureCode != "" && failure.Code != tc.failureCode {
				t.Fatalf("the fixture lost its collision: failure code = %q, want %q", failure.Code, tc.failureCode)
			}
			requireNoLeak(t, classified)
		})
	}
}

// normalizedProviderFailure runs one SSE body through the real transport and
// the normalizer and returns the terminal failure.
func normalizedProviderFailure(t *testing.T, body string) error {
	t.Helper()
	reference := newReferenceServer(t)
	reference.requestIDHeader("req-stream-1")
	reference.answer(http.StatusOK, "text/event-stream", body)
	client := newReferenceClient(t, reference, newSelectedAuthority())
	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	normalized := stream.Normalize()
	t.Cleanup(func() { _ = normalized.Close() })
	for {
		if _, err := normalized.Recv(context.Background()); err != nil {
			if errors.Is(err, io.EOF) {
				t.Fatal("the stream reported success, want a terminal failure")
			}
			return err
		}
	}
}

// TestClassify_LocalDiscriminatorIsNeverAProviderCode is the structural proof
// of the 5.2 F2 distinction.
//
// The fixture makes the provider's own status text EQUAL the documented usage
// code. The normalizer records that text on a contradictory-event failure,
// whose Code is a LOCAL discriminator. The classification must therefore
// report a local protocol violation with an empty provider code, and the
// failure must say so through its typed Origin field. A classifier that
// matched Code as a provider code would report usage exhaustion; a normalizer
// that marked the failure as provider-reported would report a provider source.
func TestClassify_LocalDiscriminatorIsNeverAProviderCode(t *testing.T) {
	t.Parallel()
	err := normalizedProviderFailure(t,
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\""+documentedUsageCode+"\"}}\n\n")
	var failure *responses.StreamFailure
	if !errors.As(err, &failure) {
		t.Fatalf("the stream failure is %T, want a *responses.StreamFailure: %v", err, err)
	}
	if failure.Code != documentedUsageCode {
		t.Fatalf("the fixture collision was lost: failure code = %q, want %q", failure.Code, documentedUsageCode)
	}
	if failure.Origin != responses.FailureOriginLocal {
		t.Fatalf("the local discriminator is marked %q, want %q", failure.Origin, responses.FailureOriginLocal)
	}
	classified := requireClassified(t, err, responses.ErrorClassProtocolViolation)
	if classified.Class == responses.ErrorClassUsageExhausted {
		t.Fatal("the classifier read the LOCAL discriminator as a provider usage code")
	}
	if classified.Source != responses.ErrorSourceLocal {
		t.Fatalf("the classification source = %q, want %q", classified.Source, responses.ErrorSourceLocal)
	}
	if classified.Code != "" {
		t.Fatalf("the local discriminator reached the provider code field: %q", classified.Code)
	}
	if classified.Reason != "contradictory_event" {
		t.Fatalf("the classification reason = %q, want %q", classified.Reason, "contradictory_event")
	}
	if classified.Status != 0 || classified.RequestID != "req-stream-1" {
		t.Fatalf("the local failure preserved status=%d request-id=%q, want 0 and the answer's request id", classified.Status, classified.RequestID)
	}
}

// TestClassify_TransportAndUnreadableFailures proves the transient network
// classes and that the 5.1 flat unreadable sentinel stays flat.
func TestClassify_TransportAndUnreadableFailures(t *testing.T) {
	t.Parallel()
	client := newFailingTransportClient(t, &roundTripFailure{
		err: errors.New("dial failed while sending " + testAccessToken + " and " + rawBodyMarker),
	})
	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	classified := requireClassified(t, err, responses.ErrorClassTemporaryUnavailable)
	if classified.Phase != responses.ErrorPhaseTransport {
		t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseTransport)
	}
	if classified.Source != responses.ErrorSourceLocal {
		t.Fatalf("the failure source = %q, want a local observation", classified.Source)
	}
	if !classified.Retryable {
		t.Fatal("a network failure must be retryable")
	}
	if classified.ProfileID != testProfileID {
		t.Fatalf("the transport failure profile = %q, want %q", classified.ProfileID, testProfileID)
	}
	requireNoLeak(t, classified)

	unreadableClient := newFailingTransportClient(t, &roundTripFailure{
		body: &failingReadCloser{err: errors.New("trailer echoed " + testAccessToken + " " + rawBodyMarker)},
	})
	raw, err := unreadableClient.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	normalized := raw.Normalize()
	t.Cleanup(func() { _ = normalized.Close() })
	_, err = normalized.Recv(context.Background())
	if !errors.Is(err, responses.ErrStreamUnreadable) {
		t.Fatalf("the mid-stream read failure = %v, want the flat ErrStreamUnreadable", err)
	}
	classified = requireClassified(t, err, responses.ErrorClassTemporaryUnavailable)
	if classified.Phase != responses.ErrorPhaseStream {
		t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseStream)
	}
	if classified.Diagnostic != "" {
		t.Fatalf("the flat sentinel gained a diagnostic: %q", classified.Diagnostic)
	}
	if classified.Status != 0 || classified.Code != "" {
		t.Fatalf("the flat sentinel gained provider identity: status=%d code=%q", classified.Status, classified.Code)
	}
	requireNoLeak(t, classified)
}

// roundTripFailure is a transport that fails the request or returns a body
// whose read fails, without a network.
type roundTripFailure struct {
	err       error
	body      io.ReadCloser
	requestID string
}

// RoundTrip returns the prepared failure.
func (r *roundTripFailure) RoundTrip(request *http.Request) (*http.Response, error) {
	if r.err != nil {
		return nil, r.err
	}
	header := http.Header{"Content-Type": {"text/event-stream"}}
	if r.requestID != "" {
		header.Set("X-Request-Id", r.requestID)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       r.body,
		Request:    request,
	}, nil
}

// failingReadCloser fails every read with the prepared diagnostic.
type failingReadCloser struct{ err error }

// Read fails without returning bytes.
func (f *failingReadCloser) Read([]byte) (int, error) { return 0, f.err }

// Close releases the body.
func (f *failingReadCloser) Close() error { return nil }

// failingAfterReadCloser returns a prefix of the answer and then the prepared
// failure. It reproduces the raw seam's own failure shape: the size bounds and
// the unreadable body are constant sentinels, never the underlying read error.
type failingAfterReadCloser struct {
	data []byte
	err  error
}

// Read returns the prefix first, then the prepared failure.
func (f *failingAfterReadCloser) Read(p []byte) (int, error) {
	if len(f.data) > 0 {
		n := copy(p, f.data)
		f.data = f.data[n:]
		return n, nil
	}
	return 0, f.err
}

// Close releases the body.
func (f *failingAfterReadCloser) Close() error { return nil }

// newFailingTransportClient returns the transport under test over a prepared
// failing round tripper.
func newFailingTransportClient(t *testing.T, transport http.RoundTripper) *responses.Client {
	t.Helper()
	client, err := responses.NewClient(responses.ClientConfig{
		HTTPClient: &http.Client{Transport: transport},
		Tokens:     newSelectedAuthority(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// TestClassify_RawStreamFailuresKeepAnswerProvenance proves a raw stream
// failure keeps the correlation the answer envelope already carries.
//
// The failure cause stays the value-free constant: only the provider request
// id and the local profile id travel, and the untrusted read error - which may
// echo the credential - is never restored.
func TestClassify_RawStreamFailuresKeepAnswerProvenance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       io.ReadCloser
		wantClass  responses.ErrorClass
		wantReason string
		sentinel   error
	}{
		{
			name:       "an unreadable stream body",
			body:       &failingReadCloser{err: errors.New("trailer echoed " + testAccessToken + " " + rawBodyMarker)},
			wantClass:  responses.ErrorClassTemporaryUnavailable,
			wantReason: "stream_unreadable",
			sentinel:   responses.ErrStreamUnreadable,
		},
		{
			name: "a stream over its whole-answer bound",
			body: &failingAfterReadCloser{
				data: []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"),
				err:  responses.ErrStreamTooLarge,
			},
			wantClass:  responses.ErrorClassProtocolViolation,
			wantReason: "stream_too_large",
			sentinel:   responses.ErrStreamTooLarge,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newFailingTransportClient(t, &roundTripFailure{
				requestID: "req-read-1",
				body:      tc.body,
			})
			raw, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			normalized := raw.Normalize()
			t.Cleanup(func() { _ = normalized.Close() })
			var recvErr error
			for {
				if _, recvErr = normalized.Recv(context.Background()); recvErr != nil {
					break
				}
			}
			if errors.Is(recvErr, io.EOF) {
				t.Fatal("the stream reported success, want a terminal failure")
			}
			err = recvErr
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("the raw stream failure = %v (%T), want %v", err, err, tc.sentinel)
			}
			if err.Error() != tc.sentinel.Error() {
				t.Fatalf("the failure retained provider text: %q, want the constant %q", err.Error(), tc.sentinel.Error())
			}
			if got := raw.RequestID(); got != "req-read-1" {
				t.Fatalf("the fixture lost the answer envelope's request id: %q", got)
			}
			if got := raw.ProfileID(); got != testProfileID {
				t.Fatalf("the fixture lost the answer envelope's profile: %q", got)
			}
			classified := requireClassified(t, err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseStream {
				t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseStream)
			}
			if classified.Source != responses.ErrorSourceLocal {
				t.Fatalf("the failure source = %q, want local", classified.Source)
			}
			if classified.Reason != tc.wantReason {
				t.Fatalf("the classification reason = %q, want %q", classified.Reason, tc.wantReason)
			}
			if classified.RequestID != "req-read-1" {
				t.Fatalf("the classified request id = %q, want %q", classified.RequestID, "req-read-1")
			}
			if classified.ProfileID != testProfileID {
				t.Fatalf("the classified profile = %q, want %q", classified.ProfileID, testProfileID)
			}
			if classified.Diagnostic != "" || classified.Status != 0 || classified.Code != "" {
				t.Fatalf("the flat sentinel gained provider identity or text: status=%d code=%q diagnostic=%q",
					classified.Status, classified.Code, classified.Diagnostic)
			}
			requireNoLeak(t, classified)
		})
	}
}

// TestClassify_LocalRefusals proves pre-I/O refusals carry their own classes
// and never a provider identity.
func TestClassify_LocalRefusals(t *testing.T) {
	t.Parallel()
	temperature := 0.2
	strictCall := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
		Options: aipapi.GenerationOptions{Temperature: &temperature},
	}
	_, strictErr := responses.AdmitCall(
		responses.ProjectInput{Call: strictCall, Model: "gpt-6.1-codex"},
		responses.PolicyModeStrict,
	)
	continuationCall := aipapi.Call{
		PreviousResponseID: "resp_parent",
		Items:              []aipapi.Item{},
	}
	_, continuationErr := responses.AdmitCall(
		responses.ProjectInput{Call: continuationCall, Model: "gpt-6.1-codex"},
		responses.PolicyModeInteroperable,
	)
	_, modeErr := responses.ParsePolicyMode("aggressive")

	cases := []struct {
		name       string
		err        error
		wantClass  responses.ErrorClass
		wantReason string
	}{
		{
			name:       "strict posture refuses an unsupported generation option",
			err:        strictErr,
			wantClass:  responses.ErrorClassUnsupportedCapability,
			wantReason: "generation_option_unsupported",
		},
		{
			name:       "a continuation without its history cannot be served",
			err:        continuationErr,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "previous_response_missing_history",
		},
		{
			name:       "no registration is selected",
			err:        responses.ErrNoSelectedProfile,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "no_selected_profile",
		},
		{
			name:       "the registration has no usable credential",
			err:        responses.ErrRegistrationUnusable,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "registration_unusable",
		},
		{
			name:       "the request is not addressed to the public route",
			err:        responses.ErrNotPublicEndpoint,
			wantClass:  responses.ErrorClassRoute,
			wantReason: "not_public_endpoint",
		},
		{
			name:       "the projected request does not stream",
			err:        responses.ErrRequestNotStreaming,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "request_not_streaming",
		},
		{
			name:       "the projected request exceeds its bound",
			err:        responses.ErrRequestTooLarge,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "request_too_large",
		},
		{
			name:       "the configured policy posture is unknown",
			err:        modeErr,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "unknown_policy_mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classified := requireClassified(t, tc.err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseLocal {
				t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseLocal)
			}
			if classified.Source != responses.ErrorSourceLocal {
				t.Fatalf("the failure source = %q, want local", classified.Source)
			}
			if classified.Code != "" || classified.Status != 0 || classified.RequestID != "" {
				t.Fatalf("a local refusal carries provider identity: code=%q status=%d request-id=%q",
					classified.Code, classified.Status, classified.RequestID)
			}
			if classified.Reason != tc.wantReason {
				t.Fatalf("the classification reason = %q, want %q", classified.Reason, tc.wantReason)
			}
			if classified.Retryable {
				t.Fatal("a local refusal must not be retryable unchanged")
			}
			requireNoLeak(t, classified)
		})
	}
}

// TestClassify_AdmitCallRefusalsAreClassified proves the refusals AdmitCall
// produces INSIDE this connector are classified, not left unclassified.
//
// Both fixtures are real admission calls: a valid canonical call whose residual
// semantic has no public wire carrier (the 4.2 projector's refusal, propagated
// unchanged by the preview policy) and a canonical call that violates its own
// invariants. Neither reached the provider, so neither may report a provider
// class, a provider source, a provider identity or a diagnostic. The
// projector's identity (ErrCallNotRepresentable) is preserved instead of being
// hidden behind a generic policy error.
func TestClassify_AdmitCallRefusalsAreClassified(t *testing.T) {
	t.Parallel()
	residualCall := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
		SemanticExtensions: []aipapi.SemanticExtension{{
			Namespace:   "acme",
			Type:        "beta",
			Implementor: "acme",
			Direction:   aipapi.SemanticExtensionDirectionRequest,
			Presence:    aipapi.SemanticExtensionValue,
			Data:        json.RawMessage(`{"x":1}`),
		}},
	}
	if err := residualCall.Validate(); err != nil {
		t.Fatalf("the residual-semantic fixture must be a valid canonical call: %v", err)
	}
	_, residualErr := responses.AdmitCall(
		responses.ProjectInput{Call: residualCall, Model: "gpt-6.1-codex"},
		responses.PolicyModeInteroperable,
	)
	if !errors.Is(residualErr, responses.ErrCallNotRepresentable) {
		t.Fatalf("the residual-semantic refusal must stay the projector's: %v", residualErr)
	}
	if _, ok := errors.AsType[*responses.ProjectionError](residualErr); !ok {
		t.Fatalf("the residual-semantic refusal must carry a ProjectionError, got %T: %v", residualErr, residualErr)
	}

	malformedCall := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser}},
	}
	_, malformedErr := responses.AdmitCall(
		responses.ProjectInput{Call: malformedCall, Model: "gpt-6.1-codex"},
		responses.PolicyModeInteroperable,
	)
	if malformedErr == nil || !errors.Is(malformedErr, aipapi.ErrInvalidCall) {
		t.Fatalf("the malformed-call fixture must be refused as an invalid canonical call: %v", malformedErr)
	}

	cases := []struct {
		name       string
		err        error
		wantClass  responses.ErrorClass
		wantReason string
	}{
		{
			name:       "the projector refuses a residual semantic",
			err:        residualErr,
			wantClass:  responses.ErrorClassUnsupportedCapability,
			wantReason: string(responses.ProjectionReasonSemanticExtension),
		},
		{
			name:       "the canonical call is malformed",
			err:        malformedErr,
			wantClass:  responses.ErrorClassInvalidUserContext,
			wantReason: "invalid_canonical_call",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classified := requireClassified(t, tc.err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseLocal {
				t.Fatalf("the local admission phase = %q, want %q", classified.Phase, responses.ErrorPhaseLocal)
			}
			if classified.Source != responses.ErrorSourceLocal {
				t.Fatalf("the local admission source = %q, want %q", classified.Source, responses.ErrorSourceLocal)
			}
			if classified.Reason != tc.wantReason {
				t.Fatalf("the classification reason = %q, want %q", classified.Reason, tc.wantReason)
			}
			if classified.Code != "" || classified.Status != 0 || classified.RequestID != "" || classified.Diagnostic != "" {
				t.Fatalf("a local refusal carries provider identity or provider text: code=%q status=%d request-id=%q diagnostic=%q",
					classified.Code, classified.Status, classified.RequestID, classified.Diagnostic)
			}
			if classified.Retryable {
				t.Fatal("a local refusal must not be retryable unchanged")
			}
			if !errors.Is(classified, responses.ErrCallNotRepresentable) && !errors.Is(classified, aipapi.ErrInvalidCall) {
				t.Fatalf("the classification lost the refusal identity: %v", classified)
			}
			requireNoLeak(t, classified)
		})
	}
}

// TestClassify_UnusableProviderAnswersKeepTheirObservedClass proves that an
// answer which ARRIVED and cannot be used is classified from what did arrive
// (status, request id) instead of being reported as absent.
func TestClassify_UnusableProviderAnswersKeepTheirObservedClass(t *testing.T) {
	t.Parallel()
	t.Run("a success status that is not an event stream", func(t *testing.T) {
		t.Parallel()
		reference := newReferenceServer(t)
		reference.requestIDHeader("req-content-1")
		reference.answer(http.StatusOK, "application/json", `{"id":"resp_1"}`)
		client := newReferenceClient(t, reference, newSelectedAuthority())
		stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
		if stream != nil {
			_ = stream.Close()
		}
		classified := requireClassified(t, err, responses.ErrorClassProtocolViolation)
		if classified.Source != responses.ErrorSourceLocal {
			t.Fatalf("the refusal source = %q, want %q: the provider reported success, the connector judged its bytes",
				classified.Source, responses.ErrorSourceLocal)
		}
		if classified.Status != http.StatusOK {
			t.Fatalf("the observed status = %d, want 200", classified.Status)
		}
		if classified.RequestID != "req-content-1" {
			t.Fatalf("the observed request id = %q, want %q", classified.RequestID, "req-content-1")
		}
		if !errors.Is(err, responses.ErrUnexpectedContentType) {
			t.Fatalf("the refusal lost ErrUnexpectedContentType: %v", err)
		}
		requireNoLeak(t, classified)
	})

	t.Run("a 5xx whose error document exceeds its bound", func(t *testing.T) {
		t.Parallel()
		reference := newReferenceServer(t)
		reference.requestIDHeader("req-oversize-1")
		reference.answer(http.StatusServiceUnavailable, "application/json",
			`{"error":{"message":"`+strings.Repeat("x", 65<<10)+`"}}`)
		client := newReferenceClient(t, reference, newSelectedAuthority())
		stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
		if stream != nil {
			_ = stream.Close()
		}
		if !errors.Is(err, responses.ErrErrorBodyTooLarge) {
			t.Fatalf("the oversize document lost its sentinel: %v", err)
		}
		classified := requireClassified(t, err, responses.ErrorClassTemporaryUnavailable)
		if classified.Status != http.StatusServiceUnavailable {
			t.Fatalf("the observed status = %d, want 503", classified.Status)
		}
		if classified.RequestID != "req-oversize-1" {
			t.Fatalf("the observed request id = %q, want %q", classified.RequestID, "req-oversize-1")
		}
		if !classified.Retryable {
			t.Fatal("a 5xx answer must remain retryable")
		}
		requireNoLeak(t, classified)
	})
}

// TestClassify_BareThrottleIsNotPlanExhaustion proves an HTTP 429 alone does
// not establish the documented plan limit.
//
// HTTP throttling is not evidence that the subscription plan is exhausted: only
// the documented plan-limit code names that outcome, and it stays
// non-retryable. A throttle with no code, or with a code this build does not
// document, remains a retryable temporary condition, and the observed status
// and code still travel.
func TestClassify_BareThrottleIsNotPlanExhaustion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code string
	}{
		{name: "a throttle without a code", code: ""},
		{name: "a throttle with an undocumented code", code: "some_undocumented_throttle_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reference := newReferenceServer(t)
			reference.requestIDHeader("req-throttle-1")
			body := providerRefusalBody("the provider is throttling this client", tc.code)
			if tc.code == "" && strings.Contains(body, `"code"`) {
				t.Fatalf("the fixture body carries a code: %s", body)
			}
			reference.answer(http.StatusTooManyRequests, "application/json", body)
			client := newReferenceClient(t, reference, newSelectedAuthority())
			stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
			if stream != nil {
				_ = stream.Close()
			}
			classified := requireClassified(t, err, responses.ErrorClassTemporaryUnavailable)
			if classified.Class == responses.ErrorClassUsageExhausted {
				t.Fatal("an HTTP throttle without the documented plan-limit code is reported as plan exhaustion")
			}
			if !classified.Retryable {
				t.Fatal("a throttle without the documented plan-limit code must stay retryable")
			}
			if classified.Phase != responses.ErrorPhaseAdmission {
				t.Fatalf("the throttle phase = %q, want %q", classified.Phase, responses.ErrorPhaseAdmission)
			}
			if classified.Source != responses.ErrorSourceProvider {
				t.Fatalf("the throttle source = %q, want the provider", classified.Source)
			}
			if classified.Status != http.StatusTooManyRequests {
				t.Fatalf("the preserved status = %d, want %d", classified.Status, http.StatusTooManyRequests)
			}
			if classified.Code != tc.code {
				t.Fatalf("the preserved code = %q, want %q", classified.Code, tc.code)
			}
			if classified.RequestID != "req-throttle-1" {
				t.Fatalf("the preserved request id = %q, want %q", classified.RequestID, "req-throttle-1")
			}
			requireNoLeak(t, classified)
		})
	}
}

// TestClassify_RefreshOutcomesReuseTheSIWCVocabulary proves the serialized
// refresh's terminal and transient outcomes surface as their own classes, not
// as a generic local refusal.
func TestClassify_RefreshOutcomesReuseTheSIWCVocabulary(t *testing.T) {
	t.Parallel()
	const causeMarker = "refresh-token-material-that-must-not-render"
	terminal := siwc.NewRefreshError(siwc.RefreshClassTerminal, http.StatusBadRequest, "invalid_grant",
		"the refresh token is unusable", errors.New(causeMarker))
	transient := siwc.NewRefreshError(siwc.RefreshClassTransient, http.StatusServiceUnavailable, "server_error",
		"the provider could not be reached", errors.New(causeMarker))

	cases := []struct {
		name       string
		err        error
		wantClass  responses.ErrorClass
		wantRetry  bool
		wantName   string
		wantSource responses.ErrorSource
		wantStatus int
		wantCode   string
	}{
		{
			name:       "terminal refresh needs a new authorization",
			err:        terminal,
			wantClass:  responses.ErrorClassRefreshTerminal,
			wantName:   siwc.RefreshClassTerminal,
			wantSource: responses.ErrorSourceProvider,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_grant",
		},
		{
			name:       "transient refresh keeps the saved credentials",
			err:        transient,
			wantClass:  responses.ErrorClassRefreshTransient,
			wantRetry:  true,
			wantName:   siwc.RefreshClassTransient,
			wantSource: responses.ErrorSourceProvider,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "server_error",
		},
		{
			name:       "bare terminal sentinel",
			err:        siwc.ErrRefreshTerminal,
			wantClass:  responses.ErrorClassRefreshTerminal,
			wantName:   siwc.RefreshClassTerminal,
			wantSource: responses.ErrorSourceLocal,
		},
		{
			name:       "bare transient sentinel",
			err:        siwc.ErrRefreshTransient,
			wantClass:  responses.ErrorClassRefreshTransient,
			wantRetry:  true,
			wantName:   siwc.RefreshClassTransient,
			wantSource: responses.ErrorSourceLocal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classified := requireClassified(t, tc.err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseCredential {
				t.Fatalf("the failure phase = %q, want %q", classified.Phase, responses.ErrorPhaseCredential)
			}
			if classified.Source != tc.wantSource {
				t.Fatalf("the failure source = %q, want %q", classified.Source, tc.wantSource)
			}
			if classified.Retryable != tc.wantRetry {
				t.Fatalf("the retryable flag = %t, want %t", classified.Retryable, tc.wantRetry)
			}
			if classified.Reason != tc.wantName {
				t.Fatalf("the classification reason = %q, want %q", classified.Reason, tc.wantName)
			}
			if classified.Status != tc.wantStatus {
				t.Fatalf("the preserved refresh status = %d, want %d", classified.Status, tc.wantStatus)
			}
			if classified.Code != tc.wantCode {
				t.Fatalf("the preserved refresh code = %q, want %q", classified.Code, tc.wantCode)
			}
			requireNoLeak(t, classified, causeMarker)
		})
	}

	// The registration path wraps the refresh outcome in its own local
	// refusal. The refresh class must survive that wrapper: a terminal
	// refresh is not the generic invalid-registration class.
	reference := newReferenceServer(t)
	authority := newSelectedAuthority()
	authority.grantErr = map[string]error{testProfileID: terminal}
	client := newReferenceClient(t, reference, authority)
	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("the transport opened a stream for a registration whose refresh was terminal")
	}
	if !errors.Is(err, responses.ErrRegistrationUnusable) {
		t.Fatalf("the client no longer marks an unusable registration: %v", err)
	}
	classified := requireClassified(t, err, responses.ErrorClassRefreshTerminal)
	if classified.Phase != responses.ErrorPhaseCredential {
		t.Fatalf("the registry-wrapped refresh phase = %q, want %q", classified.Phase, responses.ErrorPhaseCredential)
	}
	if classified.Status != http.StatusBadRequest || classified.Code != "invalid_grant" {
		t.Fatalf("the registry-wrapped refresh identity = status %d code %q, want 400/invalid_grant",
			classified.Status, classified.Code)
	}
	if !errors.Is(classified, siwc.ErrRefreshTerminal) {
		t.Fatalf("the classification lost the terminal refresh cause: %v", classified)
	}
	requireNoLeak(t, classified, causeMarker)
}

// TestClassify_RefreshDeadlineKeepsTheTypedOutcome proves a refresh failure
// whose exchange timed out is classified by the 2.5 outcome instead of being
// mistaken for caller cancellation.
//
// The deadline below is produced by an HTTP CLIENT timeout while the caller's
// own context is live, which is exactly the shape requestRefresh constructs
// through classifyExchangeCause. A nested deadline is therefore not evidence
// that the caller withdrew, and the typed refresh outcome is authoritative.
func TestClassify_RefreshDeadlineKeepsTheTypedOutcome(t *testing.T) {
	t.Parallel()
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(slow.Close)
	caller := context.Background()
	client := &http.Client{Timeout: 20 * time.Millisecond}
	request, err := http.NewRequestWithContext(caller, http.MethodPost, slow.URL, nil)
	if err != nil {
		t.Fatalf("the timeout fixture request cannot be built: %v", err)
	}
	_, timeoutErr := client.Do(request)
	if caller.Err() != nil {
		t.Fatalf("the fixture cancelled the caller: %v", caller.Err())
	}
	if !errors.Is(timeoutErr, context.DeadlineExceeded) {
		t.Fatalf("the fixture did not produce an HTTP-client timeout: %v", timeoutErr)
	}
	transient := siwc.NewRefreshError(siwc.RefreshClassTransient, 0, "", "", timeoutErr)
	if !errors.Is(transient, siwc.ErrRefreshTransient) || !errors.Is(transient, context.DeadlineExceeded) {
		t.Fatalf("the fixture is not the production refresh shape: %v", transient)
	}
	terminal := siwc.NewRefreshError(siwc.RefreshClassTerminal, 0, "", "", timeoutErr)

	cases := []struct {
		name      string
		err       error
		wantClass responses.ErrorClass
		wantRetry bool
		wantName  string
	}{
		{
			name:      "a transient refresh whose exchange timed out",
			err:       transient,
			wantClass: responses.ErrorClassRefreshTransient,
			wantRetry: true,
			wantName:  siwc.RefreshClassTransient,
		},
		{
			name:      "a terminal refresh whose exchange timed out",
			err:       terminal,
			wantClass: responses.ErrorClassRefreshTerminal,
			wantName:  siwc.RefreshClassTerminal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classified := requireClassified(t, tc.err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseCredential {
				t.Fatalf("the refresh phase = %q, want %q", classified.Phase, responses.ErrorPhaseCredential)
			}
			if classified.Source != responses.ErrorSourceLocal {
				t.Fatalf("the refresh source = %q, want local: no provider answer was read", classified.Source)
			}
			if classified.Reason != tc.wantName {
				t.Fatalf("the refresh reason = %q, want %q", classified.Reason, tc.wantName)
			}
			if classified.Retryable != tc.wantRetry {
				t.Fatalf("the retryable flag = %t, want %t", classified.Retryable, tc.wantRetry)
			}
			if classified.Status != 0 || classified.Code != "" {
				t.Fatalf("the refresh failure invented provider identity: status=%d code=%q", classified.Status, classified.Code)
			}
		})
	}
}

// TestClassify_RefreshConnectorJudgementsStayLocal proves the refresh branch
// separates what the provider reported from what this connector judged.
//
// The fixtures are the production producers: a real profile store, the real
// Refresher and its production redirect policy against a loopback token
// endpoint. A token endpoint that answers 200 with malformed JSON, or with an
// object that carries no access token, reported SUCCESS; the connector judged
// that answer unusable. A token endpoint that answers with a redirect was
// answered, and the connector REFUSED to follow it. Neither answer is a
// provider failure report, so neither may claim provider origin, and the
// connector's own discriminator (the refusal's literal) must never occupy the
// provider code field.
//
// The provider-failure control is the documented one: 400 with invalid_grant
// is a provider-reported terminal refusal, and its origin, status and code are
// provider data.
func TestClassify_RefreshConnectorJudgementsStayLocal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		status        int
		body          string
		redirect      bool
		wantSentinel  error
		wantClass     responses.ErrorClass
		wantSource    responses.ErrorSource
		wantStatus    int
		wantCode      string
		wantReason    string
		wantRetryable bool
	}{
		{
			name:          "a malformed JSON success answer is the connector's judgement",
			status:        http.StatusOK,
			body:          `{"access_token":`,
			wantSentinel:  siwc.ErrRefreshUnreadable,
			wantClass:     responses.ErrorClassProtocolViolation,
			wantSource:    responses.ErrorSourceLocal,
			wantStatus:    http.StatusOK,
			wantReason:    siwc.RefreshClassUnreadable,
			wantRetryable: false,
		},
		{
			name:          "a tokenless success answer is the connector's judgement",
			status:        http.StatusOK,
			body:          `{"token_type":"Bearer","expires_in":3600}`,
			wantSentinel:  siwc.ErrRefreshUnreadable,
			wantClass:     responses.ErrorClassProtocolViolation,
			wantSource:    responses.ErrorSourceLocal,
			wantStatus:    http.StatusOK,
			wantReason:    siwc.RefreshClassUnreadable,
			wantRetryable: false,
		},
		{
			name:          "a refused redirect is the connector's judgement",
			status:        http.StatusFound,
			redirect:      true,
			wantSentinel:  siwc.ErrRefreshRejected,
			wantClass:     responses.ErrorClassInvalidUserContext,
			wantSource:    responses.ErrorSourceLocal,
			wantStatus:    http.StatusFound,
			wantReason:    siwc.RefreshClassRejected,
			wantRetryable: false,
		},
		{
			name:          "a genuine provider failure keeps its provider identity",
			status:        http.StatusBadRequest,
			body:          `{"error":"invalid_grant"}`,
			wantSentinel:  siwc.ErrRefreshTerminal,
			wantClass:     responses.ErrorClassRefreshTerminal,
			wantSource:    responses.ErrorSourceProvider,
			wantStatus:    http.StatusBadRequest,
			wantCode:      "invalid_grant",
			wantReason:    siwc.RefreshClassTerminal,
			wantRetryable: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// The redirect must not be followed: the request carries the
			// refresh token, and the production client refuses to send it
			// wherever the provider points. This server records a followed
			// redirect so the fixture cannot pass by following it silently.
			var followed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/token" {
					followed.Store(true)
					http.Error(w, "the redirect was followed", http.StatusInternalServerError)
					return
				}
				if tc.redirect {
					w.Header().Set("Location", "/redirect-target")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)

			store, err := siwc.NewProfileStore(filepath.Join(t.TempDir(), "profiles"))
			if err != nil {
				t.Fatalf("NewProfileStore: %v", err)
			}
			record := siwc.ProfileRecord{
				ID:             "refresh-producer",
				Label:          "fixture",
				Subject:        "subject-refresh-producer",
				IssuedClientID: "issued-client-refresh-producer",
				HostID:         "urn:uuid:11111111-2222-4333-8444-555555555555",
				GrantedScopes:  []string{"openid", "chatgpt.tokens.use.direct"},
				AccessToken:    "producer-access-token-material",
				RefreshToken:   "producer-refresh-token-material",
				Expiry:         time.Now().Add(-time.Hour),
			}
			if err := store.SaveNew(record); err != nil {
				t.Fatalf("save the registration: %v", err)
			}
			refresher, err := siwc.NewRefresher(siwc.RefresherConfig{
				Profiles:      store,
				TokenEndpoint: server.URL + "/token",
				HTTPClient:    &http.Client{Timeout: 5 * time.Second},
			})
			if err != nil {
				t.Fatalf("NewRefresher: %v", err)
			}

			_, err = refresher.Refresh(context.Background(), record.ID)
			if err == nil {
				t.Fatal("the refresh succeeded, want a classified refresh failure")
			}
			// The fixture must be the production producer's own shape: the
			// typed class and the observed status are read before the
			// classifier sees them.
			var refreshErr *siwc.RefreshError
			if !errors.As(err, &refreshErr) {
				t.Fatalf("the refresh producer returned %T, want *siwc.RefreshError: %v", err, err)
			}
			if !errors.Is(err, tc.wantSentinel) {
				t.Fatalf("the refresh outcome is not the %v sentinel: %v", tc.wantSentinel, err)
			}
			if refreshErr.StatusCode != tc.wantStatus {
				t.Fatalf("the producer observed status %d, want %d", refreshErr.StatusCode, tc.wantStatus)
			}

			classified := requireClassified(t, err, tc.wantClass)
			if classified.Phase != responses.ErrorPhaseCredential {
				t.Fatalf("the refresh phase = %q, want %q", classified.Phase, responses.ErrorPhaseCredential)
			}
			if classified.Source != tc.wantSource {
				// Reported without stopping, so a failure that also copies
				// a local discriminator into Code names both violations.
				t.Errorf("the refresh source = %q, want %q", classified.Source, tc.wantSource)
			}
			if classified.Status != tc.wantStatus {
				t.Fatalf("the preserved refresh status = %d, want %d", classified.Status, tc.wantStatus)
			}
			if classified.Code != tc.wantCode {
				t.Errorf("the refresh code = %q, want %q; a local discriminator is never a provider code",
					classified.Code, tc.wantCode)
			}
			if classified.Reason != tc.wantReason {
				t.Fatalf("the refresh reason = %q, want %q", classified.Reason, tc.wantReason)
			}
			if classified.Retryable != tc.wantRetryable {
				t.Fatalf("the retryable flag = %t, want %t", classified.Retryable, tc.wantRetryable)
			}
			if followed.Load() {
				t.Fatal("the production refresh client followed the redirect")
			}
			requireNoLeak(t, classified, record.AccessToken, record.RefreshToken)
		})
	}
}

// TestClassify_ClassifiesTheProviderAnswerNotTheRequestShape proves the class
// is read from the actual answer, never inferred from the request.
//
// The same call carries an unsupported generation option (omitted under the
// interoperable posture); a 503 answer is a temporary condition and a 429
// answer is usage exhaustion. A classifier that inferred a class from the
// request shape would report an unsupported capability for both.
func TestClassify_ClassifiesTheProviderAnswerNotTheRequestShape(t *testing.T) {
	t.Parallel()
	maxTokens := 128
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
		Options: aipapi.GenerationOptions{MaxOutputTokens: &maxTokens},
	}
	admission, err := responses.AdmitCall(
		responses.ProjectInput{Call: call, Model: "gpt-6.1-codex"},
		responses.PolicyModeInteroperable,
	)
	if err != nil {
		t.Fatalf("AdmitCall: %v", err)
	}
	if len(admission.Omissions) == 0 {
		t.Fatal("the fixture request carries no omitted control, so the check-vs-assume proof would be vacuous")
	}

	cases := []struct {
		status    int
		code      string
		wantClass responses.ErrorClass
	}{
		{status: http.StatusServiceUnavailable, code: "subscription_sharing_user_unavailable", wantClass: responses.ErrorClassTemporaryUnavailable},
		{status: http.StatusTooManyRequests, code: documentedUsageCode, wantClass: responses.ErrorClassUsageExhausted},
	}
	for _, tc := range cases {
		reference := answerProvisionedProvider(t, tc.status, tc.code)
		client := newReferenceClient(t, reference, newSelectedAuthority())
		stream, err := client.OpenStream(context.Background(), admission.Request)
		if stream != nil {
			_ = stream.Close()
		}
		classified := requireClassified(t, err, tc.wantClass)
		if classified.Status != tc.status {
			t.Fatalf("the preserved status = %d, want %d", classified.Status, tc.status)
		}
	}
}

// TestClassify_UnclassifiableOutcomesAreNotGuessed proves the classifier does
// not invent a class for something outside the connector's failure surface.
func TestClassify_UnclassifiableOutcomesAreNotGuessed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{name: "nil", err: nil},
		{name: "an unrelated error", err: errors.New("boom")},
		{name: "caller cancellation", err: context.Canceled},
		{name: "caller deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)},
		{
			name: "caller cancellation observed by a refresh exchange",
			err:  siwc.NewRefreshError(siwc.RefreshClassTransient, 0, "", "", context.Canceled),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if classified, ok := responses.Classify(tc.err); ok || classified != nil {
				t.Fatalf("Classify(%v) = %#v, %t; want it unclassified", tc.err, classified, ok)
			}
		})
	}
}

// TestClassify_IsDeterministic proves one failure classifies identically.
func TestClassify_IsDeterministic(t *testing.T) {
	t.Parallel()
	reference := answerProvisionedProvider(t, http.StatusTooManyRequests, documentedUsageCode)
	client := newReferenceClient(t, reference, newSelectedAuthority())
	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("the provider refused the request, want an error")
	}
	first := requireClassified(t, err, responses.ErrorClassUsageExhausted)
	second := requireClassified(t, err, responses.ErrorClassUsageExhausted)
	if *first != *second {
		t.Fatalf("two classifications of one failure differ:\nfirst  = %#v\nsecond = %#v", first, second)
	}
	if first.Error() != second.Error() {
		t.Fatalf("two renderings of one failure differ: %q vs %q", first.Error(), second.Error())
	}
}
