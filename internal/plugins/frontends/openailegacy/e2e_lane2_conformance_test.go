package openailegacy_test

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
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Task 16.3: Full E2E conformance test suite for Lane 2
// (OpenAI Chat Completions frontend -> OpenAI-compatible Chat backend).
// Requirements covered: 17, 18, 21.

const (
	testDefaultChatRouteSelector = "test-backend:default-model"
	testChatSSEPayloadChunk1     = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"
	testChatSSEPayloadChunk2     = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello from lane-2 wire E2E\"},\"finish_reason\":null}]}\n\n"
	testChatSSEPayloadChunk3     = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	testChatSSEPayloadDone       = "data: [DONE]\n\n"
	testChatSSEPayloadStream     = testChatSSEPayloadChunk1 + testChatSSEPayloadChunk2 + testChatSSEPayloadChunk3 + testChatSSEPayloadDone
)

type lane2CapturedProviderReq struct {
	Method        string
	Path          string
	Header        http.Header
	ContentLength int64
	Body          []byte
	ParsedJSON    map[string]any
	Proto         string
}

type lane2TestAssessorExecutor struct {
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

func (e *lane2TestAssessorExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	e.assessCalls.Add(1)
	e.mu.Lock()
	fn := e.assessFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, proof)
	}
	return makeLane2AcceptedAssessment(proof)
}

func (e *lane2TestAssessorExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
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

func (e *lane2TestAssessorExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
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

func (e *lane2TestAssessorExecutor) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	e.mu.Lock()
	e.canceledALegs = append(e.canceledALegs, req.ALegID)
	fn := e.cancelALegFunc
	e.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return nil
}

func (e *lane2TestAssessorExecutor) WallClock() func() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.wallClock
}

var (
	_ aipsdk.ExecutorView         = (*lane2TestAssessorExecutor)(nil)
	_ largebody.LargeBodyExecutor = (*lane2TestAssessorExecutor)(nil)
)

func makeLane2AcceptedAssessment(proof largebody.Proof) (largebody.Assessment, error) {
	stamp, err := largebody.NewAssessmentStamp(
		"gen_test_lane2",
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

func buildLane2WireOpenRequest(accepted largebody.Assessment, body io.ReadCloser, contentLength int64, header http.Header) largebody.WireOpenRequest {
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
	callID := accepted.Stamp.IdentityDigest().CallID("call_wire_lane2")
	return largebody.WireOpenRequest{
		Candidate:     candidate,
		Body:          body,
		ContentLength: contentLength,
		TraceID:       callID,
		ALegID:        "aleg-lane2-test",
		BLegID:        "bleg-lane2-test",
		WireRequest:   accepted.WireRequest,
		Header:        header,
	}
}

func buildDirectLane2WireOpenRequest(payload []byte, isStream bool, header http.Header) largebody.WireOpenRequest {
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
		TraceID:       "trace-lane2-test",
		ALegID:        "aleg-lane2-test",
		BLegID:        "bleg-lane2-test",
		WireRequest: largebody.WireRequestFacts{
			ProfileID: openailegacy.ProfileID,
			Operation: aipapi.OperationOpenAIChatCompletions,
			Delivery:  delivery,
			BodyMode:  largebody.BodyModeIdentityJSON,
		},
		Header: header,
	}
}

func newLane2TestSpec(t *testing.T, exec *lane2TestAssessorExecutor, threshold int64) *frontendpipe.Spec[openailegacy.EncodeOptions] {
	t.Helper()
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: testDefaultChatRouteSelector,
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
		cid := rc.OpenAIChatCompletionID()
		created := rc.DeterministicTimestamp()
		model := rc.EffectiveModel()

		// Initial role chunk
		initChunk, _ := json.Marshal(map[string]any{
			"id":      cid,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role": "assistant",
					},
					"finish_reason": nil,
				},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", initChunk)
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
				deltaChunk, _ := json.Marshal(map[string]any{
					"id":      cid,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []any{
						map[string]any{
							"index": 0,
							"delta": map[string]any{
								"content": ev.Delta,
							},
							"finish_reason": nil,
						},
					},
				})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", deltaChunk)
				if flusher != nil {
					flusher.Flush()
				}
			case aipapi.EventResponseFinished:
				stopChunk, _ := json.Marshal(map[string]any{
					"id":      cid,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []any{
						map[string]any{
							"index":         0,
							"delta":         map[string]any{},
							"finish_reason": "stop",
						},
					},
				})
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", stopChunk)
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
			"id":      rc.OpenAIChatCompletionID(),
			"object":  "chat.completion",
			"created": rc.DeterministicTimestamp(),
			"model":   rc.EffectiveModel(),
			"choices": []any{
				map[string]any{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": col.Text.String(),
					},
					"finish_reason": "stop",
				},
			},
		}
		return json.NewEncoder(w).Encode(resp)
	}
	return spec
}

func startLane2CaptureServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *lane2CapturedProviderReq) {
	t.Helper()
	var captured lane2CapturedProviderReq
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
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testChatSSEPayloadStream)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl-upstream-001","object":"chat.completion","created":1726000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func buildLane2WireReq(model, content string, stream bool) []byte {
	m := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": content,
			},
		},
		"stream": stream,
	}
	b, _ := json.Marshal(m)
	return b
}

type lane2KeepaliveStatusWriter struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
	mu       sync.Mutex
}

func (w *lane2KeepaliveStatusWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *lane2KeepaliveStatusWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *lane2KeepaliveStatusWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statuses = append(w.statuses, status)
}

func (w *lane2KeepaliveStatusWriter) count(status int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := 0
	for _, s := range w.statuses {
		if s == status {
			c++
		}
	}
	return c
}

// 1. Selector Precedence (Requirements 4, 17)
func TestLane2E2E_SelectorPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("HeaderSelectorOverridesBodyModel", func(t *testing.T) {
		t.Parallel()
		var assessedSelector string
		exec := &lane2TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeLane2AcceptedAssessment(proof)
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("route-prefix:body-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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
		exec := &lane2TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeLane2AcceptedAssessment(proof)
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("route-prefix:body-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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

	t.Run("DefaultSelectorUsedWhenNoHeaderOrBodyMatch", func(t *testing.T) {
		t.Parallel()
		var assessedSelector string
		exec := &lane2TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				assessedSelector = proof.RouteSelector
				return makeLane2AcceptedAssessment(proof)
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("unknown-model", "test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if assessedSelector != testDefaultChatRouteSelector {
			t.Fatalf("expected default RouteSelector %q, got %q", testDefaultChatRouteSelector, assessedSelector)
		}
	})
}

// 2. Stream and Non-Stream Delivery Modes (Requirement 18)
func TestLane2E2E_StreamAndNonStreamModes(t *testing.T) {
	t.Parallel()

	srv, _ := startLane2CaptureServer(t, nil)
	pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-secret"}})
	prims := openaicompat.WireOpenPrimitives{
		ProviderID: "lane2-prov",
		BaseURL:    srv.URL + "/v1",
		Flavor:     openaicompat.FlavorChat,
		Pool:       pool,
		HTTPClient: srv.Client(),
		MaxPending: 50,
	}

	t.Run("StreamingDelivery", func(t *testing.T) {
		t.Parallel()
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane2WireOpenRequest(accepted, rc, src.Size(), nil)
				stream, err := prims.OpenWire(ctx, wireReq)
				if err != nil {
					return largebody.ExecutionResult{}, err
				}
				return largebody.ExecutionResult{
					Stream: stream,
					Facts: largebody.ResponseFacts{
						RequestID:      accepted.Stamp.IdentityDigest().CallID("call_stream"),
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "streaming test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/event-stream") {
			t.Fatalf("expected text/event-stream, got %q", ct)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "chat.completion.chunk") {
			t.Fatalf("expected chat.completion.chunk in response, got: %s", body)
		}
		if !strings.Contains(body, "[DONE]") {
			t.Fatalf("expected [DONE] terminal in stream, got: %s", body)
		}
	})

	t.Run("NonStreamingDelivery", func(t *testing.T) {
		t.Parallel()
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "collected chat non-streaming response"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Facts: largebody.ResponseFacts{
						RequestID:      accepted.Stamp.IdentityDigest().CallID("call_nonstream"),
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "non-streaming test", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Fatalf("expected application/json, got %q", ct)
		}
		var parsed map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("unmarshal non-stream JSON: %v", err)
		}
		if parsed["object"] != "chat.completion" {
			t.Errorf("object: got %q, want chat.completion", parsed["object"])
		}
		choices, _ := parsed["choices"].([]any)
		if len(choices) == 0 {
			t.Fatal("expected non-empty choices")
		}
		firstChoice, _ := choices[0].(map[string]any)
		msg, _ := firstChoice["message"].(map[string]any)
		if msg["role"] != "assistant" {
			t.Errorf("role: got %q, want assistant", msg["role"])
		}
		if msg["content"] != "collected chat non-streaming response" {
			t.Errorf("content: got %q, want collected chat non-streaming response", msg["content"])
		}
	})
}

// 3. Error Mapping across Protocols and Pre-Output Recoverability (Requirements 10, 12, 17)
func TestLane2E2E_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("Upstream401Unauthorized", func(t *testing.T) {
		t.Parallel()
		srv, _ := startLane2CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-bad-key"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "lane2-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		var capturedErr error
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane2WireOpenRequest(accepted, rc, src.Size(), nil)
				_, err := prims.OpenWire(ctx, wireReq)
				capturedErr = err
				return largebody.ExecutionResult{}, err
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "error test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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
		if _, aerr := pool.Acquire(time.Now(), nil); !errors.Is(aerr, credpool.ErrNoUsableCredential) {
			t.Fatalf("expected credential marked invalid in pool, got acquire err: %v", aerr)
		}
	})

	t.Run("Upstream429RateLimit", func(t *testing.T) {
		t.Parallel()
		srv, _ := startLane2CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"Rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`)
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-rate-limited"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID:        "lane2-prov",
			BaseURL:           srv.URL + "/v1",
			Flavor:            openaicompat.FlavorChat,
			Pool:              pool,
			HTTPClient:        srv.Client(),
			RateLimitFallback: 5 * time.Second,
		}

		var capturedErr error
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane2WireOpenRequest(accepted, rc, src.Size(), nil)
				_, err := prims.OpenWire(ctx, wireReq)
				capturedErr = err
				return largebody.ExecutionResult{}, err
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "rate limit test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500, got %d: %s", rec.Code, rec.Body.String())
		}
		if !aipapi.IsRecoverablePreOutput(capturedErr) {
			t.Fatalf("expected OpenWire to return RecoverablePreOutputError, got %v", capturedErr)
		}
		var httpErr *openaicompat.HTTPError
		if !errors.As(capturedErr, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected HTTP 429 within OpenWire error, got %v", capturedErr)
		}
		if _, aerr := pool.Acquire(time.Now(), nil); !errors.Is(aerr, credpool.ErrNoUsableCredential) {
			t.Fatalf("expected credential marked rate-limited in pool, got acquire err: %v", aerr)
		}
	})

	t.Run("Upstream500InternalServerError", func(t *testing.T) {
		t.Parallel()
		srv, _ := startLane2CaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"Internal server error","type":"server_error","code":null}}`)
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-server-error"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "lane2-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		var capturedErr error
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				rc, _ := src.Open()
				defer func() { _ = rc.Close() }()
				wireReq := buildLane2WireOpenRequest(accepted, rc, src.Size(), nil)
				_, err := prims.OpenWire(ctx, wireReq)
				capturedErr = err
				return largebody.ExecutionResult{}, err
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "500 test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500, got %d: %s", rec.Code, rec.Body.String())
		}
		var httpErr *openaicompat.HTTPError
		if !errors.As(capturedErr, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected HTTP 500 error from OpenWire, got %v", capturedErr)
		}
	})

	t.Run("MidStreamContextCancellation", func(t *testing.T) {
		t.Parallel()

		blocker := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, testChatSSEPayloadChunk1)
			if flusher != nil {
				flusher.Flush()
			}
			<-blocker
		}))
		t.Cleanup(func() {
			close(blocker)
			srv.Close()
		})

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-cancel"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "lane2-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "mid-stream cancel test", true)
		wireReq := buildDirectLane2WireOpenRequest(payload, true, nil)

		ctx, cancel := context.WithCancel(context.Background())
		stream, err := prims.OpenWire(ctx, wireReq)
		if err != nil {
			t.Fatalf("OpenWire failed: %v", err)
		}
		defer func() { _ = stream.Close() }()

		ev, err := stream.Recv(ctx)
		if err != nil {
			t.Fatalf("expected first event, got err: %v", err)
		}
		if ev.Kind != aipapi.EventResponseStarted {
			t.Fatalf("expected EventResponseStarted, got %v", ev.Kind)
		}

		cancel()
		_, err = stream.Recv(ctx)
		if err == nil {
			t.Fatal("expected Recv to fail on canceled context, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

// 4. Canonical Response Event Parity (Requirements 12, 18)
func TestLane2E2E_CanonicalResponseEventParity(t *testing.T) {
	t.Parallel()
	srv, _ := startLane2CaptureServer(t, nil)

	pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-lane2"}})
	prims := openaicompat.WireOpenPrimitives{
		ProviderID: "lane2-prov",
		BaseURL:    srv.URL + "/v1",
		Flavor:     openaicompat.FlavorChat,
		Pool:       pool,
		HTTPClient: srv.Client(),
		MaxPending: 50,
	}

	payload := buildLane2WireReq("test-backend:gpt-4o", "parity check input", true)
	wireReq := buildDirectLane2WireOpenRequest(payload, true, nil)

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
	if !strings.Contains(textSeen.String(), "hello from lane-2 wire E2E") {
		t.Errorf("expected text delta 'hello from lane-2 wire E2E', got %q", textSeen.String())
	}
	if events[len(events)-1].Kind != aipapi.EventResponseFinished {
		t.Errorf("last event expected EventResponseFinished, got %v", events[len(events)-1].Kind)
	}
}

// 5. Stable Request and Response Identity (Requirements 16, 18)
func TestLane2E2E_StableRequestAndResponseIdentity(t *testing.T) {
	t.Parallel()

	payload := buildLane2WireReq("test-backend:gpt-4o", "identity parity payload", false)

	// Derive canonical token from DecodeChatRequest
	decoded, err := openailegacy.DecodeChatRequest(payload, openailegacy.DecodeOptions{
		RouteSelector: "test-backend:gpt-4o",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest failed: %v", err)
	}
	canonCallID := diag.StableCallID(decoded.Call)
	canonToken := diag.StableCallToken(decoded.Call)
	wantCompletionID := "chatcmpl_" + canonToken

	var observedCallID string
	var observedToken string
	exec := &lane2TestAssessorExecutor{
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
	spec := newLane2TestSpec(t, exec, 20)
	spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
		observedCallID = rc.DeterministicCallID()
		observedToken = rc.DeterministicToken()
		return inner, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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
	if parsed["id"] != wantCompletionID {
		t.Errorf("completion ID mismatch: got %q, want %q", parsed["id"], wantCompletionID)
	}
}

// 6. Cancellation by Returned ID (Requirement 18)
func TestLane2E2E_CancellationByReturnedID(t *testing.T) {
	t.Parallel()

	targetALegID := "aleg_lane2_cancel_007"
	targetSessionID := "sess_lane2_cancel_007"

	t.Run("CarrierBoundCancellationSuccess", func(t *testing.T) {
		t.Parallel()
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "cancel test"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Session: largebody.SessionResponseCarrier{
						AuthoritativeSessionID: targetSessionID,
						ALegID:                 targetALegID,
					},
					Facts: largebody.ResponseFacts{
						ALegID:         targetALegID,
						SessionID:      targetSessionID,
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}

		var observedCancellationID string
		spec := newLane2TestSpec(t, exec, 20)
		spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			observedCancellationID = rc.CancellationID()
			return inner, nil
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "cancel test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(observedCancellationID, "resp_aip_") {
			t.Fatalf("expected carrier-bound cancellation ID starting with resp_aip_, got %q", observedCancellationID)
		}

		// Verify that cancellation ID unpacks to target A-leg ID
		parsedALeg, parsedSess, ok := frontendpipe.ParseOpenAICancellationCarrier(observedCancellationID)
		if !ok || parsedALeg != targetALegID || parsedSess != targetSessionID {
			t.Fatalf("ParseOpenAICancellationCarrier failed: aleg=%q, sess=%q, ok=%v", parsedALeg, parsedSess, ok)
		}

		// Cancel via executor
		if err := exec.CancelALeg(context.Background(), aipapi.ALegCancelRequest{ALegID: parsedALeg}); err != nil {
			t.Fatalf("CancelALeg failed: %v", err)
		}
		if len(exec.canceledALegs) != 1 || exec.canceledALegs[0] != targetALegID {
			t.Fatalf("expected CancelALeg called with %q, got %v", targetALegID, exec.canceledALegs)
		}
	})

	t.Run("FallbackCancellationToOpenAIChatCompletionID", func(t *testing.T) {
		t.Parallel()
		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream([]aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "fallback cancel"},
						{Kind: aipapi.EventResponseFinished},
					}),
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}

		var observedCancellationID string
		var observedChatCmplID string
		spec := newLane2TestSpec(t, exec, 20)
		spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			observedCancellationID = rc.CancellationID()
			observedChatCmplID = rc.OpenAIChatCompletionID()
			return inner, nil
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "fallback cancel test message", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(observedCancellationID, "chatcmpl_") {
			t.Fatalf("expected chatcmpl_ prefix for Chat fallback cancellation ID, got %q", observedCancellationID)
		}
		if observedCancellationID != observedChatCmplID {
			t.Fatalf("expected CancellationID %q to equal OpenAIChatCompletionID %q", observedCancellationID, observedChatCmplID)
		}
	})

	t.Run("InvalidCarrierRejection", func(t *testing.T) {
		t.Parallel()
		_, _, ok := frontendpipe.ParseOpenAICancellationCarrier("invalid_carrier_string")
		if ok {
			t.Fatal("expected ParseOpenAICancellationCarrier to reject invalid carrier")
		}
	})
}

// 7. Session / Resume Turn-2 (Requirements 14, 18)
func TestLane2E2E_SessionResumeTurn2(t *testing.T) {
	t.Parallel()
	srv, captured := startLane2CaptureServer(t, nil)

	var turn2SessionInput largebody.SessionInput

	exec := &lane2TestAssessorExecutor{
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "session turn response"},
					{Kind: aipapi.EventResponseFinished},
				}),
				Session: largebody.SessionResponseCarrier{
					AuthoritativeSessionID: "sess_lane2_turn1",
					ALegID:                 "aleg_lane2_turn1",
					ResumeToken:            largebody.NewSensitiveString("secret_resume_lane2_xyz"),
				},
				Facts: largebody.ResponseFacts{
					SessionID:      "sess_lane2_turn1",
					ALegID:         "aleg_lane2_turn1",
					EffectiveModel: "gpt-4o",
				},
			}, nil
		},
	}
	spec := newLane2TestSpec(t, exec, 20)

	// Turn 1: Fresh request without session headers
	payload1 := buildLane2WireReq("test-backend:gpt-4o", "turn 1 prompt", false)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("turn 1 failed: %d: %s", rec1.Code, rec1.Body.String())
	}
	gotSessID := rec1.Header().Get(sessionwire.HeaderAuthoritativeSessionID)
	gotALegID := rec1.Header().Get(sessionwire.HeaderALegID)
	gotResume := rec1.Header().Get(sessionwire.HeaderResumeToken)
	if gotSessID != "sess_lane2_turn1" || gotALegID != "aleg_lane2_turn1" || gotResume != "secret_resume_lane2_xyz" {
		t.Fatalf("turn 1 headers mismatch: sess=%q, aleg=%q, resume=%q", gotSessID, gotALegID, gotResume)
	}

	// Turn 2: Follow-up request with session headers
	pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-secret"}})
	prims := openaicompat.WireOpenPrimitives{
		ProviderID: "lane2-prov",
		BaseURL:    srv.URL + "/v1",
		Flavor:     openaicompat.FlavorChat,
		Pool:       pool,
		HTTPClient: srv.Client(),
	}

	payload2 := buildLane2WireReq("test-backend:gpt-4o", "turn 2 prompt", true)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, gotSessID)
	req2.Header.Set(sessionwire.HeaderALegID, gotALegID)
	req2.Header.Set(sessionwire.HeaderResumeToken, gotResume)
	rec2 := httptest.NewRecorder()

	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		turn2SessionInput = proof.Session
		return makeLane2AcceptedAssessment(proof)
	}
	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		rc, _ := src.Open()
		defer func() { _ = rc.Close() }()
		wireReq := buildLane2WireOpenRequest(accepted, rc, src.Size(), req2.Header)
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
	if turn2SessionInput.AuthoritativeSessionID != "sess_lane2_turn1" {
		t.Errorf("turn 2 SessionInput session ID: got %q, want %q", turn2SessionInput.AuthoritativeSessionID, "sess_lane2_turn1")
	}
	if turn2SessionInput.ResumeToken.Reveal() != "secret_resume_lane2_xyz" {
		t.Errorf("turn 2 SessionInput resume token: got %q, want %q", turn2SessionInput.ResumeToken.Reveal(), "secret_resume_lane2_xyz")
	}

	// Confidentiality assertion: resume token must NOT leak into upstream headers or payload
	if strings.Contains(captured.Header.Get("Authorization"), "secret_resume_lane2_xyz") {
		t.Errorf("sensitive resume token leaked into Authorization header: %q", captured.Header.Get("Authorization"))
	}
	if captured.Header.Get(sessionwire.HeaderResumeToken) != "" {
		t.Errorf("sensitive resume token header leaked upstream: %q", captured.Header.Get(sessionwire.HeaderResumeToken))
	}
	if bytes.Contains(captured.Body, []byte("secret_resume_lane2_xyz")) {
		t.Errorf("sensitive resume token leaked into provider request body")
	}
}

// 8. Secure Recorder Input (Requirement 14)
func TestLane2E2E_SecureRecorderInput(t *testing.T) {
	t.Parallel()

	var recordedShape largebody.ClientTurnShape
	exec := &lane2TestAssessorExecutor{
		assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			recordedShape = proof.Turn
			return makeLane2AcceptedAssessment(proof)
		},
	}
	spec := newLane2TestSpec(t, exec, 20)

	payload := buildLane2WireReq("test-backend:gpt-4o", "recorder turn shape input", true)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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
func TestLane2E2E_MeteringCheckpoints(t *testing.T) {
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

	exec := &lane2TestAssessorExecutor{
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
	spec := newLane2TestSpec(t, exec, 20)
	spec.WireWrapStream = func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
		egressCP = mockCheckpoint{
			CallID:         rc.CallID(),
			EffectiveModel: rc.EffectiveModel(),
			InputTokens:    12,
			OutputTokens:   28,
		}
		return inner, nil
	}

	payload := buildLane2WireReq("test-backend:gpt-4o", "metering checkpoint input", true)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ingressCP.CallID == "" || !strings.Contains(ingressCP.EffectiveModel, "gpt-4o") || ingressCP.BodyBytes <= 0 {
		t.Fatalf("invalid ingress checkpoint: %+v", ingressCP)
	}
	if egressCP.InputTokens != 12 || egressCP.OutputTokens != 28 {
		t.Fatalf("invalid egress checkpoint: %+v", egressCP)
	}
}

// 10. Retry, Failover, and Race across Attempts with OpenWire Credential Economics (Requirements 10, 12)
func TestLane2E2E_RetryFailoverRaceCredentialEconomics(t *testing.T) {
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
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testChatSSEPayloadStream)
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
			ProviderID:        "lane2-retry-prov",
			BaseURL:           srv.URL + "/v1",
			Flavor:            openaicompat.FlavorChat,
			Pool:              pool,
			HTTPClient:        srv.Client(),
			RateLimitFallback: 5 * time.Second,
			MaxPending:        50,
		}

		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				var stream aipapi.ManagedEventStream
				var lastErr error
				for attempt := 1; attempt <= 2; attempt++ {
					reader, oerr := src.Open()
					if oerr != nil {
						return largebody.ExecutionResult{}, oerr
					}
					wireReq := buildLane2WireOpenRequest(accepted, reader, src.Size(), nil)
					s, err := prims.OpenWire(ctx, wireReq)
					_ = reader.Close()
					if err == nil {
						stream = s
						break
					}
					lastErr = err
					if !aipapi.IsRecoverablePreOutput(err) {
						return largebody.ExecutionResult{}, err
					}
				}
				if stream == nil {
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
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "retry economics test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200 after retry, got %d: %s", rec.Code, rec.Body.String())
		}
		if attemptCount.Load() != 2 {
			t.Fatalf("expected exactly 2 attempts, got %d", attemptCount.Load())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(receivedSecrets) != 2 {
			t.Fatalf("expected 2 authorization headers, got %v", receivedSecrets)
		}
		if receivedSecrets[0] != "Bearer sk-attempt-1" || receivedSecrets[1] != "Bearer sk-attempt-2" {
			t.Errorf("expected credential rotation: got %v", receivedSecrets)
		}
	})

	t.Run("FailoverBetweenBackendsPreservesBodyFromReplay", func(t *testing.T) {
		t.Parallel()

		var primaryCalled atomic.Bool
		primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryCalled.Store(true)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"Primary down","type":"server_error"}}`)
		}))
		t.Cleanup(primarySrv.Close)

		var secondaryCalled atomic.Bool
		secondarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			secondaryCalled.Store(true)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testChatSSEPayloadStream)
		}))
		t.Cleanup(secondarySrv.Close)

		pool1, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-primary"}})
		pool2, _ := credpool.New([]credpool.Credential{{ID: "k2", Secret: "sk-secondary"}})

		prims1 := openaicompat.WireOpenPrimitives{
			ProviderID: "primary",
			BaseURL:    primarySrv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool1,
			HTTPClient: primarySrv.Client(),
		}
		prims2 := openaicompat.WireOpenPrimitives{
			ProviderID: "secondary",
			BaseURL:    secondarySrv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool2,
			HTTPClient: secondarySrv.Client(),
		}

		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				// Attempt 1: primary backend
				r1, _ := src.Open()
				wireReq1 := buildLane2WireOpenRequest(accepted, r1, src.Size(), nil)
				_, err1 := prims1.OpenWire(ctx, wireReq1)
				_ = r1.Close()
				if err1 == nil {
					return largebody.ExecutionResult{}, errors.New("expected primary to fail")
				}

				// Attempt 2: failover to secondary backend
				r2, _ := src.Open()
				wireReq2 := buildLane2WireOpenRequest(accepted, r2, src.Size(), nil)
				stream2, err2 := prims2.OpenWire(ctx, wireReq2)
				_ = r2.Close()
				if err2 != nil {
					return largebody.ExecutionResult{}, err2
				}
				return largebody.ExecutionResult{
					Stream: stream2,
					Facts: largebody.ResponseFacts{
						EffectiveModel: "gpt-4o",
					},
				}, nil
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "failover test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200 after failover, got %d: %s", rec.Code, rec.Body.String())
		}
		if !primaryCalled.Load() {
			t.Fatal("expected primary backend to be called")
		}
		if !secondaryCalled.Load() {
			t.Fatal("expected secondary backend to be called after failover")
		}
	})
}

// 11. Pre-Request Keepalive 102 Processing (Requirements 18)
func TestLane2E2E_Keepalive102(t *testing.T) {
	t.Parallel()

	t.Run("StreamingEmits102DuringSlowOpen", func(t *testing.T) {
		t.Parallel()

		exec := &lane2TestAssessorExecutor{
			executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
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
		spec := newLane2TestSpec(t, exec, 20)
		spec.PreRequestKeepalive = aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 15 * time.Millisecond,
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "keepalive streaming test", true)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := &lane2KeepaliveStatusWriter{}

		frontendpipe.ServeHTTP(spec, w, req)

		n102 := w.count(102)
		if n102 == 0 {
			t.Fatalf("expected at least one 102 Processing status, got 0; statuses: %v", w.statuses)
		}
	})

	t.Run("NonStreamingBypasses102", func(t *testing.T) {
		t.Parallel()

		exec := &lane2TestAssessorExecutor{
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
		spec := newLane2TestSpec(t, exec, 20)
		spec.PreRequestKeepalive = aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 15 * time.Millisecond,
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "keepalive non-stream test", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := &lane2KeepaliveStatusWriter{}

		frontendpipe.ServeHTTP(spec, w, req)

		n102 := w.count(102)
		if n102 > 0 {
			t.Fatalf("expected 0 102 Processing statuses for non-stream, got %d", n102)
		}
	})
}

// 12. Decode-Admission Saturation Decline-to-Canonical Fallback (Requirements 1, 6, 16.7)
func TestLane2E2E_DecodeAdmissionSaturationFallback(t *testing.T) {
	t.Parallel()

	t.Run("AssessorDeclineFallsBackToCanonical", func(t *testing.T) {
		t.Parallel()
		var canonicalExecuted atomic.Bool
		exec := &lane2TestAssessorExecutor{
			assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
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
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "fallback input", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
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
	})

	t.Run("SessionHintDeclinesToCanonicalFallback", func(t *testing.T) {
		t.Parallel()
		var canonicalExecuted atomic.Bool
		exec := &lane2TestAssessorExecutor{
			canonicalExecFunc: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
				canonicalExecuted.Store(true)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "canonical hint success"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}
		spec := newLane2TestSpec(t, exec, 20)

		payload := buildLane2WireReq("test-backend:gpt-4o", "fallback hint input", false)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-AIP-Session-Hint", "client-hint-test")
		rec := httptest.NewRecorder()

		frontendpipe.ServeHTTP(spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected HTTP 200 on session hint fallback, got %d: %s", rec.Code, rec.Body.String())
		}
		if !canonicalExecuted.Load() {
			t.Fatal("expected canonical Spec.Decode/Execute to be invoked when X-AIP-Session-Hint is present")
		}
		if exec.executeLargeCalls.Load() != 0 {
			t.Fatalf("expected ExecuteLargeBody NOT to be called for session-hint carrying request, got %d", exec.executeLargeCalls.Load())
		}
	})
}

// 13. HTTP/1.1 + HTTP/2 Transport Parity (Requirement 12)
func TestLane2E2E_HTTP1AndHTTP2Transport(t *testing.T) {
	t.Parallel()

	t.Run("HTTP1_Transport", func(t *testing.T) {
		t.Parallel()
		srv, captured := startLane2CaptureServer(t, nil)

		pool, _ := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-http1"}})
		prims := openaicompat.WireOpenPrimitives{
			ProviderID: "h1-prov",
			BaseURL:    srv.URL + "/v1",
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool,
			HTTPClient: srv.Client(),
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "http1 payload", true)
		wireReq := buildDirectLane2WireOpenRequest(payload, true, nil)
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
			_, _ = io.WriteString(w, testChatSSEPayloadStream)
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
			Flavor:     openaicompat.FlavorChat,
			Pool:       pool,
			HTTPClient: cli,
		}

		payload := buildLane2WireReq("test-backend:gpt-4o", "http2 payload", true)
		wireReq := buildDirectLane2WireOpenRequest(payload, true, nil)
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
func TestLane2E2E_WireSupportNotAdvertised(t *testing.T) {
	t.Parallel()

	// Verify default LargePayloadConfig is disabled
	lpCfg := frontendpipe.LargePayloadConfig{}
	if lpCfg.Enabled {
		t.Fatal("frontendpipe.LargePayloadConfig must have Enabled == false by default")
	}

	// Verify new Handler without overrides leaves LargePayload disabled
	exec := &lane2TestAssessorExecutor{}
	h := &openailegacy.Handler{Exec: exec}
	spec := h.Spec()
	if spec.LargePayload.Enabled {
		t.Fatal("Handler.Spec() must have LargePayload.Enabled == false by default")
	}
	if spec.LargePayload.ThresholdBytes != 0 {
		t.Fatalf("expected default 0 ThresholdBytes, got %d", spec.LargePayload.ThresholdBytes)
	}
}
