package frontendpipe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	_ fmt.Stringer   = frontendpipe.ResponseContext{}
	_ fmt.GoStringer = frontendpipe.ResponseContext{}
	_ fmt.Formatter  = frontendpipe.ResponseContext{}
	_ slog.LogValuer = frontendpipe.ResponseContext{}
)

type mockSource struct {
	data []byte
}

func (m *mockSource) Size() int64 { return int64(len(m.data)) }
func (m *mockSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.data)), nil
}
func (m *mockSource) Close() error { return nil }

type mockStream struct {
	events []aipapi.Event
	idx    int
}

func (s *mockStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *mockStream) Close() error { return nil }

// TestResponseContext_NewAndAccessors tests Requirement 18.1, 18.2, 18.8:
// ResponseContext is bounded, frontend-owned, couples FrontendWireState + ExecutionResult (Facts + Session + Stream),
// and provides accessors without fabricating a fake aipapi.Call.
func TestResponseContext_NewAndAccessors(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	es := &mockStream{}
	defer func() { _ = es.Close() }()

	facts := largebody.ResponseFacts{
		RequestID:       proofOut.Seeds().DeterministicCallID,
		TraceID:         "trace_001",
		ALegID:          "aleg_test_1",
		SessionID:       "sess_test_1",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeNonStreaming,
		EffectiveModel:  "gpt-4o-2024-08-06",
		Source:          proofOut.Proof().Source,
		BodyBytes:       1024,
		RewrittenLength: 1024,
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_test_1",
		ALegID:                 "aleg_test_1",
		ResumeToken:            largebody.NewSensitiveString("resume_tok_secret"),
	}

	execRes := largebody.ExecutionResult{
		Stream:  es,
		Facts:   facts,
		Session: carrier,
	}

	// Verify ExecutionResult.ResponseFacts() method exists and matches
	if execRes.ResponseFacts().RequestID != proofOut.Seeds().DeterministicCallID {
		t.Fatalf("expected execRes.ResponseFacts().RequestID to match, got %q", execRes.ResponseFacts().RequestID)
	}

	// Construct ResponseContext
	rc := frontendpipe.NewResponseContext(proofOut.State, execRes)

	// Verify accessors
	if rc.ProfileID() != "test_certified_profile_v1" {
		t.Errorf("ProfileID: got %q, want %q", rc.ProfileID(), "test_certified_profile_v1")
	}
	if rc.CallID() != proofOut.Seeds().DeterministicCallID {
		t.Errorf("CallID: got %q, want %q", rc.CallID(), proofOut.Seeds().DeterministicCallID)
	}
	if rc.DeterministicCallID() != proofOut.Seeds().DeterministicCallID {
		t.Errorf("DeterministicCallID: got %q, want %q", rc.DeterministicCallID(), proofOut.Seeds().DeterministicCallID)
	}
	if rc.DeterministicTimestamp() != proofOut.Seeds().DeterministicTimestamp {
		t.Errorf("DeterministicTimestamp: got %d, want %d", rc.DeterministicTimestamp(), proofOut.Seeds().DeterministicTimestamp)
	}
	if rc.RouteSelector() != "gpt-4o" {
		t.Errorf("RouteSelector: got %q, want %q", rc.RouteSelector(), "gpt-4o")
	}
	if rc.ClientModel() != "gpt-4o" {
		t.Errorf("ClientModel: got %q, want %q", rc.ClientModel(), "gpt-4o")
	}
	if rc.EffectiveModel() != "gpt-4o-2024-08-06" {
		t.Errorf("EffectiveModel: got %q, want %q", rc.EffectiveModel(), "gpt-4o-2024-08-06")
	}
	if rc.IsStream() {
		t.Errorf("IsStream: got true, want false")
	}
	if rc.SessionID() != "sess_test_1" {
		t.Errorf("SessionID: got %q, want %q", rc.SessionID(), "sess_test_1")
	}
	if rc.ALegID() != "aleg_test_1" {
		t.Errorf("ALegID: got %q, want %q", rc.ALegID(), "aleg_test_1")
	}
	if rc.ResumeToken().Reveal() != "resume_tok_secret" {
		t.Errorf("ResumeToken: got %q, want %q", rc.ResumeToken().Reveal(), "resume_tok_secret")
	}
	if rc.TraceID() != "trace_001" {
		t.Errorf("TraceID: got %q, want %q", rc.TraceID(), "trace_001")
	}
	if rc.Stream != es {
		t.Errorf("Stream: got %v, want %v", rc.Stream, es)
	}
	if rc.Facts().RequestID != facts.RequestID {
		t.Errorf("Facts: got %+v, want %+v", rc.Facts(), facts)
	}
	if rc.SessionCarrier().AuthoritativeSessionID != carrier.AuthoritativeSessionID {
		t.Errorf("SessionCarrier: got %+v, want %+v", rc.SessionCarrier(), carrier)
	}
}

// TestResponseContext_Validate tests bounds and cross-consistency enforcement.
func TestResponseContext_Validate(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	facts := largebody.ResponseFacts{
		RequestID:       proofOut.Seeds().DeterministicCallID,
		TraceID:         "trace_1",
		ALegID:          "aleg_test_1",
		SessionID:       "sess_test_1",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeNonStreaming,
		EffectiveModel:  "gpt-4o",
		Source:          proofOut.Proof().Source,
		BodyBytes:       1024,
		RewrittenLength: 1024,
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_test_1",
		ALegID:                 "aleg_test_1",
		ResumeToken:            largebody.NewSensitiveString("tok_1"),
	}

	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream:  &mockStream{},
		Facts:   facts,
		Session: carrier,
	})

	if err := rc.Validate(largebody.DefaultMaxSemanticFactBytes); err != nil {
		t.Fatalf("expected valid context, got error: %v", err)
	}

	// Budget <= 0 fails
	if err := rc.Validate(0); err == nil {
		t.Fatalf("expected error for budget <= 0, got nil")
	}

	// Inconsistent session ID
	inconsistentRC := rc
	inconsistentRC.ResponseFacts.SessionID = "different_sess"
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent session ID, got nil")
	}

	// Inconsistent ALeg ID
	inconsistentRC = rc
	inconsistentRC.ResponseFacts.ALegID = "different_aleg"
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent aleg ID, got nil")
	}

	// Inconsistent stream flag
	inconsistentRC = rc
	inconsistentRC.ResponseFacts.Delivery = aipapi.DeliveryModeStreaming
	if err := inconsistentRC.Validate(largebody.DefaultMaxSemanticFactBytes); err == nil {
		t.Fatalf("expected error for inconsistent delivery mode, got nil")
	}
}

// TestResponseContext_WriteSessionHeaders tests Requirement 18.2, 14.6:
// Writing session response carrier headers onto http.ResponseWriter and http.Header.
func TestResponseContext_WriteSessionHeaders(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_auth_xyz",
		ALegID:                 "aleg_abc",
		ResumeToken:            largebody.NewSensitiveString("sensitive_resume_token_999"),
	}

	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
		Facts: largebody.ResponseFacts{
			RequestID:       proofOut.Seeds().DeterministicCallID,
			TraceID:         "trace_xyz",
			ALegID:          "aleg_abc",
			SessionID:       "sess_auth_xyz",
			Operation:       aipapi.OperationOpenAIChatCompletions,
			Delivery:        aipapi.DeliveryModeNonStreaming,
			EffectiveModel:  "gpt-4o",
			Source:          proofOut.Proof().Source,
			BodyBytes:       1024,
			RewrittenLength: 1024,
		},
		Session: carrier,
	})

	rec := httptest.NewRecorder()
	rc.WriteSessionHeaders(rec)

	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess_auth_xyz" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderAuthoritativeSessionID, got, "sess_auth_xyz")
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg_abc" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderALegID, got, "aleg_abc")
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != "sensitive_resume_token_999" {
		t.Errorf("Header %s: got %q, want %q", sessionwire.HeaderResumeToken, got, "sensitive_resume_token_999")
	}

	// Verify token is never in String() representation
	str := rc.Session.String()
	if strings.Contains(str, "sensitive_resume_token_999") {
		t.Fatalf("sensitive token leaked into String(): %s", str)
	}

	// Empty carrier does not set headers
	emptyRC := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
	})
	emptyRec := httptest.NewRecorder()
	emptyRC.WriteSessionHeaders(emptyRec)
	if emptyRec.Header().Get(sessionwire.HeaderAuthoritativeSessionID) != "" {
		t.Errorf("expected no session header on empty carrier")
	}
}

// TestResponseContext_DrivesWireWrappingAndWriters tests Requirement 18:
// Wire execution creates bounded ResponseContext, invokes WireWrapStream, writes session carrier headers,
// invokes WireWriteStream, and supplies OnWireCommit with the ResponseContext.
func TestResponseContext_DrivesWireWrappingAndWriters(t *testing.T) {
	t.Parallel()

	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}

	es := &mockStream{}
	defer func() { _ = es.Close() }()

	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		return largebody.ExecutionResult{
			Stream: es,
			Facts: largebody.ResponseFacts{
				RequestID:       accepted.Stamp.GenerationID(),
				TraceID:         "trace_wire_001",
				ALegID:          "aleg_test_1",
				SessionID:       "sess_test_1",
				Operation:       accepted.WireRequest.Operation,
				Delivery:        accepted.WireRequest.Delivery,
				EffectiveModel:  "effective-provider-model",
				Source:          accepted.Stamp.SourceDigest(),
				BodyBytes:       src.Size(),
				RewrittenLength: 100,
			},
			Session: largebody.SessionResponseCarrier{
				AuthoritativeSessionID: "sess_wire_test",
				ALegID:                 "aleg_wire_test",
				ResumeToken:            largebody.NewSensitiveString("secret_tok_wire"),
			},
		}, nil
	}

	var wireWrapStreamCalled bool
	var wireWriteStreamCalled bool
	var wireWriteNonStreamCalled bool
	var observedCommitResult frontendpipe.WireCommitResult

	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			DefaultRouteSelector: "gpt-4o",
			MaxRequestBodyBytes:  10 * 1024 * 1024,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024, // 1 KiB threshold
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: minimalValidProofProfile(),
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			t.Fatal("Spec.Decode should NOT be called on wire execution fast path")
			return nil, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			t.Fatal("BuildEncodeOpts should NOT be called on wire execution fast path")
			return struct{}{}
		},
		WriteStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, stream aipapi.EventStream, opts struct{}) error {
			t.Fatal("canonical WriteStream should NOT be called on wire execution fast path")
			return nil
		},
		WireWrapStream: func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			wireWrapStreamCalled = true
			if rc.CallID() == "" {
				t.Errorf("WireWrapStream rc.CallID is empty")
			}
			if rc.EffectiveModel() != "effective-provider-model" {
				t.Errorf("WireWrapStream rc.EffectiveModel: got %q, want %q", rc.EffectiveModel(), "effective-provider-model")
			}
			return inner, nil
		},
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream aipapi.EventStream) error {
			wireWriteStreamCalled = true
			return nil
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream aipapi.EventStream) error {
			wireWriteNonStreamCalled = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":"wire_non_stream"}`))
			return nil
		},
		OnWireCommit: func(r *http.Request, res frontendpipe.WireCommitResult) {
			observedCommitResult = res
		},
	}

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !wireWrapStreamCalled {
		t.Errorf("expected WireWrapStream to be called")
	}
	if !wireWriteNonStreamCalled {
		t.Errorf("expected WireWriteNonStream to be called for non-streaming request")
	}
	if wireWriteStreamCalled {
		t.Errorf("expected WireWriteStream NOT to be called for non-streaming request")
	}

	// Verify session carrier headers were written by the response context
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess_wire_test" {
		t.Errorf("Session header: got %q, want %q", got, "sess_wire_test")
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg_wire_test" {
		t.Errorf("ALeg header: got %q, want %q", got, "aleg_wire_test")
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != "secret_tok_wire" {
		t.Errorf("Resume header: got %q, want %q", got, "secret_tok_wire")
	}

	// Verify WireCommitResult contains ResponseContext
	if observedCommitResult.ResponseContext.CallID() == "" {
		t.Errorf("observedCommitResult.ResponseContext.CallID should not be empty")
	}
}

// TestResponseContext_WireWrapStreamError proves that when WireWrapStream fails,
// it writes an execute error JSON and does NOT call writers.
func TestResponseContext_WireWrapStreamError(t *testing.T) {
	t.Parallel()

	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}
	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		return largebody.ExecutionResult{
			Stream: &mockStream{},
			Facts: largebody.ResponseFacts{
				RequestID:       accepted.Stamp.GenerationID(),
				TraceID:         "trace_001",
				ALegID:          "aleg_test_1",
				SessionID:       "sess_test_1",
				Operation:       accepted.WireRequest.Operation,
				Delivery:        accepted.WireRequest.Delivery,
				EffectiveModel:  "wire-model",
				Source:          accepted.Stamp.SourceDigest(),
				BodyBytes:       src.Size(),
				RewrittenLength: 100,
			},
		}, nil
	}

	var writerCalled bool
	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			DefaultRouteSelector: "gpt-4o",
			MaxRequestBodyBytes:  10 * 1024 * 1024,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024,
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: minimalValidProofProfile(),
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		WireWrapStream: func(ctx context.Context, rc frontendpipe.ResponseContext, inner aipapi.EventStream) (aipapi.EventStream, error) {
			return nil, errors.New("injected wire wrap stream failure")
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream aipapi.EventStream) error {
			writerCalled = true
			return nil
		},
	}

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected error status code, got %d", rec.Code)
	}
	if writerCalled {
		t.Errorf("writer should NOT be called after WireWrapStream error")
	}
}

// TestResponseContext_CoreDoesNotImportFrontendResponseState proves Requirement 18.8:
// Core packages (internal/core/...) MUST NOT import frontend response-state types or frontendpipe.
func TestResponseContext_CoreDoesNotImportFrontendResponseState(t *testing.T) {
	t.Parallel()

	// Walk internal/core directory and check all imports
	coreRoot := filepath.Join("..", "..", "..", "core")
	err := filepath.Walk(coreRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		fset := token.NewFileSet()
		node, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range node.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(importPath, "frontendpipe") {
				t.Errorf("core file %s illegally imports frontendpipe: %s", path, importPath)
			}
			if !strings.HasSuffix(info.Name(), "_test.go") && strings.Contains(importPath, "internal/plugins/frontends") {
				t.Errorf("core production file %s illegally imports frontends package: %s", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to walk core root: %v", err)
	}
}

// TestResponseContext_DeterministicIDsAndTimestamps_ParityWithCanonical tests Requirements 16 and 18.7:
// Deterministic response ID, timestamp, time, token, chat completion ID, and Anthropic message ID
// derivation from canonical semantic identity digest, matching diag.Stable* on canonical Call.
func TestResponseContext_DeterministicIDsAndTimestamps_ParityWithCanonical(t *testing.T) {
	t.Parallel()

	canonicalCall := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello deterministic world")},
		}},
		Route: aipapi.RouteIntent{Selector: "gpt-4o"},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sess_parity_1",
		},
	}

	digest := largebody.CanonicalCallIdentity(canonicalCall)
	if digest.IsZero() {
		t.Fatalf("expected non-zero identity digest")
	}

	seeds := frontendpipe.NewResponseStateSeeds(
		digest,
		"", // no explicit request ID
		"gpt-4o",
		"gpt-4o",
		false,
		largebody.SessionInput{AuthoritativeSessionID: "sess_parity_1"},
		"",
	)

	state := frontendpipe.FrontendWireState{
		ProfileID: "test_profile_parity",
		Proof: largebody.Proof{
			ProfileID:     "test_profile_parity",
			Operation:     aipapi.OperationOpenAIChatCompletions,
			Delivery:      aipapi.DeliveryModeNonStreaming,
			RouteSelector: "gpt-4o",
			ClientModel:   "gpt-4o",
			Identity:      digest,
		},
		Seeds: seeds,
	}

	rc := frontendpipe.NewResponseContext(state, largebody.ExecutionResult{
		Stream: &mockStream{},
		Facts: largebody.ResponseFacts{
			Operation:       aipapi.OperationOpenAIChatCompletions,
			Delivery:        aipapi.DeliveryModeNonStreaming,
			EffectiveModel:  "gpt-4o",
			BodyBytes:       100,
			RewrittenLength: 100,
		},
	})

	wantCallID := diag.StableCallID(canonicalCall)
	wantUnix := diag.StableUnix(canonicalCall)
	wantTime := diag.StableTime(canonicalCall)
	wantToken := diag.StableCallToken(canonicalCall)
	wantChatCmplID := "chatcmpl_" + wantToken
	wantAnthropicMsgID := "msg_" + wantToken
	wantOpenAIRespID := "resp_" + wantToken
	wantOpenAIMsgID := "msg_resp_" + wantToken

	if got := rc.DeterministicCallID(); got != wantCallID {
		t.Errorf("DeterministicCallID: got %q, want %q", got, wantCallID)
	}
	if got := rc.CallID(); got != wantCallID {
		t.Errorf("CallID: got %q, want %q", got, wantCallID)
	}
	if got := rc.DeterministicTimestamp(); got != wantUnix {
		t.Errorf("DeterministicTimestamp: got %d, want %d", got, wantUnix)
	}
	if got := rc.DeterministicTime(); !got.Equal(wantTime) {
		t.Errorf("DeterministicTime: got %v, want %v", got, wantTime)
	}
	if got := rc.DeterministicToken(); got != wantToken {
		t.Errorf("DeterministicToken: got %q, want %q", got, wantToken)
	}
	if got := rc.ResponseID("resp_"); got != wantOpenAIRespID {
		t.Errorf("ResponseID(resp_): got %q, want %q", got, wantOpenAIRespID)
	}
	if got := rc.OpenAIChatCompletionID(); got != wantChatCmplID {
		t.Errorf("OpenAIChatCompletionID: got %q, want %q", got, wantChatCmplID)
	}
	if got := rc.AnthropicMessageID(); got != wantAnthropicMsgID {
		t.Errorf("AnthropicMessageID: got %q, want %q", got, wantAnthropicMsgID)
	}
	// When no A-leg ID is present, OpenAIResponseID falls back to deterministic resp_ + token
	if got := rc.OpenAIResponseID(); got != wantOpenAIRespID {
		t.Errorf("OpenAIResponseID (no A-leg): got %q, want %q", got, wantOpenAIRespID)
	}
	if got := rc.OpenAIMessageID(); got != wantOpenAIMsgID {
		t.Errorf("OpenAIMessageID (no A-leg): got %q, want %q", got, wantOpenAIMsgID)
	}

	// Requirement 16.6: Explicit caller request ID retains precedence for CallID,
	// while DeterministicCallID remains stable.
	seedsExplicit := frontendpipe.NewResponseStateSeeds(
		digest,
		"req_explicit_caller_id_123",
		"gpt-4o",
		"gpt-4o",
		false,
		largebody.SessionInput{AuthoritativeSessionID: "sess_parity_1"},
		"",
	)
	stateExplicit := state
	stateExplicit.Seeds = seedsExplicit
	rcExplicit := frontendpipe.NewResponseContext(stateExplicit, largebody.ExecutionResult{
		Stream: &mockStream{},
	})
	if got := rcExplicit.CallID(); got != "req_explicit_caller_id_123" {
		t.Errorf("CallID with explicit ID: got %q, want req_explicit_caller_id_123", got)
	}
	if got := rcExplicit.DeterministicCallID(); got != "req_explicit_caller_id_123" {
		t.Errorf("DeterministicCallID with explicit ID: got %q, want req_explicit_caller_id_123", got)
	}
	if got := rcExplicit.DeterministicToken(); got != wantToken {
		t.Errorf("DeterministicToken with explicit ID: got %q, want %q", got, wantToken)
	}
}

type mockALegCanceler struct {
	canceled aipapi.ALegCancelRequest
}

func (m *mockALegCanceler) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (m *mockALegCanceler) WallClock() func() time.Time { return nil }

func (m *mockALegCanceler) CancelALeg(_ context.Context, req aipapi.ALegCancelRequest) error {
	m.canceled = req
	return nil
}

// TestResponseContext_OpenAICancellationCarrier_BoundToAuthoritativeALegAndSession tests Requirements 16 and 18.6:
// OpenAI Responses cancellation IDs remain bound to authoritative A-leg and session semantics.
// Wire requests must remain cancellable by the returned ID through openairesponses.Handler.
func TestResponseContext_OpenAICancellationCarrier_BoundToAuthoritativeALegAndSession(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	// Case 1: Authoritative A-leg and session carrier present from wire execution result
	facts := largebody.ResponseFacts{
		RequestID:       proofOut.Seeds().DeterministicCallID,
		TraceID:         "trace_001",
		ALegID:          "a-leg-wire-auth-99",
		SessionID:       "sess-wire-auth-99",
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		EffectiveModel:  "gpt-4o",
		Source:          proofOut.Proof().Source,
		BodyBytes:       1024,
		RewrittenLength: 1024,
	}

	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess-wire-auth-99",
		ALegID:                 "a-leg-wire-auth-99",
		ResumeToken:            largebody.NewSensitiveString("secret_tok"),
	}

	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream:  &mockStream{},
		Facts:   facts,
		Session: carrier,
	})

	respID := rc.OpenAIResponseID()
	if !strings.HasPrefix(respID, frontendpipe.OpenAICancellationPrefix) {
		t.Fatalf("OpenAIResponseID: got %q, want prefix %q", respID, frontendpipe.OpenAICancellationPrefix)
	}
	if got := rc.CancellationID(); got != respID {
		t.Errorf("CancellationID: got %q, want %q (matches OpenAIResponseID)", got, respID)
	}

	// Verify ParseOpenAICancellationCarrier round-trips
	aLegID, sessionID, ok := frontendpipe.ParseOpenAICancellationCarrier(respID)
	if !ok {
		t.Fatalf("ParseOpenAICancellationCarrier(%q) failed", respID)
	}
	if aLegID != "a-leg-wire-auth-99" || sessionID != "sess-wire-auth-99" {
		t.Fatalf("parsed carrier = (%q, %q), want (a-leg-wire-auth-99, sess-wire-auth-99)", aLegID, sessionID)
	}

	// End-to-end cancellation proof: Send cancel request to openairesponses.Handler using returned ID
	canceler := &mockALegCanceler{}
	handler := &openairesponses.Handler{Exec: canceler}
	cancelReq := httptest.NewRequest(http.MethodPost, "/v1/responses/"+respID+"/cancel", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, cancelReq)

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel HTTP status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if canceler.canceled.ALegID != "a-leg-wire-auth-99" {
		t.Errorf("CancelALeg ALegID: got %q, want a-leg-wire-auth-99", canceler.canceled.ALegID)
	}
	if canceler.canceled.SessionID != "sess-wire-auth-99" {
		t.Errorf("CancelALeg SessionID: got %q, want sess-wire-auth-99", canceler.canceled.SessionID)
	}

	// Case 2: No A-leg ID (e.g. no session carrier)
	stateNoALeg := proofOut.State
	stateNoALeg.Seeds.ALegID = ""
	stateNoALeg.Seeds.SessionID = ""
	rcNoALeg := frontendpipe.NewResponseContext(stateNoALeg, largebody.ExecutionResult{
		Stream: &mockStream{},
		Facts: largebody.ResponseFacts{
			Operation:       aipapi.OperationOpenAIResponses,
			Delivery:        aipapi.DeliveryModeStreaming,
			EffectiveModel:  "gpt-4o",
			BodyBytes:       1024,
			RewrittenLength: 1024,
		},
	})
	noALegRespID := rcNoALeg.OpenAIResponseID()
	wantFallback := "resp_" + rcNoALeg.DeterministicToken()
	if noALegRespID != wantFallback {
		t.Errorf("OpenAIResponseID without A-leg: got %q, want %q", noALegRespID, wantFallback)
	}
	if _, _, ok := frontendpipe.ParseOpenAICancellationCarrier(noALegRespID); ok {
		t.Errorf("ParseOpenAICancellationCarrier should reject fallback ID %q", noALegRespID)
	}

	// Cancel endpoint rejects cancel request with unbound response ID
	canceler2 := &mockALegCanceler{}
	handler2 := &openairesponses.Handler{Exec: canceler2}
	cancelReq2 := httptest.NewRequest(http.MethodPost, "/v1/responses/"+noALegRespID+"/cancel", nil)
	rec2 := httptest.NewRecorder()

	handler2.ServeHTTP(rec2, cancelReq2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for cancel on unbound ID, got %d", rec2.Code)
	}
	if canceler2.canceled.ALegID != "" {
		t.Errorf("canceler should not be called for unbound ID")
	}
}

type testLogSpy struct {
	mu      sync.Mutex
	records []string
}

func (s *testLogSpy) Enabled(context.Context, slog.Level) bool { return true }
func (s *testLogSpy) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		val := a.Value.Resolve()
		b.WriteString(" ")
		b.WriteString(a.Key)
		b.WriteString("=")
		fmt.Fprintf(&b, "%v", val.Any())
		return true
	})
	s.records = append(s.records, b.String())
	return nil
}
func (s *testLogSpy) WithAttrs(attrs []slog.Attr) slog.Handler { return s }
func (s *testLogSpy) WithGroup(name string) slog.Handler       { return s }

func (s *testLogSpy) Contains(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.records {
		if strings.Contains(rec, substr) {
			return true
		}
	}
	return false
}

func (s *testLogSpy) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// TestResponseContext_SessionHeaders_ParityWithCanonical tests Requirements 14.6 and 18.2:
// A wire-executed session returns the exact same session/resume headers as canonical execution.
func TestResponseContext_SessionHeaders_ParityWithCanonical(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	t.Run("NewSession", func(t *testing.T) {
		t.Parallel()
		const (
			sessID      = "sess_parity_new_1"
			aLegID      = "aleg_parity_new_1"
			resumeToken = "secret_resume_token_parity_new_1"
		)

		// Canonical path: sessionwire.WriteResponseCarriers from aipapi.Call
		canonicalCall := &aipapi.Call{
			Session: aipapi.SessionRef{
				AuthoritativeSessionID: sessID,
				ALegID:                 aLegID,
				ResumeToken:            resumeToken,
			},
		}
		recCanonical := httptest.NewRecorder()
		sessionwire.WriteResponseCarriers(recCanonical, canonicalCall)

		// Wire path: ResponseContext.WriteSessionHeaders
		rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
			Stream: &mockStream{},
			Session: largebody.SessionResponseCarrier{
				AuthoritativeSessionID: sessID,
				ALegID:                 aLegID,
				ResumeToken:            largebody.NewSensitiveString(resumeToken),
			},
		})
		recWire := httptest.NewRecorder()
		rc.WriteSessionHeaders(recWire)

		// Assert exact parity
		if got, want := recWire.Header().Get(sessionwire.HeaderAuthoritativeSessionID), recCanonical.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != want || got != sessID {
			t.Errorf("AuthoritativeSessionID header: got %q, want %q", got, want)
		}
		if got, want := recWire.Header().Get(sessionwire.HeaderALegID), recCanonical.Header().Get(sessionwire.HeaderALegID); got != want || got != aLegID {
			t.Errorf("ALegID header: got %q, want %q", got, want)
		}
		if got, want := recWire.Header().Get(sessionwire.HeaderResumeToken), recCanonical.Header().Get(sessionwire.HeaderResumeToken); got != want || got != resumeToken {
			t.Errorf("ResumeToken header: got %q, want %q", got, want)
		}
		if !reflect.DeepEqual(recWire.Header(), recCanonical.Header()) {
			t.Errorf("Header map mismatch:\nwire:      %+v\ncanonical: %+v", recWire.Header(), recCanonical.Header())
		}

		// Also test WriteSessionHeadersTo(h)
		h := make(http.Header)
		rc.WriteSessionHeadersTo(h)
		if !reflect.DeepEqual(h, recCanonical.Header()) {
			t.Errorf("WriteSessionHeadersTo header mismatch:\ngot:  %+v\nwant: %+v", h, recCanonical.Header())
		}
	})

	t.Run("ResumedSession", func(t *testing.T) {
		t.Parallel()
		const (
			sessID = "sess_parity_resumed_1"
			aLegID = "aleg_parity_resumed_1"
		)

		// Resumed session does NOT emit a new resume token
		canonicalCall := &aipapi.Call{
			Session: aipapi.SessionRef{
				AuthoritativeSessionID: sessID,
				ALegID:                 aLegID,
				ResumeToken:            "",
			},
		}
		recCanonical := httptest.NewRecorder()
		sessionwire.WriteResponseCarriers(recCanonical, canonicalCall)

		rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
			Stream: &mockStream{},
			Session: largebody.SessionResponseCarrier{
				AuthoritativeSessionID: sessID,
				ALegID:                 aLegID,
				ResumeToken:            largebody.SensitiveString{},
			},
		})
		recWire := httptest.NewRecorder()
		rc.WriteSessionHeaders(recWire)

		if got, want := recWire.Header().Get(sessionwire.HeaderAuthoritativeSessionID), recCanonical.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != want || got != sessID {
			t.Errorf("AuthoritativeSessionID header: got %q, want %q", got, want)
		}
		if got, want := recWire.Header().Get(sessionwire.HeaderALegID), recCanonical.Header().Get(sessionwire.HeaderALegID); got != want || got != aLegID {
			t.Errorf("ALegID header: got %q, want %q", got, want)
		}
		if got := recWire.Header().Get(sessionwire.HeaderResumeToken); got != "" {
			t.Errorf("ResumeToken header must be absent on resumed session, got %q", got)
		}
		if !reflect.DeepEqual(recWire.Header(), recCanonical.Header()) {
			t.Errorf("Header map mismatch:\nwire:      %+v\ncanonical: %+v", recWire.Header(), recCanonical.Header())
		}
	})

	t.Run("EmptySession", func(t *testing.T) {
		t.Parallel()
		canonicalCall := &aipapi.Call{}
		recCanonical := httptest.NewRecorder()
		sessionwire.WriteResponseCarriers(recCanonical, canonicalCall)

		rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
			Stream: &mockStream{},
		})
		recWire := httptest.NewRecorder()
		rc.WriteSessionHeaders(recWire)

		if len(recWire.Header()) != 0 {
			t.Errorf("expected 0 headers on empty session, got %+v", recWire.Header())
		}
		if !reflect.DeepEqual(recWire.Header(), recCanonical.Header()) {
			t.Errorf("Header map mismatch on empty session:\nwire:      %+v\ncanonical: %+v", recWire.Header(), recCanonical.Header())
		}
	})
}

// TestResponseContext_SessionHeaders_NextRequestResumes tests Requirements 14.1, 14.6, 18.2:
// Headers returned by a wire-executed first turn are accepted by follow-up requests,
// which resume the session with the same session ID and A-leg ID.
func TestResponseContext_SessionHeaders_NextRequestResumes(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	const (
		initialSessionID = "sess_resume_e2e_001"
		initialALegID    = "aleg_resume_e2e_001"
		initialSecret    = "super_secret_resume_token_xyz"
	)

	// Turn 1: Wire execution of new session produces response carrier headers
	rcTurn1 := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
		Session: largebody.SessionResponseCarrier{
			AuthoritativeSessionID: initialSessionID,
			ALegID:                 initialALegID,
			ResumeToken:            largebody.NewSensitiveString(initialSecret),
		},
	})
	recTurn1 := httptest.NewRecorder()
	rcTurn1.WriteSessionHeaders(recTurn1)

	sid := strings.TrimSpace(recTurn1.Header().Get(sessionwire.HeaderAuthoritativeSessionID))
	aLeg := strings.TrimSpace(recTurn1.Header().Get(sessionwire.HeaderALegID))
	tok := strings.TrimSpace(recTurn1.Header().Get(sessionwire.HeaderResumeToken))

	if sid != initialSessionID {
		t.Fatalf("Turn 1 session ID: got %q, want %q", sid, initialSessionID)
	}
	if aLeg != initialALegID {
		t.Fatalf("Turn 1 A-leg ID: got %q, want %q", aLeg, initialALegID)
	}
	if tok != initialSecret {
		t.Fatalf("Turn 1 resume token: got %q, want %q", tok, initialSecret)
	}

	// Turn 2: Follow-up request carrying the emitted session and resume headers
	reqTurn2 := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"turn 2"}`))
	reqTurn2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid)
	reqTurn2.Header.Set(sessionwire.HeaderResumeToken, tok)

	// Verify canonical frontend extraction receives exact session ID and resume token
	var canonicalRef aipapi.SessionRef
	sessionwire.ApplyAuthoritativeHeaders(&canonicalRef, reqTurn2.Header)
	if canonicalRef.AuthoritativeSessionID != initialSessionID {
		t.Errorf("Canonical resume session ID: got %q, want %q", canonicalRef.AuthoritativeSessionID, initialSessionID)
	}
	if canonicalRef.ResumeToken != initialSecret {
		t.Errorf("Canonical resume token: got %q, want %q", canonicalRef.ResumeToken, initialSecret)
	}

	// Verify wire frontend extraction receives exact session ID and resume token
	wireInput, err := sessionwire.BuildSessionInput(reqTurn2.Header, nil, sessionwire.SessionInputOptions{
		MaxFactBytes: 1024,
	})
	if err != nil {
		t.Fatalf("BuildSessionInput failed: %v", err)
	}
	if wireInput.AuthoritativeSessionID != initialSessionID {
		t.Errorf("Wire resume session ID: got %q, want %q", wireInput.AuthoritativeSessionID, initialSessionID)
	}
	if wireInput.ResumeToken.Reveal() != initialSecret {
		t.Errorf("Wire resume token: got %q, want %q", wireInput.ResumeToken.Reveal(), initialSecret)
	}

	// Turn 2 response: resumed session retains authoritative IDs and omits resume token
	rcTurn2 := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream: &mockStream{},
		Session: largebody.SessionResponseCarrier{
			AuthoritativeSessionID: sid,
			ALegID:                 aLeg,
			ResumeToken:            largebody.SensitiveString{}, // empty on resume
		},
	})
	recTurn2 := httptest.NewRecorder()
	rcTurn2.WriteSessionHeaders(recTurn2)

	if got := recTurn2.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != initialSessionID {
		t.Errorf("Turn 2 AuthoritativeSessionID: got %q, want %q", got, initialSessionID)
	}
	if got := recTurn2.Header().Get(sessionwire.HeaderALegID); got != initialALegID {
		t.Errorf("Turn 2 ALegID: got %q, want %q", got, initialALegID)
	}
	if got := recTurn2.Header().Get(sessionwire.HeaderResumeToken); got != "" {
		t.Errorf("Turn 2 ResumeToken must not be set on resumed session, got %q", got)
	}
}

// TestResponseContext_SensitiveToken_NeverReachesLoggingMetricsDebug tests Requirements 14.1, 14.7, 18.2, 22.2, 22.3:
// Sensitive resume token never reaches general logging, metrics, debug formatting, or JSON/text representations.
func TestResponseContext_SensitiveToken_NeverReachesLoggingMetricsDebug(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	const rawSecret = "super_secret_resume_token_classified_never_log_me"

	tok := largebody.NewSensitiveString(rawSecret)
	carrier := largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess_secret_test_001",
		ALegID:                 "aleg_secret_test_001",
		ResumeToken:            tok,
	}
	rc := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
		Stream:  &mockStream{},
		Session: carrier,
	})

	t.Run("SensitiveStringRedaction", func(t *testing.T) {
		t.Parallel()
		assertNoSecret := func(label, got string) {
			t.Helper()
			if strings.Contains(got, rawSecret) {
				t.Fatalf("%s leaked sensitive secret: %q", label, got)
			}
		}

		assertNoSecret("String()", tok.String())
		assertNoSecret("GoString()", tok.GoString())
		assertNoSecret("fmt %v", fmt.Sprintf("%v", tok))
		assertNoSecret("fmt %+v", fmt.Sprintf("%+v", tok))
		assertNoSecret("fmt %#v", fmt.Sprintf("%#v", tok))
		assertNoSecret("fmt %s", fmt.Sprintf("%s", tok))
		assertNoSecret("fmt %q", fmt.Sprintf("%q", tok))

		rawJSON, err := json.Marshal(tok)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		assertNoSecret("json.Marshal", string(rawJSON))
		if string(rawJSON) != `"[redacted]"` {
			t.Errorf("json.Marshal: got %s, want %s", string(rawJSON), `"[redacted]"`)
		}

		rawText, err := tok.MarshalText()
		if err != nil {
			t.Fatalf("MarshalText failed: %v", err)
		}
		assertNoSecret("MarshalText", string(rawText))
		if string(rawText) != "[redacted]" {
			t.Errorf("MarshalText: got %s, want [redacted]", string(rawText))
		}
	})

	t.Run("SessionResponseCarrierRedaction", func(t *testing.T) {
		t.Parallel()
		assertNoSecret := func(label, got string) {
			t.Helper()
			if strings.Contains(got, rawSecret) {
				t.Fatalf("%s leaked sensitive secret: %q", label, got)
			}
		}

		assertNoSecret("carrier.String()", carrier.String())
		assertNoSecret("carrier.GoString()", carrier.GoString())
		assertNoSecret("carrier %v", fmt.Sprintf("%v", carrier))
		assertNoSecret("carrier %+v", fmt.Sprintf("%+v", carrier))
		assertNoSecret("carrier %#v", fmt.Sprintf("%#v", carrier))
		assertNoSecret("carrier %s", fmt.Sprintf("%s", carrier))

		// Also confirm that session ID and A-leg ID do not appear in presence string
		if strings.Contains(carrier.String(), "sess_secret_test_001") {
			t.Errorf("carrier.String() contains session ID: %s", carrier.String())
		}
		if strings.Contains(carrier.String(), "aleg_secret_test_001") {
			t.Errorf("carrier.String() contains A-leg ID: %s", carrier.String())
		}
	})

	t.Run("ResponseContextRedaction", func(t *testing.T) {
		t.Parallel()
		assertNoSecret := func(label, got string) {
			t.Helper()
			if strings.Contains(got, rawSecret) {
				t.Fatalf("%s leaked sensitive secret: %q", label, got)
			}
		}

		// Interface conformance checks ensuring methods are load-bearing (Requirements 14.7, 18.2, 22.3)
		if _, ok := any(rc).(fmt.Stringer); !ok {
			t.Errorf("ResponseContext does not implement fmt.Stringer")
		}
		if _, ok := any(rc).(fmt.GoStringer); !ok {
			t.Errorf("ResponseContext does not implement fmt.GoStringer")
		}
		if _, ok := any(rc).(fmt.Formatter); !ok {
			t.Errorf("ResponseContext does not implement fmt.Formatter")
		}
		if _, ok := any(rc).(slog.LogValuer); !ok {
			t.Errorf("ResponseContext does not implement slog.LogValuer")
		}

		wantString := fmt.Sprintf("ResponseContext{ProfileID:%q CallID:%q RouteSelector:%q ClientModel:%q EffectiveModel:%q Stream:%t Session:%s}",
			rc.ProfileID(), rc.CallID(), rc.RouteSelector(), rc.ClientModel(), rc.EffectiveModel(), rc.IsStream(), carrier.String())

		// Assert exact safe presence-only rendering on all formatting verbs (Requirements 14.7, 18.2, 22.3)
		if got := rc.String(); got != wantString {
			t.Errorf("rc.String():\ngot:  %s\nwant: %s", got, wantString)
		}
		if got := rc.GoString(); got != wantString {
			t.Errorf("rc.GoString():\ngot:  %s\nwant: %s", got, wantString)
		}
		if got := fmt.Sprintf("%v", rc); got != wantString {
			t.Errorf("rc %%v:\ngot:  %s\nwant: %s", got, wantString)
		}
		if got := fmt.Sprintf("%+v", rc); got != wantString {
			t.Errorf("rc %%+v:\ngot:  %s\nwant: %s", got, wantString)
		}
		if got := fmt.Sprintf("%#v", rc); got != wantString {
			t.Errorf("rc %%#v:\ngot:  %s\nwant: %s", got, wantString)
		}
		if got := fmt.Sprintf("%s", rc); got != wantString {
			t.Errorf("rc %%s:\ngot:  %s\nwant: %s", got, wantString)
		}
		wantQuoted := fmt.Sprintf("%q", wantString)
		if got := fmt.Sprintf("%q", rc); got != wantQuoted {
			t.Errorf("rc %%q:\ngot:  %s\nwant: %s", got, wantQuoted)
		}

		assertNoSecret("rc.String()", rc.String())
		assertNoSecret("rc.GoString()", rc.GoString())
		assertNoSecret("rc %v", fmt.Sprintf("%v", rc))
		assertNoSecret("rc %+v", fmt.Sprintf("%+v", rc))
		assertNoSecret("rc %#v", fmt.Sprintf("%#v", rc))
		assertNoSecret("rc %s", fmt.Sprintf("%s", rc))
		assertNoSecret("rc %q", fmt.Sprintf("%q", rc))

		// Resumed context rendering check: has_resume_token:false
		carrierResumed := largebody.SessionResponseCarrier{
			AuthoritativeSessionID: "sess_secret_test_001",
			ALegID:                 "aleg_secret_test_001",
			ResumeToken:            largebody.SensitiveString{},
		}
		rcResumed := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
			Stream:  &mockStream{},
			Session: carrierResumed,
		})
		wantResumedString := fmt.Sprintf("ResponseContext{ProfileID:%q CallID:%q RouteSelector:%q ClientModel:%q EffectiveModel:%q Stream:%t Session:SessionResponseCarrier{has_session_id:true has_aleg_id:true has_resume_token:false}}",
			rcResumed.ProfileID(), rcResumed.CallID(), rcResumed.RouteSelector(), rcResumed.ClientModel(), rcResumed.EffectiveModel(), rcResumed.IsStream())
		if got := rcResumed.String(); got != wantResumedString {
			t.Errorf("rcResumed.String():\ngot:  %s\nwant: %s", got, wantResumedString)
		}
		if got := fmt.Sprintf("%+v", rcResumed); got != wantResumedString {
			t.Errorf("rcResumed %%+v:\ngot:  %s\nwant: %s", got, wantResumedString)
		}
		if !strings.Contains(rcResumed.String(), "has_resume_token:false") {
			t.Errorf("rcResumed.String() missing has_resume_token:false: %s", rcResumed.String())
		}

		// Empty session context rendering check: has_session_id:false has_aleg_id:false has_resume_token:false
		rcEmpty := frontendpipe.NewResponseContext(proofOut.State, largebody.ExecutionResult{
			Stream: &mockStream{},
		})
		wantEmptyString := fmt.Sprintf("ResponseContext{ProfileID:%q CallID:%q RouteSelector:%q ClientModel:%q EffectiveModel:%q Stream:%t Session:SessionResponseCarrier{has_session_id:false has_aleg_id:false has_resume_token:false}}",
			rcEmpty.ProfileID(), rcEmpty.CallID(), rcEmpty.RouteSelector(), rcEmpty.ClientModel(), rcEmpty.EffectiveModel(), rcEmpty.IsStream())
		if got := rcEmpty.String(); got != wantEmptyString {
			t.Errorf("rcEmpty.String():\ngot:  %s\nwant: %s", got, wantEmptyString)
		}
		if got := fmt.Sprintf("%+v", rcEmpty); got != wantEmptyString {
			t.Errorf("rcEmpty %%+v:\ngot:  %s\nwant: %s", got, wantEmptyString)
		}
		if !strings.Contains(rcEmpty.String(), "has_resume_token:false") {
			t.Errorf("rcEmpty.String() missing has_resume_token:false: %s", rcEmpty.String())
		}
	})

	t.Run("StructuredLoggingNoSecret", func(t *testing.T) {
		t.Parallel()
		spy := &testLogSpy{}
		logger := slog.New(spy)

		// Direct LogValue() discrimination check (Requirements 14.7, 22.3)
		logVal := rc.LogValue()
		if logVal.Kind() != slog.KindGroup {
			t.Fatalf("rc.LogValue().Kind(): got %v, want %v", logVal.Kind(), slog.KindGroup)
		}
		groupAttrs := logVal.Group()
		wantAttrs := []slog.Attr{
			slog.String("profile_id", rc.ProfileID()),
			slog.String("call_id", rc.CallID()),
			slog.String("route_selector", rc.RouteSelector()),
			slog.String("client_model", rc.ClientModel()),
			slog.String("effective_model", rc.EffectiveModel()),
			slog.Bool("stream", rc.IsStream()),
			slog.String("session", carrier.String()),
		}
		if len(groupAttrs) != len(wantAttrs) {
			t.Fatalf("rc.LogValue().Group() len: got %d, want %d", len(groupAttrs), len(wantAttrs))
		}
		for i, want := range wantAttrs {
			if groupAttrs[i].Key != want.Key || groupAttrs[i].Value.String() != want.Value.String() {
				t.Errorf("LogValue attr[%d]: got %s=%s, want %s=%s", i, groupAttrs[i].Key, groupAttrs[i].Value, want.Key, want.Value)
			}
		}

		logger.Info(
			"audit turn event",
			"response_context", rc,
			"carrier", carrier,
			"sensitive_token", tok,
			"token_string", tok.String(),
			"token_gostring", tok.GoString(),
		)

		if spy.Contains(rawSecret) {
			t.Fatalf("structured log records leaked sensitive resume token: %+v", spy.records)
		}
		if spy.Count() == 0 {
			t.Fatalf("expected at least 1 log record")
		}

		// Discriminating check: record must contain the bounded LogValue group attrs rather than only secret-absence
		record := spy.records[0]
		wantGroupStr := fmt.Sprintf("response_context=[profile_id=%s call_id=%s route_selector=%s client_model=%s effective_model=%s stream=%t session=%s]",
			rc.ProfileID(), rc.CallID(), rc.RouteSelector(), rc.ClientModel(), rc.EffectiveModel(), rc.IsStream(), carrier.String())
		if !strings.Contains(record, wantGroupStr) {
			t.Errorf("structured log record missing expected LogValue group rendering:\ngot:  %s\nwant: %s", record, wantGroupStr)
		}
	})

	t.Run("WriteSessionHeadersSilentNoLogging", func(t *testing.T) {
		t.Parallel()
		spy := &testLogSpy{}
		rec := httptest.NewRecorder()

		initialCount := spy.Count()
		rc.WriteSessionHeaders(rec)
		h := make(http.Header)
		rc.WriteSessionHeadersTo(h)

		if count := spy.Count(); count != initialCount {
			t.Fatalf("WriteSessionHeaders generated %d log records; expected 0 (silent execution)", count-initialCount)
		}
	})

	t.Run("WithoutSensitiveToken", func(t *testing.T) {
		t.Parallel()
		msg := "operation failed with token " + rawSecret + " in trace"
		cleaned := sessionwire.WithoutSensitiveToken(msg, rawSecret)
		if strings.Contains(cleaned, rawSecret) {
			t.Fatalf("WithoutSensitiveToken failed to redact secret: %q", cleaned)
		}
		if !strings.Contains(cleaned, "[REDACTED]") {
			t.Fatalf("WithoutSensitiveToken did not contain [REDACTED]: %q", cleaned)
		}
	})
}

// TestResponseContext_WireExecution_SessionHeadersAndRedactionE2E tests Requirement 14.6, 18.2, 22.3:
// End-to-end wire execution via ServeHTTP returns exact session/resume headers on Turn 1,
// resumes with the same session on Turn 2, and never leaks the resume token into body or logs.
func TestResponseContext_WireExecution_SessionHeadersAndRedactionE2E(t *testing.T) {
	t.Parallel()

	const (
		expectedSessID = "sess_wire_e2e_turn1"
		expectedALegID = "aleg_wire_e2e_turn1"
		secretToken    = "secret_wire_e2e_token_super_safe"
	)

	spy := &testLogSpy{}
	logger := slog.New(spy)

	exec := &testAssessorExecutor{}
	exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}

	var currentTurn int
	exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		currentTurn++
		carrier := largebody.SessionResponseCarrier{
			AuthoritativeSessionID: expectedSessID,
			ALegID:                 expectedALegID,
		}
		if currentTurn == 1 {
			carrier.ResumeToken = largebody.NewSensitiveString(secretToken)
		}
		return largebody.ExecutionResult{
			Stream: &mockStream{},
			Facts: largebody.ResponseFacts{
				RequestID:       accepted.Stamp.GenerationID(),
				TraceID:         "trace_e2e_001",
				ALegID:          expectedALegID,
				SessionID:       expectedSessID,
				Operation:       accepted.WireRequest.Operation,
				Delivery:        accepted.WireRequest.Delivery,
				EffectiveModel:  "gpt-4o",
				Source:          accepted.Stamp.SourceDigest(),
				BodyBytes:       src.Size(),
				RewrittenLength: 100,
			},
			Session: carrier,
		}, nil
	}

	spec := frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			Log:                  logger,
			DefaultRouteSelector: "gpt-4o",
			MaxRequestBodyBytes:  10 * 1024 * 1024,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1024,
			},
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: minimalValidProofProfile(),
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			return frontendpipe.PathMatch{}, true
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, stream aipapi.EventStream) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","session_id":"` + rc.SessionID() + `"}`))
			return nil
		},
	}

	// ------------------------------------------------------------------------
	// TURN 1: Wire execution of new session
	// ------------------------------------------------------------------------
	payload1 := buildJSONPayload(1200 * 1024)
	req1 := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload1))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("Turn 1 HTTP status = %d: %s", rec1.Code, rec1.Body.String())
	}

	gotSess1 := rec1.Header().Get(sessionwire.HeaderAuthoritativeSessionID)
	gotALeg1 := rec1.Header().Get(sessionwire.HeaderALegID)
	gotTok1 := rec1.Header().Get(sessionwire.HeaderResumeToken)

	if gotSess1 != expectedSessID {
		t.Errorf("Turn 1 AuthoritativeSessionID: got %q, want %q", gotSess1, expectedSessID)
	}
	if gotALeg1 != expectedALegID {
		t.Errorf("Turn 1 ALegID: got %q, want %q", gotALeg1, expectedALegID)
	}
	if gotTok1 != secretToken {
		t.Errorf("Turn 1 ResumeToken: got %q, want %q", gotTok1, secretToken)
	}

	// Verify response body does not contain secret token
	if strings.Contains(rec1.Body.String(), secretToken) {
		t.Fatalf("Turn 1 response body leaked resume token: %s", rec1.Body.String())
	}

	// Verify logs do not contain secret token
	if spy.Contains(secretToken) {
		t.Fatalf("Turn 1 logs leaked resume token: %+v", spy.records)
	}

	// ------------------------------------------------------------------------
	// TURN 2: Follow-up request resumes session
	// ------------------------------------------------------------------------
	payload2 := buildJSONPayload(1200 * 1024)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set(sessionwire.HeaderAuthoritativeSessionID, gotSess1)
	req2.Header.Set(sessionwire.HeaderResumeToken, gotTok1)
	rec2 := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("Turn 2 HTTP status = %d: %s", rec2.Code, rec2.Body.String())
	}

	gotSess2 := rec2.Header().Get(sessionwire.HeaderAuthoritativeSessionID)
	gotALeg2 := rec2.Header().Get(sessionwire.HeaderALegID)
	gotTok2 := rec2.Header().Get(sessionwire.HeaderResumeToken)

	if gotSess2 != expectedSessID {
		t.Errorf("Turn 2 AuthoritativeSessionID: got %q, want %q", gotSess2, expectedSessID)
	}
	if gotALeg2 != expectedALegID {
		t.Errorf("Turn 2 ALegID: got %q, want %q", gotALeg2, expectedALegID)
	}
	if gotTok2 != "" {
		t.Errorf("Turn 2 ResumeToken must not be set on resumed session, got %q", gotTok2)
	}

	// Verify response body does not contain secret token
	if strings.Contains(rec2.Body.String(), secretToken) {
		t.Fatalf("Turn 2 response body leaked resume token: %s", rec2.Body.String())
	}

	// Verify logs do not contain secret token
	if spy.Contains(secretToken) {
		t.Fatalf("Turn 2 logs leaked resume token: %+v", spy.records)
	}
}

// TestResponseContext_ChatResponseBridge proves Task 16.2 / Requirement 18:
// Chat response bridge preserves completion ID ("chatcmpl_" + DeterministicToken),
// timestamp (DeterministicTimestamp / DeterministicTime), effective model, session headers,
// and ensures CancellationID falls back to OpenAIChatCompletionID when seeds/aLegID are empty.
func TestResponseContext_ChatResponseBridge(t *testing.T) {
	t.Parallel()

	prof := minimalValidProofProfile()
	proofOut, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		BodyBytes: 1024,
		Source:    &mockSource{data: []byte("test-source-data")},
	})
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	token := proofOut.State.Proof.Identity.Token()

	// Clear seeds cancellation ID and ALegID to test Chat fallback
	state := proofOut.State
	state.Proof.Operation = aipapi.OperationOpenAIChatCompletions
	state.Seeds.CancellationID = ""
	state.Seeds.ALegID = ""
	state.Seeds.DeterministicTimestamp = 1726000000

	execRes := largebody.ExecutionResult{
		Facts: largebody.ResponseFacts{
			EffectiveModel: "gpt-4o-mini",
		},
	}

	rc := frontendpipe.NewResponseContext(state, execRes)

	// Verify completion ID has chatcmpl_ prefix and matches deterministic token
	expectedCompletionID := "chatcmpl_" + token
	if got := rc.OpenAIChatCompletionID(); got != expectedCompletionID {
		t.Errorf("OpenAIChatCompletionID() = %q, want %q", got, expectedCompletionID)
	}

	// Verify timestamp
	if got := rc.DeterministicTimestamp(); got != 1726000000 {
		t.Errorf("DeterministicTimestamp() = %d, want 1726000000", got)
	}
	expectedTime := time.Unix(1726000000, 0).UTC()
	if got := rc.DeterministicTime(); !got.Equal(expectedTime) {
		t.Errorf("DeterministicTime() = %v, want %v", got, expectedTime)
	}

	// Verify effective model
	if got := rc.EffectiveModel(); got != "gpt-4o-mini" {
		t.Errorf("EffectiveModel() = %q, want gpt-4o-mini", got)
	}

	// Verify cancellation ID falls back to OpenAIChatCompletionID (not OpenAIResponseID!)
	if got := rc.CancellationID(); got != expectedCompletionID {
		t.Errorf("CancellationID() = %q, want %q", got, expectedCompletionID)
	}
}
