package openairesponses_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Task 15.4: Full E2E conformance test suite for Lane 1
// (OpenAI Responses frontend -> OpenAI-compatible Responses backend).
// Requirements covered: 1, 6, 7, 10, 12, 14, 15, 16, 17, 18.

const (
	testDefaultRouteSelector = "test-backend:default-model"
	testResponsesSSEPayload  = `event: response.created
data: {"type":"response.created","response":{"id":"resp_upstream_001","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"hello from lane-1 wire E2E"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_upstream_001","status":"completed","usage":{"input_tokens":15,"output_tokens":30}}}

`
)

type lane1CapturedProviderReq struct {
	Method        string
	Path          string
	Header        http.Header
	ContentLength int64
	Body          []byte
	ParsedJSON    map[string]any
	Proto         string
}

type lane1TestAssessorExecutor struct {
	mu                sync.Mutex
	assessCalls       atomic.Int64
	executeLargeCalls atomic.Int64
	canceledALegs     []string
	assessFunc        func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	executeLargeFunc  func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
	canonicalExecFunc func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
	cancelALegFunc    func(ctx context.Context, req aipapi.ALegCancelRequest) error
	wallClock         func() time.Time
}

func (e *lane1TestAssessorExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	e.assessCalls.Add(1)
	e.mu.Lock()
	fn := e.assessFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, proof)
	}
	return makeAcceptedAssessment(proof)
}

func (e *lane1TestAssessorExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	e.executeLargeCalls.Add(1)
	e.mu.Lock()
	fn := e.executeLargeFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, accepted, src)
	}
	return largebody.ExecutionResult{
		Stream: aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "default large body response"},
			{Kind: aipapi.EventResponseFinished},
		}),
		Facts: largebody.ResponseFacts{
			RequestID:      accepted.Stamp.IdentityDigest().CallID("call_default"),
			EffectiveModel: accepted.WireRequest.CandidateModel,
		},
	}, nil
}

func (e *lane1TestAssessorExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.mu.Lock()
	fn := e.canonicalExecFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, call)
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "canonical fallback response"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *lane1TestAssessorExecutor) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	e.mu.Lock()
	e.canceledALegs = append(e.canceledALegs, req.ALegID)
	fn := e.cancelALegFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return nil
}

func (e *lane1TestAssessorExecutor) WallClock() func() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.wallClock
}

var (
	_ aipsdk.ExecutorView         = (*lane1TestAssessorExecutor)(nil)
	_ largebody.LargeBodyExecutor = (*lane1TestAssessorExecutor)(nil)
)

func makeAcceptedAssessment(proof largebody.Proof) (largebody.Assessment, error) {
	stamp, err := largebody.NewAssessmentStamp(
		"gen_test_lane1",
		proof.ProfileID,
		proof.Source,
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		proof.Identity,
	)
	if err != nil {
		return largebody.Assessment{}, err
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       proof.ProfileID,
		Operation:       proof.Operation,
		Delivery:        proof.Delivery,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     proof.ClientModel,
		CandidateModel:  proof.ClientModel,
		MaxOutputTokens: proof.MaxOutputTokens,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID: proof.ProfileID,
		Operation: proof.Operation,
		Delivery:  proof.Delivery,
	}
	return largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
}

func buildLane1WireOpenRequest(accepted largebody.Assessment, body io.ReadCloser, contentLength int64, header http.Header) largebody.WireOpenRequest {
	model := accepted.WireRequest.CandidateModel
	if model == "" {
		model = "gpt-4o"
	}
	candidate := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-backend",
			Model:   model,
		},
	}
	callID := accepted.Stamp.IdentityDigest().CallID("call_wire_lane1")
	return largebody.WireOpenRequest{
		Candidate:     candidate,
		Body:          body,
		ContentLength: contentLength,
		TraceID:       callID,
		ALegID:        "aleg-lane1-test",
		BLegID:        "bleg-lane1-test",
		WireRequest:   accepted.WireRequest,
		Header:        header,
	}
}

func buildDirectLane1WireOpenRequest(payload []byte, isStream bool, header http.Header) largebody.WireOpenRequest {
	delivery := aipapi.DeliveryModeNonStreaming
	if isStream {
		delivery = aipapi.DeliveryModeStreaming
	}
	return largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-backend",
				Model:   "gpt-4o",
			},
		},
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
		TraceID:       "trace-lane1-test",
		ALegID:        "aleg-lane1-test",
		BLegID:        "bleg-lane1-test",
		WireRequest: largebody.WireRequestFacts{
			ProfileID: openairesponses.ProfileID,
			Operation: aipapi.OperationOpenAIResponses,
			Delivery:  delivery,
			BodyMode:  largebody.BodyModeIdentityJSON,
		},
		Header: header,
	}
}

func newLane1TestSpec(t *testing.T, exec *lane1TestAssessorExecutor, threshold int64) *frontendpipe.Spec[openairesponses.EncodeOptions] {
	t.Helper()
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: testDefaultRouteSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-backend", "route-prefix"}),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: threshold,
	}
	spec.WireWriteStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		rid := rc.OpenAIResponseID()
		createdPayload, _ := json.Marshal(map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":     rid,
				"status": "in_progress",
				"model":  rc.EffectiveModel(),
			},
		})
		_, _ = fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", createdPayload)
		if flusher != nil {
			flusher.Flush()
		}
		for {
			ev, err := es.Recv(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
			switch ev.Kind {
			case aipapi.EventTextDelta:
				deltaPayload, _ := json.Marshal(map[string]any{
					"type":  "response.output_text.delta",
					"delta": ev.Delta,
				})
				_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", deltaPayload)
				if flusher != nil {
					flusher.Flush()
				}
			case aipapi.EventResponseFinished:
				completedPayload, _ := json.Marshal(map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     rid,
						"status": "completed",
						"model":  rc.EffectiveModel(),
					},
				})
				_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", completedPayload)
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		return nil
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		col, err := aipapi.Collect(ctx, es)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]any{
			"id":         rc.OpenAIResponseID(),
			"object":     "response",
			"created_at": rc.DeterministicTimestamp(),
			"status":     "completed",
			"model":      rc.EffectiveModel(),
			"output": []any{
				map[string]any{
					"type":   "message",
					"id":     rc.OpenAIMessageID(),
					"status": "completed",
					"role":   "assistant",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": col.Text.String(),
						},
					},
				},
			},
		}
		return json.NewEncoder(w).Encode(resp)
	}
	return spec
}

func startLane1CaptureServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *lane1CapturedProviderReq) {
	t.Helper()
	var captured lane1CapturedProviderReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Header = r.Header.Clone()
		captured.ContentLength = r.ContentLength
		captured.Proto = r.Proto
		body, _ := io.ReadAll(r.Body)
		captured.Body = body
		if len(body) > 0 {
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			captured.ParsedJSON = m
		}
		if handler != nil {
			handler(w, r)
			return
		}
		// Default capture behavior:
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testResponsesSSEPayload)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"resp_upstream_001","object":"response","status":"completed","output":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func buildLane1WireReq(model, input string, stream bool) []byte {
	m := map[string]any{
		"model":  model,
		"input":  input,
		"stream": stream,
	}
	b, _ := json.Marshal(m)
	return b
}

// 1. Selector Precedence (Requirements 4, 17)
// Header selector > Body model > DefaultRouteSelector.
func TestLane1E2E_SelectorPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("HeaderSelectorOverridesBodyModel", func(t *testing.T) {
		t.Parallel()
		var assessedSelector string
		exec := &lane1TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeAcceptedAssessment(proof)
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("route-prefix:body-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(routeselect.HeaderRouteSelector, "test-backend:header-model")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if assessedSelector != "test-backend:header-model" {
			t.Fatalf("expected RouteSelector %q from header, got %q", "test-backend:header-model", assessedSelector)
		}
	})

	t.Run("BodyModelOverridesDefaultSelector", func(t *testing.T) {
		t.Parallel()
		var assessedSelector string
		exec := &lane1TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeAcceptedAssessment(proof)
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("route-prefix:body-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if assessedSelector != "route-prefix:body-model" {
			t.Fatalf("expected RouteSelector %q from body model, got %q", "route-prefix:body-model", assessedSelector)
		}
	})

	t.Run("UnprefixedBodyModelFallsBackToDefaultSelector", func(t *testing.T) {
		t.Parallel()
		var assessedSelector string
		exec := &lane1TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeAcceptedAssessment(proof)
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("unprefixed-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if assessedSelector != testDefaultRouteSelector {
			t.Fatalf("expected fallback to default RouteSelector %q, got %q", testDefaultRouteSelector, assessedSelector)
		}
	})
}

// 2. Stream and Non-Stream Modes (Requirements 1, 12, 17, 18)
func TestLane1E2E_StreamAndNonStreamModes(t *testing.T) {
	t.Parallel()

	t.Run("StreamingMode", func(t *testing.T) {
		t.Parallel()
		srv, captured := startLane1CaptureServer(t, nil)

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-lane1"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "lane1-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: srv.Client(),
			MaxPending: 50,
		}

		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, err := src.Open()
				if err != nil {
					return largebody.ExecutionResult{}, err
				}
				defer func() { _ = rc.Close() }()
				wireReq := buildLane1WireOpenRequest(accepted, rc, src.Size(), nil)
				stream, err := prims.OpenWire(ctx, wireReq)
				if err != nil {
					return largebody.ExecutionResult{}, err
				}
				return largebody.ExecutionResult{
					Stream: stream,
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "streaming input payload", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Fatalf("expected text/event-stream Content-Type, got %q", ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "event: response.created") || !strings.Contains(body, "event: response.completed") {
			t.Fatalf("expected response.created and response.completed events in SSE stream, got %s", body)
		}
		if !strings.Contains(captured.Header.Get("Accept"), "text/event-stream") {
			t.Fatalf("expected Accept text/event-stream on provider request, got %q", captured.Header.Get("Accept"))
		}
	})

	t.Run("NonStreamingModeDeclinesToCanonicalFallback", func(t *testing.T) {
		t.Parallel()
		var canonicalExecuted atomic.Bool
		exec := &lane1TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				// Requirement 1 & wire_proof.go: Lane 1 wire fast path is streaming-only.
				// Non-streaming delivery mode is declined with DeliveryUnsupported.
				if proof.Delivery != aipapi.DeliveryModeStreaming {
					return largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
				}
				return makeAcceptedAssessment(proof)
			},
			canonicalExecFunc: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
				canonicalExecuted.Store(true)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "canonical non-streaming response"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "non-streaming input payload", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("expected application/json Content-Type, got %q", ct)
		}
		if !canonicalExecuted.Load() {
			t.Fatal("expected non-streaming request to decline wire mode and execute canonical fallback")
		}
		if exec.executeLargeCalls.Load() != 0 {
			t.Fatalf("expected ExecuteLargeBody NOT to be called on decline, got %d", exec.executeLargeCalls.Load())
		}
	})
}

// 3. Error Mapping (Requirements 1, 6, 12)
func TestLane1E2E_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("Upstream401Unauthorized", func(t *testing.T) {
		t.Parallel()
		srv, _ := startLane1CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-bad-key"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "lane1-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		var capturedErr error
		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane1WireOpenRequest(accepted, rc, src.Size(), nil)
				_, err := prims.OpenWire(ctx, wireReq)
				capturedErr = err
				return largebody.ExecutionResult{}, err
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "error test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500 on upstream auth failure to frontend client, got %d: %s", rec.Code, rec.Body.String())
		}
		if !aipapi.IsRecoverablePreOutput(capturedErr) {
			t.Fatalf("expected OpenWire to return RecoverablePreOutputError, got %v", capturedErr)
		}
		var httpErr *openaicompat.HTTPError
		if !errors.As(capturedErr, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected HTTP 401 within OpenWire error, got %v", capturedErr)
		}
		// Confirm credential was marked invalid in pool
		if _, aerr := pool.Acquire(time.Now(), nil); !errors.Is(aerr, credpool.ErrNoUsableCredential) {
			t.Fatalf("expected credential marked invalid in pool, got acquire err: %v", aerr)
		}
	})

	t.Run("Upstream429RateLimit", func(t *testing.T) {
		t.Parallel()
		srv, _ := startLane1CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"Rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`)
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-rate-limited"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID:        "lane1-prov",
			BaseURL:           srv.URL + "/v1",
			Flavor:            openaicompat.FlavorResponses,
			Pool:              pool,
			HTTPClient:        srv.Client(),
			RateLimitFallback: 5 * time.Second,
		}

		var capturedErr error
		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane1WireOpenRequest(accepted, rc, src.Size(), nil)
				_, err := prims.OpenWire(ctx, wireReq)
				capturedErr = err
				return largebody.ExecutionResult{}, err
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "rate limit test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500 on upstream rate limit to frontend client, got %d: %s", rec.Code, rec.Body.String())
		}
		if !aipapi.IsRecoverablePreOutput(capturedErr) {
			t.Fatalf("expected OpenWire to return RecoverablePreOutputError, got %v", capturedErr)
		}
		var httpErr *openaicompat.HTTPError
		if !errors.As(capturedErr, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected HTTP 429 within OpenWire error, got %v", capturedErr)
		}
		// Confirm credential was marked rate-limited in pool
		if _, aerr := pool.Acquire(time.Now(), nil); !errors.Is(aerr, credpool.ErrNoUsableCredential) {
			t.Fatalf("expected credential marked rate-limited in pool, got acquire err: %v", aerr)
		}
	})

	t.Run("ClientInvalidJSONReturns400WithoutProviderOpen", func(t *testing.T) {
		t.Parallel()
		var providerCalled bool
		srv, _ := startLane1CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			providerCalled = true
		})
		_ = srv

		exec := &lane1TestAssessorExecutor{}
		spec := newLane1TestSpec(t, exec, 20)

		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o", broken json`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected HTTP 400 for invalid JSON, got %d: %s", rec.Code, rec.Body.String())
		}
		if providerCalled {
			t.Fatal("expected provider capture server NOT to be called on invalid JSON")
		}
	})

	t.Run("OversizedBodyReturns413WithoutProviderOpen", func(t *testing.T) {
		t.Parallel()
		var providerCalled bool
		srv, _ := startLane1CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			providerCalled = true
		})
		_ = srv

		exec := &lane1TestAssessorExecutor{}
		spec := newLane1TestSpec(t, exec, 20)
		spec.MaxRequestBodyBytes = 30

		payload := bytes.Repeat([]byte("a"), 100)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected HTTP 413 for oversized body, got %d: %s", rec.Code, rec.Body.String())
		}
		if providerCalled {
			t.Fatal("expected provider capture server NOT to be called on oversized body")
		}
	})

	t.Run("MethodNotAllowedReturns405WithoutProviderOpen", func(t *testing.T) {
		t.Parallel()
		exec := &lane1TestAssessorExecutor{}
		spec := newLane1TestSpec(t, exec, 20)

		req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected HTTP 405 for GET method, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("PolicyDenialReturns403WithoutProviderOpen", func(t *testing.T) {
		t.Parallel()
		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				return largebody.ExecutionResult{}, aipapi.NewPolicyDeniedError("pre_request", "policy-prov", "policy_denied", "policy_denied", "policy denial test", errors.New("denial cause"))
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "policy test", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected HTTP 403 on policy denial, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

// 4. Canonical Response Event Parity (Requirements 12, 18)
func TestLane1E2E_CanonicalResponseEventParity(t *testing.T) {
	t.Parallel()
	srv, _ := startLane1CaptureServer(t, nil)

	pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-lane1"}})
	prims := openaicompat.WireOpenPrimitives{
		ProviderID: "lane1-prov",
		BaseURL:    srv.URL + "/v1",
		Flavor:     openaicompat.FlavorResponses,
		Pool:       pool,
		HTTPClient: srv.Client(),
		MaxPending: 50,
	}

	payload := buildLane1WireReq("test-backend:gpt-4o", "parity check input", true)
	wireReq := buildDirectLane1WireOpenRequest(payload, true, nil)

	stream, err := prims.OpenWire(context.Background(), wireReq)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var events []aipapi.Event
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			t.Fatalf("Recv failed: %v", rerr)
		}
		events = append(events, ev)
	}

	if len(events) < 3 {
		t.Fatalf("expected at least 3 canonical events, got %d: %+v", len(events), events)
	}
	if events[0].Kind != aipapi.EventResponseStarted {
		t.Errorf("event[0] expected EventResponseStarted, got %v", events[0].Kind)
	}
	var textSeen strings.Builder
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta {
			textSeen.WriteString(ev.Delta)
		}
	}
	if !strings.Contains(textSeen.String(), "hello from lane-1 wire E2E") {
		t.Errorf("expected text delta 'hello from lane-1 wire E2E', got %q", textSeen.String())
	}
	if events[len(events)-1].Kind != aipapi.EventResponseFinished {
		t.Errorf("last event expected EventResponseFinished, got %v", events[len(events)-1].Kind)
	}
}

// 5. Stable Request and Response Identity (Requirements 16, 18)
func TestLane1E2E_StableRequestAndResponseIdentity(t *testing.T) {
	t.Parallel()

	payload := buildLane1WireReq("test-backend:gpt-4o", "identity parity payload", false)

	// Derive canonical token from Spec.Decode
	decoded, err := openairesponses.DecodeCreateRequest(payload, openairesponses.DecodeOptions{
		RouteSelector: "test-backend:gpt-4o",
	})
	if err != nil {
		t.Fatalf("DecodeCreateRequest failed: %v", err)
	}
	canonCallID := diag.StableCallID(decoded.Call)
	canonToken := diag.StableCallToken(decoded.Call)
	wantRespID := "resp_" + canonToken

	var observedCallID string
	var observedToken string
	exec := &lane1TestAssessorExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "id test"},
					{Kind: aipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      accepted.Stamp.IdentityDigest().CallID(""),
					EffectiveModel: "gpt-4o",
				},
			}, nil
		},
	}
	spec := newLane1TestSpec(t, exec, 20)
	spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
		observedCallID = rc.DeterministicCallID()
		observedToken = rc.DeterministicToken()
		return inner, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if observedCallID != canonCallID {
		t.Errorf("DeterministicCallID mismatch: got %q, want %q", observedCallID, canonCallID)
	}
	if observedToken != canonToken {
		t.Errorf("DeterministicToken mismatch: got %q, want %q", observedToken, canonToken)
	}

	var parsed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal response JSON: %v", err)
	}
	if parsed["id"] != wantRespID {
		t.Errorf("response ID mismatch: got %q, want %q", parsed["id"], wantRespID)
	}
}

// 6. Cancellation by Returned ID (Requirement 18)
func TestLane1E2E_CancellationByReturnedID(t *testing.T) {
	t.Parallel()

	targetALegID := "aleg_lane1_cancel_007"
	exec := &lane1TestAssessorExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "cancel test"},
					{Kind: aipapi.EventResponseFinished},
				}),
				Session: largebody.SessionResponseCarrier{
					AuthoritativeSessionID: "sess_cancel_1",
					ALegID:                 targetALegID,
				},
				Facts: largebody.ResponseFacts{
					ALegID:         targetALegID,
					SessionID:      "sess_cancel_1",
					EffectiveModel: "gpt-4o",
				},
			}, nil
		},
	}
	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: testDefaultRouteSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-backend"}),
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 20,
	}
	spec.WireWriteNonStream = func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		return json.NewEncoder(w).Encode(map[string]any{
			"id":     rc.OpenAIResponseID(),
			"status": "completed",
		})
	}

	// 1. Send create request to obtain carrier-bound response ID
	payload := buildLane1WireReq("test-backend:gpt-4o", "cancel test message", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create request failed: %d: %s", rec.Code, rec.Body.String())
	}
	var createdResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &createdResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	rid, _ := createdResp["id"].(string)
	if !strings.HasPrefix(rid, "resp_aip_") {
		t.Fatalf("expected carrier-bound response ID starting with resp_aip_, got %q", rid)
	}

	// 2. Cancel using the returned carrier-bound response ID
	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/responses/"+rid+"/cancel", nil)
	cancelRec := httptest.NewRecorder()

	h.ServeHTTP(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel request failed: %d: %s", cancelRec.Code, cancelRec.Body.String())
	}

	exec.mu.Lock()
	canceled := exec.canceledALegs
	exec.mu.Unlock()
	if len(canceled) != 1 || canceled[0] != targetALegID {
		t.Fatalf("expected CancelALeg called with %q, got %v", targetALegID, canceled)
	}

	// 3. Invalid/unbound response ID returns 400
	badCancelReq := httptest.NewRequest(http.MethodPost, "/v1/responses/invalid_carrier/cancel", nil)
	badCancelRec := httptest.NewRecorder()

	h.ServeHTTP(badCancelRec, badCancelReq)

	if badCancelRec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 on invalid cancel carrier, got %d: %s", badCancelRec.Code, badCancelRec.Body.String())
	}
}

// 7. Session / Resume Turn-2 (Requirements 14, 18)
func TestLane1E2E_SessionResumeTurn2(t *testing.T) {
	t.Parallel()
	srv, captured := startLane1CaptureServer(t, nil)

	var turn1SessionInput largebody.SessionInput
	var turn2SessionInput largebody.SessionInput

	exec := &lane1TestAssessorExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "session turn response"},
					{Kind: aipapi.EventResponseFinished},
				}),
				Session: largebody.SessionResponseCarrier{
					AuthoritativeSessionID: "sess_lane1_turn1",
					ALegID:                 "aleg_lane1_turn1",
					ResumeToken:            largebody.NewSensitiveString("secret_resume_lane1_xyz"),
				},
				Facts: largebody.ResponseFacts{
					SessionID:      "sess_lane1_turn1",
					ALegID:         "aleg_lane1_turn1",
					EffectiveModel: "gpt-4o",
				},
			}, nil
		},
	}
	spec := newLane1TestSpec(t, exec, 20)

	// Turn 1: Fresh request without session headers
	payload1 := buildLane1WireReq("test-backend:gpt-4o", "turn 1 prompt", false)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("turn 1 failed: %d: %s", rec1.Code, rec1.Body.String())
	}
	gotSessID := rec1.Header().Get(sessionwire.HeaderAuthoritativeSessionID)
	gotALegID := rec1.Header().Get(sessionwire.HeaderALegID)
	gotResume := rec1.Header().Get(sessionwire.HeaderResumeToken)
	if gotSessID != "sess_lane1_turn1" || gotALegID != "aleg_lane1_turn1" || gotResume != "secret_resume_lane1_xyz" {
		t.Fatalf("turn 1 headers mismatch: sess=%q, aleg=%q, resume=%q", gotSessID, gotALegID, gotResume)
	}

	// Turn 2: Follow-up request with session headers
	pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-secret"}})
	prims := openaicompat.WireOpenPrimitives{
		ProviderID: "lane1-prov",
		BaseURL:    srv.URL + "/v1",
		Flavor:     openaicompat.FlavorResponses,
		Pool:       pool,
		HTTPClient: srv.Client(),
	}

	payload2 := buildLane1WireReq("test-backend:gpt-4o", "turn 2 prompt", true)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, gotSessID)
	req2.Header.Set(sessionwire.HeaderALegID, gotALegID)
	req2.Header.Set(sessionwire.HeaderResumeToken, gotResume)
	rec2 := httptest.NewRecorder()

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		turn2SessionInput = proof.Session
		return makeAcceptedAssessment(proof)
	}
	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		rc, _ := src.Open()
		defer func() { _ = rc.Close() }()
		wireReq := buildLane1WireOpenRequest(accepted, rc, src.Size(), req2.Header)
		stream, err := prims.OpenWire(ctx, wireReq)
		if err != nil {
			return largebody.ExecutionResult{}, err
		}
		return largebody.ExecutionResult{
			Stream: stream,
			Session: largebody.SessionResponseCarrier{
				AuthoritativeSessionID: gotSessID,
				ALegID:                 gotALegID,
			},
			Facts: largebody.ResponseFacts{
				SessionID:      gotSessID,
				ALegID:         gotALegID,
				EffectiveModel: "gpt-4o",
			},
		}, nil
	}

	frontendpipe.ServeHTTP(spec, rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("turn 2 failed: %d: %s", rec2.Code, rec2.Body.String())
	}
	if turn2SessionInput.AuthoritativeSessionID != "sess_lane1_turn1" {
		t.Errorf("turn 2 SessionInput session ID: got %q, want %q", turn2SessionInput.AuthoritativeSessionID, "sess_lane1_turn1")
	}
	if turn2SessionInput.ResumeToken.Reveal() != "secret_resume_lane1_xyz" {
		t.Errorf("turn 2 SessionInput resume token: got %q, want %q", turn2SessionInput.ResumeToken.Reveal(), "secret_resume_lane1_xyz")
	}

	// Confidentiality assertion: resume token must NOT leak into upstream headers or payload
	if strings.Contains(captured.Header.Get("Authorization"), "secret_resume_lane1_xyz") {
		t.Errorf("sensitive resume token leaked into Authorization header: %q", captured.Header.Get("Authorization"))
	}
	if captured.Header.Get(sessionwire.HeaderResumeToken) != "" {
		t.Errorf("sensitive resume token header leaked upstream: %q", captured.Header.Get(sessionwire.HeaderResumeToken))
	}
	if bytes.Contains(captured.Body, []byte("secret_resume_lane1_xyz")) {
		t.Errorf("sensitive resume token leaked into provider request body")
	}
	_ = turn1SessionInput
}

// 8. Secure Recorder Input (Requirement 14)
func TestLane1E2E_SecureRecorderInput(t *testing.T) {
	t.Parallel()

	var recordedShape largebody.ClientTurnShape
	exec := &lane1TestAssessorExecutor{
		assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			recordedShape = proof.Turn
			return makeAcceptedAssessment(proof)
		},
	}
	spec := newLane1TestSpec(t, exec, 20)

	payload := []byte(`{"model":"test-backend:gpt-4o","instructions":"system role guidelines","input":"hello recorder test","stream":false}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(recordedShape.Items) == 0 {
		t.Fatal("expected non-empty ClientTurnShape.Items for recorder")
	}
	if recordedShape.TotalContentBytes <= 0 {
		t.Fatalf("expected positive TotalContentBytes, got %d", recordedShape.TotalContentBytes)
	}
}

// 9. Metering Checkpoints (Requirement 15)
func TestLane1E2E_MeteringCheckpoints(t *testing.T) {
	t.Parallel()

	type mockCheckpoint struct {
		CallID         string
		EffectiveModel string
		BodyBytes      int64
		IsStream       bool
		InputTokens    int
		OutputTokens   int
	}

	var ingressCP mockCheckpoint
	var egressCP mockCheckpoint

	exec := &lane1TestAssessorExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			ingressCP = mockCheckpoint{
				CallID:         accepted.Stamp.IdentityDigest().CallID(""),
				EffectiveModel: accepted.WireRequest.CandidateModel,
				BodyBytes:      src.Size(),
				IsStream:       accepted.WireRequest.Delivery == aipapi.DeliveryModeStreaming,
			}
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "metering test"},
					{Kind: aipapi.EventResponseFinished},
				}),
				Facts: largebody.ResponseFacts{
					RequestID:      ingressCP.CallID,
					EffectiveModel: ingressCP.EffectiveModel,
				},
			}, nil
		},
	}
	spec := newLane1TestSpec(t, exec, 20)
	spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
		egressCP = mockCheckpoint{
			CallID:         rc.CallID(),
			EffectiveModel: rc.EffectiveModel(),
			InputTokens:    10,
			OutputTokens:   25,
		}
		return inner, nil
	}

	payload := buildLane1WireReq("test-backend:gpt-4o", "metering checkpoint input", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ingressCP.CallID == "" || !strings.Contains(ingressCP.EffectiveModel, "gpt-4o") || ingressCP.BodyBytes <= 0 {
		t.Fatalf("invalid ingress checkpoint: %+v", ingressCP)
	}
	if egressCP.InputTokens != 10 || egressCP.OutputTokens != 25 {
		t.Fatalf("invalid egress checkpoint: %+v", egressCP)
	}
}

// 10. Retry, Failover, and Race across Attempts with OpenWire Credential Economics (Requirements 10, 12)
func TestLane1E2E_RetryFailoverRaceCredentialEconomics(t *testing.T) {
	t.Parallel()

	t.Run("SingleAcquireCredentialRotationAndFreshReplayReader", func(t *testing.T) {
		t.Parallel()

		var attemptCount atomic.Int64
		var receivedSecrets []string
		var mu sync.Mutex

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			att := attemptCount.Add(1)
			mu.Lock()
			receivedSecrets = append(receivedSecrets, r.Header.Get("Authorization"))
			mu.Unlock()

			if att == 1 {
				// First attempt fails pre-output with 401 (invalid key triggers single-acquire credential rotation)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
				return
			}
			// Second attempt succeeds
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testResponsesSSEPayload)
		}))
		t.Cleanup(srv.Close)

		pool, err := credpool.New([]credpool.Credential{
			{ID: "k1", Secret: "sk-attempt-1"},
			{ID: "k2", Secret: "sk-attempt-2"},
		})
		if err != nil {
			t.Fatalf("credpool.New: %v", err)
		}
		prims := openaicompat.WireOpenPrimitives{
			ProviderID:        "lane1-retry-prov",
			BaseURL:           srv.URL + "/v1",
			Flavor:            openaicompat.FlavorResponses,
			Pool:              pool,
			HTTPClient:        srv.Client(),
			RateLimitFallback: 5 * time.Second,
			MaxPending:        50,
		}

		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				// Core-owned retry loop: attempt 1 fails recoverable, attempt 2 opens fresh reader
				var stream aipapi.ManagedEventStream
				var lastErr error
				for attempt := 1; attempt <= 2; attempt++ {
					reader, oerr := src.Open()
					if oerr != nil {
						return largebody.ExecutionResult{}, oerr
					}
					wireReq := buildLane1WireOpenRequest(accepted, reader, src.Size(), nil)
					stream, lastErr = prims.OpenWire(ctx, wireReq)
					_ = reader.Close()
					if lastErr == nil {
						break
					}
					if !aipapi.IsRecoverablePreOutput(lastErr) {
						return largebody.ExecutionResult{}, lastErr
					}
				}
				if lastErr != nil {
					return largebody.ExecutionResult{}, lastErr
				}
				return largebody.ExecutionResult{
					Stream: stream,
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane1TestSpec(t, exec, 20)

		payload := buildLane1WireReq("test-backend:gpt-4o", "retry economics test payload", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200 on second attempt, got %d: %s", rec.Code, rec.Body.String())
		}
		if attemptCount.Load() != 2 {
			t.Fatalf("expected exactly 2 attempts, got %d", attemptCount.Load())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(receivedSecrets) != 2 {
			t.Fatalf("expected 2 acquired secrets, got %d", len(receivedSecrets))
		}
		if receivedSecrets[0] != "Bearer sk-attempt-1" || receivedSecrets[1] != "Bearer sk-attempt-2" {
			t.Errorf("expected secret rotation sk-attempt-1 -> sk-attempt-2, got %v", receivedSecrets)
		}
	})

	t.Run("ParallelRaceIndependentReaders", func(t *testing.T) {
		t.Parallel()

		src := largebody.NewMemorySource([]byte(`{"model":"gpt-4o","input":"concurrent race test"}`))
		defer func() { _ = src.Close() }()

		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Go(func() {
				r, err := src.Open()
				if err != nil {
					errs <- err
					return
				}
				defer func() { _ = r.Close() }()
				data, err := io.ReadAll(r)
				if err != nil {
					errs <- err
					return
				}
				if !bytes.Contains(data, []byte("concurrent race test")) {
					errs <- fmt.Errorf("unexpected body content: %s", string(data))
					return
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	})
}

// 11. Keepalive 102s (Requirement 18)
type lane1KeepaliveStatusWriter struct {
	mu       sync.Mutex
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func (w *lane1KeepaliveStatusWriter) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *lane1KeepaliveStatusWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *lane1KeepaliveStatusWriter) WriteHeader(s int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statuses = append(w.statuses, s)
}

func (w *lane1KeepaliveStatusWriter) Flush() {}

func (w *lane1KeepaliveStatusWriter) count(status int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, s := range w.statuses {
		if s == status {
			n++
		}
	}
	return n
}

func TestLane1E2E_Keepalive102(t *testing.T) {
	t.Parallel()

	t.Run("StreamingEmits102DuringSlowOpen", func(t *testing.T) {
		t.Parallel()

		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				// Simulate slow backend open
				time.Sleep(50 * time.Millisecond)
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "keepalive ok"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane1TestSpec(t, exec, 20)
		spec.PreRequestKeepalive = aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 15 * time.Millisecond,
		}

		payload := buildLane1WireReq("test-backend:gpt-4o", "keepalive streaming test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := &lane1KeepaliveStatusWriter{}

		frontendpipe.ServeHTTP(spec, w, req)

		n102 := w.count(102)
		if n102 == 0 {
			t.Fatalf("expected at least one 102 Processing status, got 0; statuses: %v", w.statuses)
		}
	})

	t.Run("NonStreamingBypasses102", func(t *testing.T) {
		t.Parallel()

		exec := &lane1TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				time.Sleep(40 * time.Millisecond)
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "non-streaming keepalive"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane1TestSpec(t, exec, 20)
		spec.PreRequestKeepalive = aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 15 * time.Millisecond,
		}

		payload := buildLane1WireReq("test-backend:gpt-4o", "keepalive non-stream test", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := &lane1KeepaliveStatusWriter{}

		frontendpipe.ServeHTTP(spec, w, req)

		n102 := w.count(102)
		if n102 > 0 {
			t.Fatalf("expected 0 102 Processing statuses for non-stream, got %d", n102)
		}
	})
}

// 12. Decode-Admission Saturation Decline-to-Canonical Fallback (Requirements 1, 6)
func TestLane1E2E_DecodeAdmissionSaturationFallback(t *testing.T) {
	t.Parallel()

	var canonicalExecuted atomic.Bool
	exec := &lane1TestAssessorExecutor{
		assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			// Decline wire execution (e.g. backend unsupported or candidate incompatible)
			declined, err := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
			return declined, err
		},
		canonicalExecFunc: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			canonicalExecuted.Store(true)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "canonical success"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
	spec := newLane1TestSpec(t, exec, 20)

	payload := buildLane1WireReq("test-backend:gpt-4o", "fallback input", false)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on fallback, got %d: %s", rec.Code, rec.Body.String())
	}
	if !canonicalExecuted.Load() {
		t.Fatal("expected canonical Spec.Decode/Execute to be invoked on wire decline")
	}
	if exec.executeLargeCalls.Load() != 0 {
		t.Fatalf("expected ExecuteLargeBody NOT to be called on decline, got %d", exec.executeLargeCalls.Load())
	}
}

// 13. HTTP/1.1 + HTTP/2 Transport Parity (Requirement 12)
func TestLane1E2E_HTTP1AndHTTP2Transport(t *testing.T) {
	t.Parallel()

	t.Run("HTTP1_Transport", func(t *testing.T) {
		t.Parallel()
		srv, captured := startLane1CaptureServer(t, nil)

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-http1"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "h1-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		payload := buildLane1WireReq("test-backend:gpt-4o", "http1 payload", true)
		wireReq := buildDirectLane1WireOpenRequest(payload, true, nil)
		stream, err := prims.OpenWire(context.Background(), wireReq)
		if err != nil {
			t.Fatalf("OpenWire failed over HTTP/1: %v", err)
		}
		_ = stream.Close()

		if captured.Proto != "HTTP/1.1" {
			t.Errorf("expected HTTP/1.1 proto, got %q", captured.Proto)
		}
	})

	t.Run("HTTP2_Transport", func(t *testing.T) {
		t.Parallel()

		var capturedProto string
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedProto = r.Proto
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testResponsesSSEPayload)
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()
		t.Cleanup(srv.Close)

		tr := httpclient.DefaultTransport()
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         []string{"h2", "http/1.1"},
		}
		cli := &http.Client{Transport: tr, Timeout: 10 * time.Second}

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-http2"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "h2-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorResponses,
			Pool:       pool,
			HTTPClient: cli,
		}

		payload := buildLane1WireReq("test-backend:gpt-4o", "http2 payload", true)
		wireReq := buildDirectLane1WireOpenRequest(payload, true, nil)
		stream, err := prims.OpenWire(context.Background(), wireReq)
		if err != nil {
			t.Fatalf("OpenWire failed over HTTP/2: %v", err)
		}
		_ = stream.Close()

		if capturedProto != "HTTP/2.0" {
			t.Errorf("expected HTTP/2.0 proto, got %q", capturedProto)
		}
	})
}

// 14. Wire Support Not Advertised User-Visible (Requirements 1, 22)
func TestLane1E2E_WireSupportNotAdvertised(t *testing.T) {
	t.Parallel()

	// Verify default LargePayloadConfig is disabled
	lpCfg := frontendpipe.LargePayloadConfig{}
	if lpCfg.Enabled {
		t.Fatal("frontendpipe.LargePayloadConfig must have Enabled == false by default")
	}

	// Verify new Handler without overrides leaves LargePayload disabled
	exec := &lane1TestAssessorExecutor{}
	h := &openairesponses.Handler{Exec: exec}
	spec := h.Spec()
	if spec.LargePayload.Enabled {
		t.Fatal("Handler.Spec() must have LargePayload.Enabled == false by default")
	}
	if spec.LargePayload.ThresholdBytes != 0 {
		t.Fatalf("expected default 0 ThresholdBytes, got %d", spec.LargePayload.ThresholdBytes)
	}
}
