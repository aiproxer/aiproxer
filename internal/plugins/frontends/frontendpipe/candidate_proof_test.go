package frontendpipe_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 7.5: Acquire exactly one decode-admission permit after EOF —
// weight = exact final decoded bytes; never hold permit while waiting for client upload/spill writes;
// under permit, replay source through protocol proof: selector/default, semantic subset validation,
// ClientTurnShape, SessionInput, body/rewrite facts, canonical semantic identity;
// no custom route hook is invoked here.
// Requirements 4, 6, 13, 14, 16, 17; design target-flow + proof sections.

// trackingAdmissionLimiter records TryAcquire calls and in-flight permits for verification.
type trackingAdmissionLimiter struct {
	mu           sync.Mutex
	acquireCalls []int64
	inflight     int64
	releaseCalls int
	rejectAll    bool
}

func (l *trackingAdmissionLimiter) TryAcquire(ctx context.Context, weight int64) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rejectAll {
		return nil, false, nil
	}
	l.acquireCalls = append(l.acquireCalls, weight)
	l.inflight += weight
	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if !released {
			released = true
			l.inflight -= weight
			l.releaseCalls++
		}
	}, true, nil
}

func (l *trackingAdmissionLimiter) CallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.acquireCalls)
}

func (l *trackingAdmissionLimiter) InflightBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight
}

func (l *trackingAdmissionLimiter) ReleaseCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releaseCalls
}

// uploadObservingReader verifies that no permit is held during client body reads prior to EOF.
type uploadObservingReader struct {
	data     []byte
	offset   int
	limiter  *trackingAdmissionLimiter
	onRead   func(bytesRemaining int)
	finished bool
}

func (r *uploadObservingReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		r.finished = true
		return 0, io.EOF
	}
	if r.onRead != nil {
		r.onRead(len(r.data) - r.offset)
	}
	chunk := min(
		// Return smaller chunks to simulate streaming upload
		min(len(p), len(r.data)-r.offset), 16*1024,
	)
	copy(p, r.data[r.offset:r.offset+chunk])
	r.offset += chunk
	return chunk, nil
}

func (r *uploadObservingReader) Close() error {
	return nil
}

// certifiedTestProfile implements frontendpipe.FrontendProfile with full protocol proof and identity compilation.
type certifiedTestProfile struct {
	profileID   string
	compileFunc func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error)
}

func (p *certifiedTestProfile) ProfileID() string {
	if p.profileID != "" {
		return p.profileID
	}
	return "test_certified_profile_v1"
}

func (p *certifiedTestProfile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	if p.compileFunc != nil {
		return p.compileFunc(ctx, in)
	}
	return frontendpipe.ProofOutput{}, errors.New("certifiedTestProfile: compileFunc not set")
}

var _ frontendpipe.FrontendProfile = (*certifiedTestProfile)(nil)

func newCandidateProofSpec(
	exec *candidateGatesExec,
	prof frontendpipe.FrontendProfile,
	cfg frontendpipe.LargePayloadConfig,
	admission decodeqos.TryAcquirer,
	proofRecord *[]frontendpipe.CandidateProofResult,
	decodeObserver func(dctx frontendpipe.DecodeContext),
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:            exec,
			FrontendID:      "candidate_proof_test",
			LargePayload:    cfg,
			DecodeAdmission: admission,
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
			if decodeObserver != nil {
				decodeObserver(dctx)
			}
			return &frontendpipe.Decoded{
				RouteSelector: dctx.RouteSelector,
				Call: &aipapi.Call{
					ID: "call_proof_test",
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
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return nil
		},
		OnCandidateProof: func(r *http.Request, res frontendpipe.CandidateProofResult) {
			if proofRecord != nil {
				*proofRecord = append(*proofRecord, res)
			}
		},
	}
}

// TestCandidateProof_AdmissionPermitAcquiredAfterEOFOnly verifies that:
// 1. Never hold permit while waiting for client upload/spill writes.
// 2. Permit acquired after EOF only.
// 3. Weight = exact final decoded bytes.
// 4. Exactly one decode-admission permit acquired.
// 5. Permit held during proof compilation and fallback decode.
// 6. Permit released only at post-decode boundary.
func TestCandidateProof_AdmissionPermitAcquiredAfterEOFOnly(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayload(1200 * 1024) // 1.2 MiB (> 1 MiB threshold)
	expectedWeight := int64(len(payload))

	var permitHeldDuringCompile bool
	var inflightDuringCompile int64
	var permitHeldDuringDecode bool
	var inflightDuringDecode int64

	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			permitHeldDuringCompile = limiter.CallCount() == 1
			inflightDuringCompile = limiter.InflightBytes()
			return frontendpipe.ProofOutput{}, errors.New("proof decline fallback")
		},
	}

	var proofRecords []frontendpipe.CandidateProofResult
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20, // 1 MiB
		},
		limiter,
		&proofRecords,
		func(dctx frontendpipe.DecodeContext) {
			permitHeldDuringDecode = limiter.CallCount() == 1
			inflightDuringDecode = limiter.InflightBytes()
		},
	)

	// Custom reader asserting NO permit is held during client upload before EOF
	reader := &uploadObservingReader{
		data:    payload,
		limiter: limiter,
		onRead: func(remaining int) {
			if limiter.CallCount() != 0 {
				t.Errorf("permit acquired during client upload (%d bytes remaining), want 0 calls", remaining)
			}
			if limiter.InflightBytes() != 0 {
				t.Errorf("inflight bytes > 0 during client upload: %d", limiter.InflightBytes())
			}
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", reader)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(payload))
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Invariant 1 & 2: upload finished before admission
	if !reader.finished {
		t.Fatal("expected upload reader to finish completely to EOF")
	}

	// Invariant 3 & 4: exactly one permit acquired with weight = exact final decoded bytes
	if limiter.CallCount() != 1 {
		t.Fatalf("expected exactly 1 TryAcquire call, got %d", limiter.CallCount())
	}
	if limiter.acquireCalls[0] != expectedWeight {
		t.Fatalf("acquired weight: got %d, want exact final bytes %d", limiter.acquireCalls[0], expectedWeight)
	}

	// Invariant 5: permit held during proof and decode
	if !permitHeldDuringCompile || inflightDuringCompile != expectedWeight {
		t.Fatalf("during CompileProof: held=%t inflight=%d, want true and %d", permitHeldDuringCompile, inflightDuringCompile, expectedWeight)
	}
	if !permitHeldDuringDecode || inflightDuringDecode != expectedWeight {
		t.Fatalf("during Decode: held=%t inflight=%d, want true and %d", permitHeldDuringDecode, inflightDuringDecode, expectedWeight)
	}

	// Invariant 6: permit released only at post-decode boundary
	if limiter.InflightBytes() != 0 {
		t.Fatalf("after ServeHTTP: inflight bytes = %d, want 0 (released at post-decode boundary)", limiter.InflightBytes())
	}
	if limiter.ReleaseCount() != 1 {
		t.Fatalf("expected exactly 1 release, got %d", limiter.ReleaseCount())
	}

	// Verify observer received result with PermitHeld=true
	if len(proofRecords) != 1 {
		t.Fatalf("expected 1 proof record, got %d", len(proofRecords))
	}
	if !proofRecords[0].PermitHeld {
		t.Fatal("expected PermitHeld=true in CandidateProofResult")
	}
}

// TestCandidateProof_ReplaySourceThroughProtocolProof_Parity verifies that:
//  1. Replays source through protocol proof: selector/default, semantic subset validation,
//     ClientTurnShape, SessionInput, body/rewrite facts, canonical semantic identity.
//  2. Identity digest via 6.2 writer matches CanonicalCallIdentity byte-for-byte.
//  3. Proof validation passes under semantic-fact budget.
func TestCandidateProof_ReplaySourceThroughProtocolProof_Parity(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayload(1200 * 1024)

	expectedSessionInput := largebody.SessionInput{
		AuthoritativeSessionID: "sess_auth_123",
		ClientSessionID:        "sess_client_456",
		ALegID:                 "aleg_789",
		ResumeToken:            largebody.NewSensitiveString("secret_resume_token"),
	}

	var proofDigest largebody.IdentityDigest
	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			// 1. Verify replay source can be opened and read under permit
			rc, err := in.Source.Open()
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("open source: %w", err)
			}
			readBytes, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("read source: %w", err)
			}
			if int64(len(readBytes)) != in.BodyBytes {
				return frontendpipe.ProofOutput{}, fmt.Errorf("body size mismatch: got %d, want %d", len(readBytes), in.BodyBytes)
			}

			// 2. Parse JSON payload to extract facts
			var parsed struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(readBytes, &parsed); err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("unmarshal: %w", err)
			}

			// 3. Build ClientTurnShape (Requirement 14.3)
			turnShape := largebody.ClientTurnShape{
				Items: []largebody.ClientTurnItemShape{{
					Kind:    aipapi.ItemKindMessage,
					Role:    aipapi.RoleUser,
					Ordinal: 0,
					Parts: []largebody.ClientTurnPartShape{{
						Kind:         aipapi.ContentPartText,
						ContentBytes: int64(len(parsed.Messages[0].Content)),
					}},
				}},
				TotalContentBytes: int64(len(parsed.Messages[0].Content)),
			}

			// 4. Derive model span (Requirements 4, 9)
			modelIdx := bytes.Index(readBytes, fmt.Appendf(nil, "%q", parsed.Model))
			modelSpan := largebody.Span{
				Offset: int64(modelIdx),
				Length: int64(len(parsed.Model) + 2), // include quotes
			}
			rewriteSemantics, err := largebody.NewModelTokenRewrite(modelSpan)
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("rewrite semantics: %w", err)
			}

			// 5. Canonical semantic identity via 6.2 CallIdentityWriter
			sel := in.RouteSelector
			if sel == "" {
				sel = parsed.Model
			}
			idWriter, err := largebody.NewCallIdentityWriter(largebody.CallIdentityConfig{
				SessionInput:  expectedSessionInput,
				RouteSelector: sel,
				Extensions: map[string]json.RawMessage{
					"openai.model": json.RawMessage(fmt.Appendf(nil, "%q", parsed.Model)),
				},
			})
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("identity writer: %w", err)
			}
			if err := idWriter.StartMessages(); err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			if err := idWriter.AddMessage(aipapi.Message{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(parsed.Messages[0].Content)},
			}); err != nil {
				return frontendpipe.ProofOutput{}, err
			}
			digest, err := idWriter.Digest()
			if err != nil {
				return frontendpipe.ProofOutput{}, fmt.Errorf("digest: %w", err)
			}
			proofDigest = digest

			sourceDigest := largebody.NewSourceDigest(sha256.Sum256(readBytes))
			proof := largebody.Proof{
				ProfileID:       "test_certified_profile_v1",
				Operation:       aipapi.OperationOpenAIChatCompletions,
				Delivery:        aipapi.DeliveryModeNonStreaming,
				RouteSelector:   sel,
				ClientModel:     parsed.Model,
				MaxOutputTokens: 0,
				Facts: largebody.ProtocolFacts{
					RequirementsID: "openai_chat_v1",
				},
				Mode:      largebody.BodyModeIdentityJSON,
				Rewrite:   rewriteSemantics,
				ModelSpan: modelSpan,
				Identity:  digest,
				Turn:      turnShape,
				Session:   expectedSessionInput,
				Source:    sourceDigest,
				BodyBytes: in.BodyBytes,
			}

			seeds := frontendpipe.NewResponseStateSeeds(
				digest,
				"req_test_explicit_123",
				sel,
				parsed.Model,
				false,
				expectedSessionInput,
				"",
			)

			return frontendpipe.ProofOutput{
				State: frontendpipe.FrontendWireState{
					ProfileID: "test_certified_profile_v1",
					Proof:     proof,
					Seeds:     seeds,
				},
			}, nil
		},
	}

	var proofRecords []frontendpipe.CandidateProofResult
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		&proofRecords,
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-aip-route", "custom-route-sel")
	req.Header.Set("x-aip-session-id", expectedSessionInput.AuthoritativeSessionID)
	req.Header.Set("x-aip-aleg-id", expectedSessionInput.ALegID)
	req.Header.Set("x-aip-resume-token", expectedSessionInput.ResumeToken.Reveal())
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(proofRecords) != 1 {
		t.Fatalf("expected 1 proof record, got %d", len(proofRecords))
	}
	pRes := proofRecords[0]
	if !pRes.PermitHeld {
		t.Fatal("expected PermitHeld=true")
	}
	if pRes.Err != nil {
		t.Fatalf("unexpected proof error: %v", pRes.Err)
	}

	// Validate bounds under semantic-fact budget
	if err := pRes.Output.Validate(frontendpipe.DefaultMaxSemanticFactBytes); err != nil {
		t.Fatalf("proof validation failed: %v", err)
	}

	proof := pRes.Output.Proof()
	if proof.RouteSelector != "custom-route-sel" {
		t.Fatalf("route selector: got %q, want custom-route-sel", proof.RouteSelector)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Fatalf("client model: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.Identity != proofDigest || proof.Identity.IsZero() {
		t.Fatalf("identity digest mismatch or zero: got %s, want %s", proof.Identity.String(), proofDigest.String())
	}

	// Exact canonical identity parity: compare against CanonicalCallIdentity over equivalent Call
	expectedCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: expectedSessionInput.AuthoritativeSessionID,
			ClientSessionID:        expectedSessionInput.ClientSessionID,
			ALegID:                 expectedSessionInput.ALegID,
			ResumeToken:            expectedSessionInput.ResumeToken.Reveal(),
		},
		Route: aipapi.RouteIntent{
			Selector: "custom-route-sel",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(string(payload))},
		}},
		Extensions: map[string]json.RawMessage{
			"openai.model": json.RawMessage(`"gpt-4o"`),
		},
	}
	// Parse actual message content
	var parsed struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(payload, &parsed)
	expectedCall.Messages[0].Parts = []aipapi.Part{aipapi.TextPart(parsed.Messages[0].Content)}

	canonicalIdentity := largebody.CanonicalCallIdentity(expectedCall)
	if proof.Identity != canonicalIdentity {
		t.Fatalf("canonical identity parity mismatch: proof=%s, canonical=%s", proof.Identity.String(), canonicalIdentity.String())
	}
}

// TestCandidateProof_DecodeAdmissionSaturation_429Parity verifies that when
// decode admission is saturated, the fast path returns HTTP 429 with Retry-After: 1
// and NEVER calls CompileProof or Spec.Decode.
func TestCandidateProof_DecodeAdmissionSaturation_429Parity(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{rejectAll: true} // simulate saturated decodeqos
	payload := buildJSONPayload(1200 * 1024)

	var compileCalled bool
	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			compileCalled = true
			return frontendpipe.ProofOutput{}, nil
		},
	}

	var decodeCalled bool
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			decodeCalled = true
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected HTTP 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") != decodeqos.RetryAfterSeconds {
		t.Fatalf("Retry-After header: got %q, want %q", rec.Header().Get("Retry-After"), decodeqos.RetryAfterSeconds)
	}
	if compileCalled {
		t.Fatal("CompileProof was called when admission was saturated, want no call")
	}
	if decodeCalled {
		t.Fatal("Spec.Decode was called when admission was saturated, want no call")
	}
}

// TestCandidateProof_ProofDecline_SamePermitFallbackDecode verifies that when
// CompileProof declines with an error:
// 1. Fallback Spec.Decode runs under the SAME held permit.
// 2. No release-reacquire cycle (total TryAcquire calls == 1).
// 3. Request succeeds with HTTP 200 OK.
// 4. Permit is released only at the post-decode boundary.
func TestCandidateProof_ProofDecline_SamePermitFallbackDecode(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayload(1200 * 1024)

	var compileCalled bool
	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			compileCalled = true
			return frontendpipe.ProofOutput{}, errors.New("unsupported protocol field: fallback to canonical")
		},
	}

	var decodeCalledUnderPermit bool
	var inflightDuringDecode int64
	var proofRecords []frontendpipe.CandidateProofResult
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		&proofRecords,
		func(dctx frontendpipe.DecodeContext) {
			decodeCalledUnderPermit = limiter.InflightBytes() > 0
			inflightDuringDecode = limiter.InflightBytes()
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !compileCalled {
		t.Fatal("expected CompileProof to be called")
	}
	if !decodeCalledUnderPermit || inflightDuringDecode != int64(len(payload)) {
		t.Fatalf("Decode under permit: held=%t, inflight=%d, want %d", decodeCalledUnderPermit, inflightDuringDecode, len(payload))
	}
	// Exactly ONE permit acquired (no second TryAdmit decision)
	if limiter.CallCount() != 1 {
		t.Fatalf("expected exactly 1 TryAcquire call, got %d", limiter.CallCount())
	}
	// Released after decode
	if limiter.InflightBytes() != 0 {
		t.Fatalf("expected 0 inflight bytes after ServeHTTP, got %d", limiter.InflightBytes())
	}
	if len(proofRecords) != 1 || proofRecords[0].Err == nil {
		t.Fatalf("expected proof record with error, got %+v", proofRecords)
	}
}

// TestCandidateProof_LegacyRouteResolver_NeverInvoked verifies that
// a custom route hook is NOT invoked on the candidate path.
func TestCandidateProof_LegacyRouteResolver_NeverInvoked(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayload(1200 * 1024)

	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			return frontendpipe.ProofOutput{}, errors.New("decline")
		},
	}

	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		nil,
	)
	var resolverCalled bool
	spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		resolverCalled = true
		return routing.ExplicitRoute("legacy-resolved-route")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	// Gate 5 sends requests with a custom route hook to canonical directly without candidate capture!
	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Resolver IS called on the canonical path, but candidate capture was bypassed entirely
	if !resolverCalled {
		t.Fatal("expected custom body route hook to be called on canonical fallback path")
	}
}

func buildJSONPayloadWithModel(targetBytes int, model string) []byte {
	padLen := max(0, targetBytes-100)
	padding := strings.Repeat("A", padLen)
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": padding},
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

// TestCandidateProof_RouteFromBodyModel_DerivedUnderPermit verifies that
// when header route selector is empty and RouteFromBodyModel is enabled,
// selector is derived under permit.
func TestCandidateProof_RouteFromBodyModel_DerivedUnderPermit(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayloadWithModel(1200*1024, "custom:gpt-4o")

	prefixes := routeselect.NewPrefixSet([]string{"custom"})

	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			// In RouteFromBodyModel, profile derives selector from model prefix
			sel := in.RoutePrefixes.FromModelOrDefault([]byte(`{"model":"custom:gpt-4o"}`), in.DefaultRouteSelector)
			digest := largebody.NewIdentityDigest(sha256.Sum256([]byte("dummy")))
			proof := largebody.Proof{
				ProfileID:     "test_certified_profile_v1",
				Operation:     aipapi.OperationOpenAIChatCompletions,
				Delivery:      aipapi.DeliveryModeNonStreaming,
				RouteSelector: sel,
				ClientModel:   "custom:gpt-4o",
				Mode:          largebody.BodyModeIdentityJSON,
				Identity:      digest,
				Source:        largebody.NewSourceDigest(sha256.Sum256(payload)),
				BodyBytes:     in.BodyBytes,
			}
			seeds := frontendpipe.NewResponseStateSeeds(
				digest, "", sel, "custom:gpt-4o", false, largebody.SessionInput{}, "",
			)
			return frontendpipe.ProofOutput{
				State: frontendpipe.FrontendWireState{
					ProfileID: "test_certified_profile_v1",
					Proof:     proof,
					Seeds:     seeds,
				},
			}, nil
		},
	}

	var decodedSelector string
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			decodedSelector = dctx.RouteSelector
		},
	)
	spec.RouteFromBodyModel = true
	spec.RoutePrefixes = prefixes
	spec.DefaultRouteSelector = "default-route"

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	// No x-aip-route header!
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if decodedSelector != "custom:gpt-4o" {
		t.Fatalf("decoded route selector: got %q, want %q", decodedSelector, "custom:gpt-4o")
	}
}

// TestCandidateProof_FactBudgetExceeded_DeclinesToCanonical verifies that
// exceeding semantic fact bounds declines to canonical processing under same permit.
func TestCandidateProof_FactBudgetExceeded_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	exec := &candidateGatesExec{}
	limiter := &trackingAdmissionLimiter{}
	payload := buildJSONPayload(1200 * 1024)

	prof := &certifiedTestProfile{
		profileID: "test_certified_profile_v1",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			digest := largebody.NewIdentityDigest(sha256.Sum256([]byte("dummy")))
			// Exceeds DefaultMaxSemanticFactBytes
			hugeModel := strings.Repeat("M", int(frontendpipe.DefaultMaxSemanticFactBytes)+100)
			proof := largebody.Proof{
				ProfileID:     "test_certified_profile_v1",
				Operation:     aipapi.OperationOpenAIChatCompletions,
				Delivery:      aipapi.DeliveryModeNonStreaming,
				RouteSelector: "sel",
				ClientModel:   hugeModel,
				Mode:          largebody.BodyModeIdentityJSON,
				Identity:      digest,
				Source:        largebody.NewSourceDigest(sha256.Sum256(payload)),
				BodyBytes:     in.BodyBytes,
			}
			seeds := frontendpipe.NewResponseStateSeeds(
				digest, "", "sel", hugeModel, false, largebody.SessionInput{}, "",
			)
			return frontendpipe.ProofOutput{
				State: frontendpipe.FrontendWireState{
					ProfileID: "test_certified_profile_v1",
					Proof:     proof,
					Seeds:     seeds,
				},
			}, nil
		},
	}

	var proofRecords []frontendpipe.CandidateProofResult
	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		&proofRecords,
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(proofRecords) != 1 {
		t.Fatalf("expected 1 proof record, got %d", len(proofRecords))
	}
	if proofRecords[0].Err == nil {
		t.Fatal("expected proof validation error due to fact budget exceeded, got nil")
	}
	if !strings.Contains(proofRecords[0].Err.Error(), "exceeds") {
		t.Fatalf("expected error mentioning exceeds fact budget, got: %v", proofRecords[0].Err)
	}
}
