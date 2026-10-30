package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	refacp "github.com/aiproxer/aiproxer/internal/refbackend/acp"
	testkitopenresponses "github.com/aiproxer/aiproxer/internal/testkit/openresponses"
)

// harnessFakeText is the deterministic assistant text emitted by harness
// contract-fake reference-provider origins. HarnessFakeText is the exported
// form for integration packages.
const (
	harnessFakeText = "harness-fake"
	HarnessFakeText = harnessFakeText
)

// OriginFailMode selects a deterministic contract-fake origin failure used for
// credential/failure injection through the deployment.
type OriginFailMode string

const (
	OriginFailNone         OriginFailMode = ""
	OriginFailUnauthorized OriginFailMode = "unauthorized"
	OriginFailServerError  OriginFailMode = "server_error"
	OriginFailMalformed    OriginFailMode = "malformed"
)

// Origin is the injectable reference-provider origin behind one real backend in
// a deployment. Base smoke deploys contract-fake origins (counter, bounded
// redacted capture, failure modes, virtual clock); existing reference families
// and the Phase 8 independent OpenResponses refbackend can be injected through
// [DeploymentSpec.ProviderOrigin] and are observed through the same counter and
// redacted-capture surface via a transparent observing proxy.
type Origin struct {
	srv        *httptest.Server
	backendID  string
	fail       OriginFailMode
	clock      testkitopenresponses.VirtualClock
	inject     *url.URL
	httpClient *http.Client

	count   atomic.Int64
	capture *testkitopenresponses.BoundedCapture

	closeOnce sync.Once
	closeErr  error
}

// URL returns the origin base URL the real backend is wired to.
func (o *Origin) URL() string {
	if o == nil || o.srv == nil {
		return ""
	}
	return o.srv.URL
}

// Addr returns the origin listen address (host:port). Prefer [Origin.Listener]
// for lifecycle assertions: a freed ephemeral port can be taken by another
// listener, so the address alone does not identify this origin after release.
func (o *Origin) Addr() string {
	if o == nil || o.srv == nil {
		return ""
	}
	raw := strings.TrimPrefix(o.srv.URL, "http://")
	raw = strings.TrimPrefix(raw, "https://")
	return strings.TrimSuffix(raw, "/")
}

// Listener returns the origin's listen listener for lifecycle assertions. It is
// the identity that stays stable across release: a freed ephemeral port may be
// taken by another listener, so asserting on [Origin.Addr] alone can report a
// released origin as still open.
func (o *Origin) Listener() net.Listener {
	if o == nil || o.srv == nil {
		return nil
	}
	return o.srv.Listener
}

// Client returns a loopback HTTP client bound to the origin server.
func (o *Origin) Client() *http.Client {
	if o == nil || o.srv == nil {
		return nil
	}
	return o.srv.Client()
}

// Count returns the number of requests the origin received. Injected external
// origins are observed through the proxy, so the count includes their requests.
func (o *Origin) Count() int {
	if o == nil {
		return 0
	}
	return int(o.count.Load())
}

// Capture returns the bounded, redacted request-capture artifacts.
func (o *Origin) Capture() []testkitopenresponses.RequestObservation {
	if o == nil || o.capture == nil {
		return nil
	}
	return o.capture.Observations()
}

// Clock returns the virtual clock injected into the origin.
func (o *Origin) Clock() testkitopenresponses.VirtualClock {
	if o == nil {
		return nil
	}
	return o.clock
}

// Close deterministically shuts the origin down. It is idempotent.
func (o *Origin) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		if o.srv != nil {
			o.srv.Close()
		}
	})
	return o.closeErr
}

// newHarnessOrigin deploys one observing reference-provider origin for
// backendID. Existing families reuse the repository reference-backend handlers;
// the OpenResponses family uses the harness contract fake until the independent
// refbackend lands in Phase 8. injectURL redirects all origin traffic to an
// external reference-provider origin through the observing proxy. custom, when
// non-nil, is the origin responder used instead of the family handler or proxy
// (adversarial Task 7.4 origins). limit bounds the redacted capture artifact
// count.
func newHarnessOrigin(
	tb testing.TB,
	backendID string,
	fail OriginFailMode,
	clock testkitopenresponses.VirtualClock,
	limit int,
	injectURL string,
	injectClient *http.Client,
	custom http.Handler,
) *Origin {
	tb.Helper()
	var inject *url.URL
	if strings.TrimSpace(injectURL) != "" {
		u, err := url.Parse(strings.TrimSpace(injectURL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			tb.Fatalf("harness: invalid injected provider origin %q: %v", injectURL, err)
		}
		inject = u
	}
	capLimit := limit
	if capLimit <= 0 {
		capLimit = 100
	}
	o := &Origin{
		backendID:  backendID,
		fail:       fail,
		clock:      clock,
		inject:     inject,
		httpClient: injectClient,
		capture:    testkitopenresponses.NewBoundedCapture(capLimit, []string{"Authorization", "x-api-key", "X-API-Key"}),
	}
	var inner http.Handler
	switch {
	case custom != nil:
		inner = custom
	case inject != nil:
		inner = &originProxy{target: inject, client: injectClient}
	default:
		inner = originFamilyHandler(tb, backendID, clock)
	}
	o.srv = httptest.NewServer(&observingOrigin{inner: inner, o: o})
	tb.Cleanup(func() { _ = o.Close() })
	return o
}

// observingOrigin wraps an inner family/proxy responder with the harness request
// counter, bounded redacted capture, and deterministic failure injection.
type observingOrigin struct {
	inner http.Handler
	o     *Origin
}

func (h *observingOrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	obs := testkitopenresponses.RequestObservation{
		Method:    r.Method,
		URLPath:   r.URL.Path,
		Headers:   r.Header.Clone(),
		Body:      body,
		Timestamp: originNow(h.o.clock),
	}
	_ = h.o.capture.Capture(obs)
	h.o.count.Add(1)

	switch h.o.fail {
	case OriginFailUnauthorized:
		writeFamilyError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid API key")
		return
	case OriginFailServerError:
		writeFamilyError(w, http.StatusInternalServerError, "server_error", "upstream_error", "upstream error")
		return
	case OriginFailMalformed:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"not-a-response","status":"weird"`))
		return
	}

	h.inner.ServeHTTP(w, r)
}

// originProxy transparently forwards origin traffic to an injected external
// reference-provider origin while preserving method, headers, and body so the
// counter and redacted capture observe every request.
type originProxy struct {
	target *url.URL
	client *http.Client
}

func (p *originProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	target := *p.target
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(r.URL.Path, "/")
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "origin proxy: bad forward request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	client := p.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "origin proxy: upstream unreachable", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// originFamilyHandler returns the deterministic reference-provider responder
// for one backend family. Existing families reuse the repository reference
// backends (the existing reference families); the OpenResponses family uses the
// harness contract-fake origin until the independent refbackend lands in Phase 8.
func originFamilyHandler(tb testing.TB, backendID string, clock testkitopenresponses.VirtualClock) http.Handler {
	tb.Helper()
	switch backendID {
	case BackendOpenResponses, BackendCompatibleOpenAI:
		return openResponsesFakeHandler{clock: clock}
	case BackendACP:
		return refacp.NewHandler(refacp.Config{})
	default:
		return parityRefHandler(tb, backendID)
	}
}

func originNow(clock testkitopenresponses.VirtualClock) time.Time {
	if clock != nil {
		return clock.Now()
	}
	return time.Unix(1715620000, 0).UTC()
}

func writeFamilyError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": message, "type": typ, "code": code},
	})
}

// openResponsesFakeHandler is the harness contract-fake OpenResponses provider
// origin. It serves non-streaming create resources, streaming SSE create, and
// compact resources with deterministic payloads under an injectable virtual
// clock. It is deliberately minimal and independent of the production codec:
// it is only a smoke origin until the independent refbackend lands in Phase 8.
type openResponsesFakeHandler struct {
	clock testkitopenresponses.VirtualClock
}

func (h openResponsesFakeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/responses/compact") {
		h.serveCompact(w)
		return
	}
	if isStreamingRequest(r) {
		h.serveStream(w)
		return
	}
	h.serveResource(w)
}

func isStreamingRequest(r *http.Request) bool {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream != nil && *probe.Stream
}

func (h openResponsesFakeHandler) created() int64 {
	if h.clock != nil {
		return h.clock.Now().Unix()
	}
	return 1715620000
}

func (h openResponsesFakeHandler) serveResource(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, fmt.Sprintf(`{
  "id": "resp_harness_1",
  "object": "response",
  "created_at": %d,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_harness_out",
      "status": "completed",
      "role": "assistant",
      "content": [{"type": "output_text", "text": %s}]
    }
  ],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`, h.created(), jsonString(harnessFakeText)))
}

func (h openResponsesFakeHandler) serveCompact(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, fmt.Sprintf(`{
  "id": "comp_harness_1",
  "object": "response.compaction",
  "created_at": %d,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_harness_compact",
      "status": "completed",
      "role": "assistant",
      "content": [{"type": "output_text", "text": %s}]
    }
  ]
}`, h.created(), jsonString(harnessFakeText)))
}

func (h openResponsesFakeHandler) serveStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	created := h.created()
	txt := jsonString(harnessFakeText)
	sse := "event: response.created\n" +
		"data: " + fmt.Sprintf(`{"type":"response.created","sequence_number":1,"response":{"id":"resp_harness_stream","object":"response","created_at":%d,"status":"in_progress","model":"gpt-4o-mini","output":[]}}`, created) + "\n\n" +
		"event: response.output_item.added\n" +
		"data: " + `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"message","id":"msg_harness_1","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":""}]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		"data: " + `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_harness_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		"data: " + fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_harness_1","output_index":0,"content_index":0,"delta":%s}`, txt) + "\n\n" +
		"event: response.output_text.done\n" +
		"data: " + fmt.Sprintf(`{"type":"response.output_text.done","sequence_number":5,"item_id":"msg_harness_1","output_index":0,"content_index":0,"text":%s}`, txt) + "\n\n" +
		"event: response.content_part.done\n" +
		"data: " + `{"type":"response.content_part.done","sequence_number":6,"item_id":"msg_harness_1","output_index":0,"content_index":0}` + "\n\n" +
		"event: response.output_item.done\n" +
		"data: " + fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"type":"message","id":"msg_harness_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":%s}]}}`, txt) + "\n\n" +
		"event: response.completed\n" +
		"data: " + fmt.Sprintf(`{"type":"response.completed","sequence_number":8,"response":{"id":"resp_harness_stream","object":"response","created_at":%d,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_harness_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":%s}]}]}}`, created, txt) + "\n\n" +
		"data: [DONE]\n\n"
	_, _ = io.WriteString(w, sse)
}

func jsonString(s string) string {
	return strconv.Quote(s)
}
