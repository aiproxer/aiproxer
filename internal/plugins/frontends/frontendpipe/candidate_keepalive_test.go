package frontendpipe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Task 14.4: Preserve PreRequestKeepalive and StreamKeepaliveInterval (Requirements 1, 18):
// - Streaming ExecuteLargeBody is invoked through same holdalive semantics as current streaming Execute.
// - Stream keepalive context remains effective downstream.
// - No holdalive/provider bytes before validation + assessment + one-way commit.
// - Differential tests: enabled/disabled, long assessment, long provider-open, cancellation.

type keepaliveStatusWriter struct {
	mu       sync.Mutex
	header   http.Header
	statuses []int
	flushes  int
	body     bytes.Buffer
}

func (w *keepaliveStatusWriter) Header() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *keepaliveStatusWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}

func (w *keepaliveStatusWriter) WriteHeader(s int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statuses = append(w.statuses, s)
}

func (w *keepaliveStatusWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
}

func (w *keepaliveStatusWriter) count(status int) int {
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

func (w *keepaliveStatusWriter) Statuses() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]int, len(w.statuses))
	copy(cp, w.statuses)
	return cp
}

func (w *keepaliveStatusWriter) FlushCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushes
}

func (w *keepaliveStatusWriter) BodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func makeStreamingOrNonStreamingProofProfile(streamMode bool) *certifiedTestProfile {
	delivery := aipapi.DeliveryModeNonStreaming
	if streamMode {
		delivery = aipapi.DeliveryModeStreaming
	}
	return &certifiedTestProfile{
		profileID: "test_keepalive_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			modelSpan := largebody.Span{Offset: 10, Length: 8}
			rewrite, err := largebody.NewModelTokenRewrite(modelSpan)
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			sess := largebody.SessionInput{
				AuthoritativeSessionID: "sess_keepalive_1",
				ALegID:                 "aleg_keepalive_1",
			}
			idWriter, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				SessionInput:  sess,
				RouteSelector: "gpt-4o",
				Extensions: map[string]json.RawMessage{
					"openai.model": json.RawMessage(`"gpt-4o"`),
				},
			})
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			_ = idWriter.StartMessages()
			_ = idWriter.AddMessage(aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hello")},
			})
			digest, err := idWriter.Digest()
			if err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			sourceDigest := largebody.NewSourceDigest([32]byte{1, 2, 3})
			var reqCaps []aipapi.Capability
			if streamMode {
				reqCaps = []aipapi.Capability{aipapi.CapabilityStreaming}
			}
			proof := largebody.Proof{
				ProfileID:            "test_keepalive_profile_v1",
				Operation:            aipapi.OperationOpenAIChatCompletions,
				Delivery:             delivery,
				RouteSelector:        "gpt-4o",
				ClientModel:          "gpt-4o",
				MaxOutputTokens:      0,
				RequiredCapabilities: reqCaps,
				Facts: largebody.ProtocolFacts{
					RequirementsID: "openai_chat_v1",
				},
				Mode:      largebody.BodyModeIdentityJSON,
				Rewrite:   rewrite,
				ModelSpan: modelSpan,
				Identity:  digest,
				Turn: largebody.ClientTurnShape{
					Items: []largebody.ClientTurnItemShape{{
						Kind:    aipapi.ItemKindMessage,
						Role:    aipapi.RoleUser,
						Ordinal: 0,
						Parts: []largebody.ClientTurnPartShape{{
							Kind:         aipapi.ContentPartText,
							ContentBytes: 5,
						}},
					}},
					TotalContentBytes: 5,
				},
				Session:   sess,
				Source:    sourceDigest,
				BodyBytes: in.BodyBytes,
			}
			seeds := frontendpipe.NewResponseStateSeeds(
				digest,
				"",
				"gpt-4o",
				"gpt-4o",
				streamMode,
				sess,
				"",
			)
			return frontendpipe.ProofOutput{
				State: frontendpipe.FrontendWireState{
					ProfileID: "test_keepalive_profile_v1",
					Proof:     proof,
					Seeds:     seeds,
				},
			}, nil
		},
	}
}

type keepaliveSpecOptions struct {
	streamMode            bool
	preRequestKeepalive   aipsdk.FrontendKeepaliveConfig
	streamInterval        time.Duration
	exec                  aipsdk.ExecutorView
	onExecuteLargeCtx     func(ctx context.Context)
	onWireWrapCtx         func(ctx context.Context)
	onWireWriteCtx        func(ctx context.Context)
	onDecodeCtx           func(ctx context.Context)
	assessFunc            func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error)
	executeLargeFunc      func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error)
	writeStreamFunc       func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error
	wireCommitRecord      *[]frontendpipe.WireCommitResult
	onCandidateAssessment func(r *http.Request, res frontendpipe.CandidateAssessmentResult)
}

func newKeepaliveTestSpec(opts keepaliveSpecOptions) frontendpipe.Spec[struct{}] {
	prof := makeStreamingOrNonStreamingProofProfile(opts.streamMode)
	limiter := &trackingAdmissionLimiter{}

	var exec aipsdk.ExecutorView
	if opts.exec != nil {
		exec = opts.exec
	} else {
		testExec := &testAssessorExecutor{}
		if opts.assessFunc != nil {
			testExec.assessFunc = opts.assessFunc
		} else {
			testExec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
				return makeAcceptedAssessment(proof)
			}
		}
		if opts.executeLargeFunc != nil {
			testExec.executeLargeFunc = opts.executeLargeFunc
		} else {
			testExec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
				if opts.onExecuteLargeCtx != nil {
					opts.onExecuteLargeCtx(ctx)
				}
				return largebody.ExecutionResult{
					Stream: aipapi.NewFixedEventStream(nil),
				}, nil
			}
		}
		exec = testExec
	}

	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                    exec,
			FrontendID:              "keepalive_test",
			LargePayload:            frontendpipe.LargePayloadConfig{Enabled: true, ThresholdBytes: 1 << 20},
			DecodeAdmission:         limiter,
			PreRequestKeepalive:     opts.preRequestKeepalive,
			StreamKeepaliveInterval: opts.streamInterval,
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			if opts.onDecodeCtx != nil {
				opts.onDecodeCtx(dctx.Ctx)
			}
			return &frontendpipe.Decoded{
				RouteSelector: dctx.RouteSelector,
				Stream:        opts.streamMode,
				Call: &aipapi.Call{
					ID: "call_keepalive_test",
					Route: aipapi.RouteIntent{
						Selector: dctx.RouteSelector,
					},
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart(string(dctx.Body))},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WireWrapStream: func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			if opts.onWireWrapCtx != nil {
				opts.onWireWrapCtx(ctx)
			}
			return inner, nil
		},
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			if opts.onWireWriteCtx != nil {
				opts.onWireWriteCtx(ctx)
			}
			if opts.writeStreamFunc != nil {
				return opts.writeStreamFunc(ctx, w, rc, es)
			}
			if es != nil {
				_ = es.Close()
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "stream-wire-ok")
			return nil
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			if es != nil {
				_ = es.Close()
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "nonstream-wire-ok")
			return nil
		},
		WriteStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "stream-canonical-ok")
			return nil
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "nonstream-canonical-ok")
			return nil
		},
		OnWireCommit: func(r *http.Request, res frontendpipe.WireCommitResult) {
			if opts.wireCommitRecord != nil {
				*opts.wireCommitRecord = append(*opts.wireCommitRecord, res)
			}
		},
		OnCandidateAssessment: opts.onCandidateAssessment,
	}

	return spec
}

// TestKeepalive_StreamingExecuteLargeBody_HoldaliveEmits102 verifies Requirement 18.5:
// Streaming ExecuteLargeBody preserves pre-request holdalive wrapping when enabled.
// On slow provider-open, holdalive.Wait emits >= 1 HTTP 102 Processing status before final 200 OK.
func TestKeepalive_StreamingExecuteLargeBody_HoldaliveEmits102(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var executeCalled bool
	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: true,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 5 * time.Millisecond,
		},
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			executeCalled = true
			select {
			case <-time.After(25 * time.Millisecond):
			case <-ctx.Done():
				return largebody.ExecutionResult{}, ctx.Err()
			}
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream(nil),
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if !executeCalled {
		t.Fatal("ExecuteLargeBody was not called")
	}

	n102 := w.count(http.StatusProcessing)
	if n102 < 1 {
		t.Fatalf("expected >= 1 HTTP 102 Processing status, got %d; statuses: %v", n102, w.Statuses())
	}
	if w.FlushCount() != 0 {
		t.Fatalf("keepalive must not Flush a final 200, got %d", w.FlushCount())
	}

	statuses := w.Statuses()
	if len(statuses) == 0 || statuses[len(statuses)-1] != http.StatusOK {
		t.Fatalf("expected final status 200, got statuses %v", statuses)
	}
	if w.BodyString() != "stream-wire-ok" {
		t.Fatalf("expected body %q, got %q", "stream-wire-ok", w.BodyString())
	}
}

// TestKeepalive_StreamingExecuteLargeBody_DisabledEmitsNo102 verifies Requirement 18.5:
// When PreRequestKeepalive is disabled, streaming ExecuteLargeBody emits no 102 Processing
// even on slow provider-open.
func TestKeepalive_StreamingExecuteLargeBody_DisabledEmitsNo102(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var executeCalled bool
	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: true,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  false,
			Interval: 5 * time.Millisecond,
		},
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			executeCalled = true
			select {
			case <-time.After(25 * time.Millisecond):
			case <-ctx.Done():
				return largebody.ExecutionResult{}, ctx.Err()
			}
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream(nil),
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if !executeCalled {
		t.Fatal("ExecuteLargeBody was not called")
	}

	n102 := w.count(http.StatusProcessing)
	if n102 != 0 {
		t.Fatalf("expected 0 HTTP 102 Processing status with disabled keepalive, got %d; statuses: %v", n102, w.Statuses())
	}

	statuses := w.Statuses()
	if len(statuses) != 1 || statuses[0] != http.StatusOK {
		t.Fatalf("expected exactly [200], got %v", statuses)
	}
	if w.BodyString() != "stream-wire-ok" {
		t.Fatalf("expected body %q, got %q", "stream-wire-ok", w.BodyString())
	}
}

// TestKeepalive_NonStreamingExecuteLargeBody_BypassesHoldalive verifies Requirement 18.5:
// Non-streaming ExecuteLargeBody bypasses holdalive entirely, emitting no 102 Processing.
func TestKeepalive_NonStreamingExecuteLargeBody_BypassesHoldalive(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var executeCalled bool
	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: false,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 5 * time.Millisecond,
		},
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			executeCalled = true
			select {
			case <-time.After(25 * time.Millisecond):
			case <-ctx.Done():
				return largebody.ExecutionResult{}, ctx.Err()
			}
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream(nil),
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if !executeCalled {
		t.Fatal("ExecuteLargeBody was not called")
	}

	n102 := w.count(http.StatusProcessing)
	if n102 != 0 {
		t.Fatalf("expected 0 HTTP 102 for non-streaming execution, got %d; statuses: %v", n102, w.Statuses())
	}

	statuses := w.Statuses()
	if len(statuses) != 1 || statuses[0] != http.StatusOK {
		t.Fatalf("expected exactly [200], got %v", statuses)
	}
	if w.BodyString() != "nonstream-wire-ok" {
		t.Fatalf("expected body %q, got %q", "nonstream-wire-ok", w.BodyString())
	}
}

// TestKeepalive_LongAssessment_NoHoldaliveBeforeCommit verifies Requirements 1.3, 18.5:
// No holdalive/provider bytes are emitted before validation + assessment + one-way commit.
// Even if AssessLargeBody takes longer than the keepalive interval, NO 102 Processing is emitted.
func TestKeepalive_LongAssessment_NoHoldaliveBeforeCommit(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var statusesDuringAssess []int
	var executeCalled bool

	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: true,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 500 * time.Millisecond,
		},
		assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			// Assessment takes 750ms (longer than 500ms keepalive interval)
			time.Sleep(750 * time.Millisecond)
			statusesDuringAssess = w.Statuses()
			return makeAcceptedAssessment(proof)
		},
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			executeCalled = true
			return largebody.ExecutionResult{
				Stream: aipapi.NewFixedEventStream(nil),
			}, nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if !executeCalled {
		t.Fatal("ExecuteLargeBody was not called")
	}

	// CRITICAL: During assessment, NO status was written!
	if len(statusesDuringAssess) != 0 {
		t.Fatalf("expected 0 statuses emitted during assessment, got %v", statusesDuringAssess)
	}

	// Because ExecuteLargeBody was immediate, total 102 count should be 0
	if n102 := w.count(http.StatusProcessing); n102 != 0 {
		t.Fatalf("expected 0 102 statuses emitted for immediate ExecuteLargeBody, got %d; statuses: %v", n102, w.Statuses())
	}

	statuses := w.Statuses()
	if len(statuses) != 1 || statuses[0] != http.StatusOK {
		t.Fatalf("expected final status [200], got %v", statuses)
	}
}

// TestKeepalive_LongAssessment_Declined_NoHoldaliveBeforeCommit verifies Requirements 1.3, 18.5:
// When a long assessment declines, NO holdalive bytes are emitted before canonical fallback.
func TestKeepalive_LongAssessment_Declined_NoHoldaliveBeforeCommit(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var statusesDuringAssess []int
	var decodeCalled bool

	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: true,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 500 * time.Millisecond,
		},
		assessFunc: func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			// Assessment takes 750ms (longer than 500ms keepalive interval)
			time.Sleep(750 * time.Millisecond)
			statusesDuringAssess = w.Statuses()
			return largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
		},
		onDecodeCtx: func(ctx context.Context) {
			decodeCalled = true
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if !decodeCalled {
		t.Fatal("expected fallback to Spec.Decode on declined assessment")
	}

	if len(statusesDuringAssess) != 0 {
		t.Fatalf("expected 0 statuses emitted during assessment, got %v", statusesDuringAssess)
	}

	// Canonical fallback was fast, so no 102 emitted unless race scheduling stalled > 500ms
	if n102 := w.count(http.StatusProcessing); n102 > 1 {
		t.Fatalf("expected <= 1 102 statuses on decline fallback under race, got %d; statuses: %v", n102, w.Statuses())
	}

	if w.BodyString() != "stream-canonical-ok" {
		t.Fatalf("expected canonical stream output, got %q", w.BodyString())
	}
}

// TestKeepalive_StreamingExecuteLargeBody_Cancellation verifies Requirement 18.5:
// Cancellation during streaming ExecuteLargeBody properly terminates the holdalive loop,
// propagates context cancellation, and closes resources without hanging.
func TestKeepalive_StreamingExecuteLargeBody_Cancellation(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	ctxStarted := make(chan struct{})
	var executeErr error

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode: true,
		preRequestKeepalive: aipsdk.FrontendKeepaliveConfig{
			Enabled:  true,
			Interval: 5 * time.Millisecond,
		},
		executeLargeFunc: func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			close(ctxStarted)
			<-ctx.Done()
			executeErr = ctx.Err()
			return largebody.ExecutionResult{}, ctx.Err()
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload)).WithContext(reqCtx)
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		defer close(done)
		frontendpipe.ServeHTTP(&spec, w, req)
	}()

	select {
	case <-ctxStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for ExecuteLargeBody")
	}
	// Cancel the context while ExecuteLargeBody is blocked
	reqCancel()

	select {
	case <-done:
		// Completed cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP timed out after context cancellation (possible deadlock or leak)")
	}

	if executeErr != context.Canceled {
		t.Fatalf("expected ExecuteLargeBody context.Canceled, got %v", executeErr)
	}

	// Verify error response was written via WriteExecuteError (500 Internal Server Error)
	statuses := w.Statuses()
	hasErrorStatus := false
	for _, s := range statuses {
		if s >= 400 {
			hasErrorStatus = true
			break
		}
	}
	if !hasErrorStatus {
		t.Fatalf("expected error HTTP status written after cancellation, got statuses: %v", statuses)
	}
}

// TestKeepalive_StreamKeepaliveInterval_EffectiveDownstream verifies Requirement 18.5:
// StreamKeepaliveInterval placed into request context before body processing remains
// effective in downstream ExecuteLargeBody, WireWrapStream, and WireWriteStream contexts.
func TestKeepalive_StreamKeepaliveInterval_EffectiveDownstream(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	targetInterval := 42 * time.Second
	var gotExecInterval time.Duration
	var gotWrapInterval time.Duration
	var gotWriteInterval time.Duration

	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode:     true,
		streamInterval: targetInterval,
		onExecuteLargeCtx: func(ctx context.Context) {
			gotExecInterval = stream.KeepaliveIntervalFromContext(ctx)
		},
		onWireWrapCtx: func(ctx context.Context) {
			gotWrapInterval = stream.KeepaliveIntervalFromContext(ctx)
		},
		onWireWriteCtx: func(ctx context.Context) {
			gotWriteInterval = stream.KeepaliveIntervalFromContext(ctx)
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if gotExecInterval != targetInterval {
		t.Fatalf("ExecuteLargeBody keepalive interval: got %v, want %v", gotExecInterval, targetInterval)
	}
	if gotWrapInterval != targetInterval {
		t.Fatalf("WireWrapStream keepalive interval: got %v, want %v", gotWrapInterval, targetInterval)
	}
	if gotWriteInterval != targetInterval {
		t.Fatalf("WireWriteStream keepalive interval: got %v, want %v", gotWriteInterval, targetInterval)
	}
}

// TestKeepalive_ZeroStreamKeepaliveInterval_DefaultDownstream verifies Requirement 18.5:
// When StreamKeepaliveInterval is 0, downstream contexts retain the default recovery interval (12s).
func TestKeepalive_ZeroStreamKeepaliveInterval_DefaultDownstream(t *testing.T) {
	t.Parallel()

	payload := buildJSONPayload(1200 * 1024)
	w := &keepaliveStatusWriter{}

	var gotExecInterval time.Duration

	spec := newKeepaliveTestSpec(keepaliveSpecOptions{
		streamMode:     true,
		streamInterval: 0,
		onExecuteLargeCtx: func(ctx context.Context) {
			gotExecInterval = stream.KeepaliveIntervalFromContext(ctx)
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	frontendpipe.ServeHTTP(&spec, w, req)

	if gotExecInterval != stream.DefaultRecoveryKeepaliveInterval {
		t.Fatalf("expected default recovery keepalive interval %v, got %v", stream.DefaultRecoveryKeepaliveInterval, gotExecInterval)
	}
}
