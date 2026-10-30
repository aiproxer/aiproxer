package largebody_test

// =============================================================================
// Task 6.4: Economic and Checkpoint Identity Parity Proofs
//
// Spec Requirements:
// - Requirement 15: Metering, Accounting, and Billing Need Wire-Native Evidence.
//   15.1: Wire path shall not call current Call-cloning helpers.
//   15.2: Wire-native frontend/backend ingress checkpoint capture from exact bounded facts.
//   15.3: Wire checkpoint storage shall not retain a hidden full canonical Call;
//         widening/retry integrity uses immutable source/proof digests.
//   15.7: Economic/metering facts, reservations, settlement, and retry idempotency
//         occur exactly once and preserve current failure policy.
// - Requirement 16: Deterministic Request and Economic Identity Parity.
//   16.1: Inventory of stable Call IDs, tokens, timestamps, checkpoint IDs, response IDs.
//   16.2: Certified profile produces exact canonical semantic identity digest without
//         retaining proportional prompt content.
//   16.3: Canonical-digest implementation differential-tested against diag oracle.
//   16.4: Helpers derive stable Call ID/token/timestamp from that digest so wire trace/economic
//         identity and deterministic frontend fields remain path-stable.
//   16.5: A raw-body-only hash that differs from canonical stable identity is NOT
//         sufficient for economic identity.
//   16.6: Caller/provider-supplied explicit IDs retain current precedence.
// - Requirement 18: Preserve Frontend Response, Keepalive, and Session-Carrier State.
//   18.7: Deterministic response IDs/timestamps derived from canonical Call hashes use
//         Requirement 16's equivalent digest.
//
// Design Sections:
// - Section 6: Protocol Proof and Canonical Semantic Identity.
// - Section 10: Wire-Native Checkpoint and Bounded Evidence.
// - Section 11: Post-Commit Runtime Facts: No Shadow Call.
//
// Prior Art / Seams:
// - Task 1.7 freeze tests: diag/stable_identity_freeze_test.go, checkpoint/identity_freeze_test.go.
// - Task 6.1: diag.StableCallTokenFromSum, diag.StableCallIDFromSum, diag.StableUnixFromSum,
//   diag.StableTimeFromSum factoring diag helpers around an already-computed canonical sum.
// - Task 6.2: largebody.CallIdentityWriter, largebody.IdentityDigest, largebody.StreamingEscapeWriter.
// - Task 6.3: differential identity corpus and fuzz tests across diverse shapes.
// - Task 10 (Future Scope): wire-native checkpoint constructors (CaptureWireFrontendIngress,
//   CaptureWireBackendIngress). Task 6.4 proves the parity rule on the digest/FromSum seam
//   and documents the Task 10 wiring obligation.
// =============================================================================

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// helperIntPtr returns a pointer to an int value.
//
//go:fix inline
func helperIntPtr(v int) *int { return new(v) }

// streamCallIdentityWithChunkSize streams a Call through CallIdentityWriter in chunks.
func streamCallIdentityWithChunkSize(t *testing.T, call *aipapi.Call, chunkSize int) (largebody.IdentityDigest, error) {
	t.Helper()
	cfg := largebody.CallIdentityConfig{
		ExplicitID:         call.ID,
		Session:            call.Session,
		Route:              call.Route,
		Instructions:       call.Instructions,
		PreviousResponseID: call.PreviousResponseID,
		PromptCacheKey:     call.PromptCacheKey,
		SemanticExtensions: call.SemanticExtensions,
		Tools:              call.Tools,
		ToolChoice:         call.ToolChoice,
		Options:            call.Options,
		Extensions:         call.Extensions,
	}

	w, err := largebody.NewCallIdentityWriter(cfg)
	if err != nil {
		return largebody.IdentityDigest{}, err
	}

	if call.Messages != nil {
		if err := w.StartMessages(); err != nil {
			return largebody.IdentityDigest{}, err
		}
		for _, msg := range call.Messages {
			if len(msg.Parts) == 1 && msg.Parts[0].Kind == aipapi.PartText {
				mw, err := w.BeginMessage(msg.Role)
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				tw, err := mw.BeginTextPart()
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				textBytes := []byte(msg.Parts[0].Text)
				if chunkSize <= 0 {
					chunkSize = len(textBytes)
				}
				for offset := 0; offset < len(textBytes); offset += chunkSize {
					end := min(offset+chunkSize, len(textBytes))
					if _, err := tw.Write(textBytes[offset:end]); err != nil {
						return largebody.IdentityDigest{}, err
					}
				}
				if err := tw.Close(); err != nil {
					return largebody.IdentityDigest{}, err
				}
				if err := mw.EndMessage(); err != nil {
					return largebody.IdentityDigest{}, err
				}
			} else {
				if err := w.AddMessage(msg); err != nil {
					return largebody.IdentityDigest{}, err
				}
			}
		}
	}

	if call.Items != nil {
		if err := w.StartItems(); err != nil {
			return largebody.IdentityDigest{}, err
		}
		for _, item := range call.Items {
			if item.Kind == aipapi.ItemKindMessage && len(item.Content) == 1 && item.Content[0].Kind == aipapi.ContentPartText &&
				item.Reference == nil && item.ToolCall == nil && item.ToolResult == nil && item.Reasoning == nil && item.Compaction == nil && item.Extension == nil {
				iw, err := w.BeginMessageItem(item.ID, item.Status, item.Role, item.Phase)
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				tw, err := iw.BeginTextContentPart()
				if err != nil {
					return largebody.IdentityDigest{}, err
				}
				textBytes := []byte(item.Content[0].Text)
				if chunkSize <= 0 {
					chunkSize = len(textBytes)
				}
				for offset := 0; offset < len(textBytes); offset += chunkSize {
					end := min(offset+chunkSize, len(textBytes))
					if _, err := tw.Write(textBytes[offset:end]); err != nil {
						return largebody.IdentityDigest{}, err
					}
				}
				if err := tw.Close(); err != nil {
					return largebody.IdentityDigest{}, err
				}
				if err := iw.EndItem(); err != nil {
					return largebody.IdentityDigest{}, err
				}
			} else {
				if err := w.AddItem(item); err != nil {
					return largebody.IdentityDigest{}, err
				}
			}
		}
	}

	return w.Digest()
}

// -----------------------------------------------------------------------------
// Test 1: Canonical vs Writer-Derived Digest Core Parity (Req 16.2, 16.4)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_CanonicalVsWriterDigest(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("You are a helpful assistant.")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Calculate economic identity parity.")}},
		},
		Options: aipapi.GenerationOptions{
			MaxOutputTokens: new(128),
			ReasoningEffort: "high",
		},
	}

	// 1. Canonical oracle derived from full Call
	canonSum := diag.StableCallSum(call)
	canonToken := diag.StableCallToken(call)
	canonCallID := diag.StableCallID(call)
	canonUnix := diag.StableUnix(call)
	canonTime := diag.StableTime(call)

	// 2. CanonicalCallIdentity helper
	canonDigest := largebody.CanonicalCallIdentity(call)
	if canonDigest.Sum() != canonSum {
		t.Fatalf("CanonicalCallIdentity sum mismatch: %x vs %x", canonDigest.Sum(), canonSum)
	}

	// 3. Streaming CallIdentityWriter without prompt retention
	writerDigest, err := streamCallIdentityWithChunkSize(t, call, 7)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	// Assert byte-for-byte sum parity
	if writerDigest.Sum() != canonSum {
		t.Fatalf("writer digest sum mismatch:\ngot:  %x\nwant: %x", writerDigest.Sum(), canonSum)
	}

	// Assert token parity
	if writerDigest.Token() != canonToken {
		t.Fatalf("token mismatch: got %q, want %q", writerDigest.Token(), canonToken)
	}
	if fromSumToken := diag.StableCallTokenFromSum(writerDigest.Sum()); fromSumToken != canonToken {
		t.Fatalf("diag.StableCallTokenFromSum mismatch: got %q, want %q", fromSumToken, canonToken)
	}

	// Assert fallback Call ID parity
	if writerDigest.CallID("") != canonCallID {
		t.Fatalf("fallback CallID mismatch: got %q, want %q", writerDigest.CallID(""), canonCallID)
	}
	if fromSumID := diag.StableCallIDFromSum("", writerDigest.Sum()); fromSumID != canonCallID {
		t.Fatalf("diag.StableCallIDFromSum mismatch: got %q, want %q", fromSumID, canonCallID)
	}

	// Assert Unix timestamp parity
	if writerDigest.Unix() != canonUnix {
		t.Fatalf("Unix mismatch: got %d, want %d", writerDigest.Unix(), canonUnix)
	}
	if fromSumUnix := diag.StableUnixFromSum(writerDigest.Sum()); fromSumUnix != canonUnix {
		t.Fatalf("diag.StableUnixFromSum mismatch: got %d, want %d", fromSumUnix, canonUnix)
	}

	// Assert UTC Time parity
	if fromSumTime := diag.StableTimeFromSum(writerDigest.Sum()); !fromSumTime.Equal(canonTime) {
		t.Fatalf("diag.StableTimeFromSum mismatch: got %v, want %v", fromSumTime, canonTime)
	}
}

// -----------------------------------------------------------------------------
// Test 2: Explicit Caller-ID Precedence (Req 15, 16.6)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_ExplicitCallerIDPrecedence(t *testing.T) {
	t.Parallel()

	baseCall := func(explicitID string) *aipapi.Call {
		return &aipapi.Call{
			ID:    explicitID,
			Route: aipapi.RouteIntent{Selector: "anthropic:claude-3-5-sonnet"},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Test explicit caller ID precedence")}},
			},
		}
	}

	testCases := []struct {
		name       string
		explicitID string
		wantIDFunc func(tok string) string
	}{
		{
			name:       "clean explicit ID wins verbatim",
			explicitID: "caller-req-12345",
			wantIDFunc: func(tok string) string { return "caller-req-12345" },
		},
		{
			name:       "untrimmed explicit ID trimmed",
			explicitID: "   caller-req-trimmed-678   ",
			wantIDFunc: func(tok string) string { return "caller-req-trimmed-678" },
		},
		{
			name:       "empty ID defaults to call_ prefix with token",
			explicitID: "",
			wantIDFunc: func(tok string) string { return "call_" + tok },
		},
		{
			name:       "whitespace-only ID defaults to call_ prefix with token",
			explicitID: "     ",
			wantIDFunc: func(tok string) string { return "call_" + tok },
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := baseCall(tc.explicitID)

			// 1. Canonical Call oracle
			canonID := diag.StableCallID(call)
			canonSum := diag.StableCallSum(call)
			canonToken := diag.StableCallToken(call)

			wantExpected := tc.wantIDFunc(canonToken)
			if canonID != wantExpected {
				t.Fatalf("canonical StableCallID: got %q, want %q", canonID, wantExpected)
			}

			// 2. Writer-derived IdentityDigest
			writerDigest, err := streamCallIdentityWithChunkSize(t, call, 11)
			if err != nil {
				t.Fatalf("streamCallIdentity failed: %v", err)
			}

			// Verify sum matches regardless of explicit ID (since Call.ID is excluded from sum)
			if writerDigest.Sum() != canonSum {
				t.Fatalf("writer digest sum mismatch:\ngot:  %x\nwant: %x", writerDigest.Sum(), canonSum)
			}

			// Verify writer CallID method preserves explicit ID precedence
			wireID := writerDigest.CallID(tc.explicitID)
			if wireID != wantExpected {
				t.Fatalf("writerDigest.CallID(%q): got %q, want %q", tc.explicitID, wireID, wantExpected)
			}

			// Verify diag.StableCallIDFromSum preserves explicit ID precedence
			fromSumID := diag.StableCallIDFromSum(tc.explicitID, writerDigest.Sum())
			if fromSumID != wantExpected {
				t.Fatalf("diag.StableCallIDFromSum(%q, sum): got %q, want %q", tc.explicitID, fromSumID, wantExpected)
			}

			// Assert cross-path equivalence
			if canonID != wireID || wireID != fromSumID {
				t.Fatalf("parity breakdown across seams: canon=%q, wire=%q, fromSum=%q", canonID, wireID, fromSumID)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test 3: Downstream Deterministic Response and Trace IDs Parity (Req 16.1, 18.7)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_DownstreamDeterministicIDs(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:model-x"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Downstream IDs parity check")}},
		},
	}

	canonSum := diag.StableCallSum(call)
	canonToken := diag.StableCallToken(call)

	writerDigest, err := streamCallIdentityWithChunkSize(t, call, 5)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	if writerDigest.Sum() != canonSum {
		t.Fatalf("sum mismatch: %x vs %x", writerDigest.Sum(), canonSum)
	}

	wireToken := writerDigest.Token()
	if wireToken != canonToken {
		t.Fatalf("token mismatch: %q vs %q", wireToken, canonToken)
	}

	type DownstreamIDs struct {
		TraceID            string
		OpenAIResponseID   string
		OpenAIMessageID    string
		OpenAIChatCmplID   string
		AnthropicMessageID string
	}

	deriveDownstreamIDs := func(token string, callID string) DownstreamIDs {
		return DownstreamIDs{
			TraceID:            callID,
			OpenAIResponseID:   "resp_" + token,
			OpenAIMessageID:    "msg_resp_" + token,
			OpenAIChatCmplID:   "chatcmpl_" + token,
			AnthropicMessageID: "msg_" + token,
		}
	}

	// 1. Fallback ID scenario
	canonFallback := deriveDownstreamIDs(canonToken, diag.StableCallID(call))
	wireFallback := deriveDownstreamIDs(wireToken, writerDigest.CallID(""))
	if canonFallback != wireFallback {
		t.Fatalf("downstream fallback IDs mismatch:\ncanon: %+v\nwire:  %+v", canonFallback, wireFallback)
	}

	// 2. Explicit ID scenario
	explicitID := "cust-session-trace-777"
	call.ID = explicitID
	canonExplicit := deriveDownstreamIDs(canonToken, diag.StableCallID(call))
	wireExplicit := deriveDownstreamIDs(wireToken, writerDigest.CallID(explicitID))
	if canonExplicit != wireExplicit {
		t.Fatalf("downstream explicit IDs mismatch:\ncanon: %+v\nwire:  %+v", canonExplicit, wireExplicit)
	}
	if wireExplicit.TraceID != explicitID {
		t.Fatalf("trace ID must equal explicit ID: %q", wireExplicit.TraceID)
	}
}

// -----------------------------------------------------------------------------
// Test 4: Metering Checkpoint Identity Parity (Req 15.2, 16.1)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_CheckpointIdentityDeterminism(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4o-mini"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Checkpoint parity test")}},
		},
	}

	writerDigest, err := streamCallIdentityWithChunkSize(t, call, 8)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	// Subtest A: Fallback derived ID
	t.Run("fallback derived ID", func(t *testing.T) {
		t.Parallel()
		canonReqID := diag.StableCallID(call)
		wireReqID := writerDigest.CallID("")

		if canonReqID != wireReqID {
			t.Fatalf("request ID mismatch: canon=%q, wire=%q", canonReqID, wireReqID)
		}

		// Frontend Ingress Identity
		feFactID1, feSrcID1, feSeq1 := checkpoint.FrontendIngressIdentity(canonReqID)
		feFactID2, feSrcID2, feSeq2 := checkpoint.FrontendIngressIdentity(wireReqID)

		if feFactID1 != feFactID2 || feSrcID1 != feSrcID2 || feSeq1 != feSeq2 {
			t.Fatalf("FE ingress identity mismatch: (%q, %q, %d) vs (%q, %q, %d)",
				feFactID1, feSrcID1, feSeq1, feFactID2, feSrcID2, feSeq2)
		}
		expectedPrefix := "fe-ingress:call_" + writerDigest.Token()
		if feFactID1 != expectedPrefix || feSrcID1 != expectedPrefix {
			t.Fatalf("unexpected FE fact identity: %q", feFactID1)
		}
		if feSeq1 != checkpoint.IngressSequence {
			t.Fatalf("FE sequence must equal IngressSequence=1, got %d", feSeq1)
		}
	})

	// Subtest B: Explicit caller ID
	t.Run("explicit caller ID", func(t *testing.T) {
		t.Parallel()
		explicitID := "ext-caller-id-999"
		callWithID := *call
		callWithID.ID = explicitID

		canonReqID := diag.StableCallID(&callWithID)
		wireReqID := writerDigest.CallID(explicitID)

		if canonReqID != explicitID || wireReqID != explicitID {
			t.Fatalf("explicit ID failed: canon=%q, wire=%q", canonReqID, wireReqID)
		}

		feFactID1, feSrcID1, feSeq1 := checkpoint.FrontendIngressIdentity(canonReqID)
		feFactID2, feSrcID2, feSeq2 := checkpoint.FrontendIngressIdentity(wireReqID)

		if feFactID1 != feFactID2 || feSrcID1 != feSrcID2 || feSeq1 != feSeq2 {
			t.Fatalf("explicit FE ingress identity mismatch: (%q, %q, %d) vs (%q, %q, %d)",
				feFactID1, feSrcID1, feSeq1, feFactID2, feSrcID2, feSeq2)
		}
		if feFactID1 != "fe-ingress:ext-caller-id-999" {
			t.Fatalf("unexpected explicit FE fact identity: %q", feFactID1)
		}
	})

	// Subtest C: Backend attempt identity namespace separation
	t.Run("backend attempt namespace separation", func(t *testing.T) {
		t.Parallel()
		attemptID := "attempt-xyz-001"
		beFactID, beSrcID, beSeq := checkpoint.BackendIngressIdentity(attemptID)

		if beFactID != "be-ingress:"+attemptID || beSrcID != "be-ingress:"+attemptID {
			t.Fatalf("BE ingress identity format error: %q / %q", beFactID, beSrcID)
		}
		if beSeq != checkpoint.IngressSequence {
			t.Fatalf("BE sequence must be IngressSequence=1, got %d", beSeq)
		}

		// Verify FE and BE namespaces can NEVER collide
		reqID := writerDigest.CallID("")
		feFactID, _, _ := checkpoint.FrontendIngressIdentity(reqID)
		if feFactID == beFactID {
			t.Fatalf("FE and BE fact identities collided: %q", feFactID)
		}
	})
}

// -----------------------------------------------------------------------------
// Test 5: Checkpoint Fact SourceEventKey and Idempotency Parity (Req 15.7, 16.1)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_FactSourceEventKeyAndIdempotency(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715620100, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-economic-parity")}

	call := &aipapi.Call{
		ID:    "req-parity-fact",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sess-auth-canonical",
			ClientSessionID:        "sess-client-hint",
			ALegID:                 "aleg-fixed-1",
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Fact idempotency key test")}},
		},
	}

	// 1. Canonical path: capture frontend ingress using standard CaptureFrontendIngress
	canonSnap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         *call,
		Scope:        sc,
		CheckpointID: "cp-parity-canonical",
		StreamID:     "customer-request:req-parity-fact",
		TraceID:      "trace-parity-canonical",
		Now:          now,
	})
	if err != nil {
		t.Fatalf("CaptureFrontendIngress failed: %v", err)
	}

	factID1, srcID1, seq1 := checkpoint.FrontendIngressIdentity(call.ID)
	canonFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint: canonSnap.Public,
		FactID:     factID1,
		Sequence:   seq1,
		SourceID:   srcID1,
		Now:        now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FactFromFrontendIngress (canonical) failed: %v", err)
	}

	// 2. Wire path: derived from bounded facts (matching Task 10's wire-native ingress constructor)
	writerDigest, err := streamCallIdentityWithChunkSize(t, call, 6)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	wireReqID := writerDigest.CallID(call.ID)
	if wireReqID != call.ID {
		t.Fatalf("wireReqID mismatch: %q vs %q", wireReqID, call.ID)
	}

	// Construct public Checkpoint from wire bounded facts (no Call clone!)
	wireCorr := metering.Correlation{
		RequestID: wireReqID,
		ALegID:    call.Session.ALegID,
		SessionID: call.Session.CorrelationID(),
		TraceID:   "trace-parity-canonical",
	}

	wireCheckpoint := metering.Checkpoint{
		CheckpointID: "cp-parity-wire",
		StreamID:     "customer-request:" + wireReqID,
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  metering.PerspectiveCustomer,
		Correlation:  wireCorr,
		Scope:        sc.Clone(),
		Source:       metering.SourceObserved,
		Authority:    metering.AuthorityEstimated,
		Presence:     metering.PresenceUnknown,
		CapturedAt:   now,
	}
	if err := wireCheckpoint.Validate(); err != nil {
		t.Fatalf("wireCheckpoint.Validate failed: %v", err)
	}

	factID2, srcID2, seq2 := checkpoint.FrontendIngressIdentity(wireReqID)
	wireFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint: wireCheckpoint,
		FactID:     factID2,
		Sequence:   seq2,
		SourceID:   srcID2,
		Now:        now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FactFromFrontendIngress (wire) failed: %v", err)
	}

	// 3. Assert exact identity, SourceEventKey and IdempotencyKey parity
	if canonFact.FactID != wireFact.FactID {
		t.Fatalf("FactID mismatch: %q vs %q", canonFact.FactID, wireFact.FactID)
	}
	if canonFact.SourceID != wireFact.SourceID {
		t.Fatalf("SourceID mismatch: %q vs %q", canonFact.SourceID, wireFact.SourceID)
	}
	if canonFact.StreamID != wireFact.StreamID {
		t.Fatalf("StreamID mismatch: %q vs %q", canonFact.StreamID, wireFact.StreamID)
	}
	if canonFact.Correlation != wireFact.Correlation {
		t.Fatalf("Correlation mismatch:\ncanon: %+v\nwire:  %+v", canonFact.Correlation, wireFact.Correlation)
	}

	// Retry stability / idempotency key parity
	canonSourceEventKey := canonFact.SourceEventKey()
	wireSourceEventKey := wireFact.SourceEventKey()
	if canonSourceEventKey != wireSourceEventKey {
		t.Fatalf("SourceEventKey parity failed:\ncanon: %q\nwire:  %q", canonSourceEventKey, wireSourceEventKey)
	}

	canonIdempotencyKey := canonFact.IdempotencyKey()
	wireIdempotencyKey := wireFact.IdempotencyKey()
	if canonIdempotencyKey != wireIdempotencyKey {
		t.Fatalf("IdempotencyKey parity failed:\ncanon: %q\nwire:  %q", canonIdempotencyKey, wireIdempotencyKey)
	}
}

// -----------------------------------------------------------------------------
// Test 6: SourceDigest Cryptographic & Type Separation (Req 16.5)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_SourceDigestIndependence(t *testing.T) {
	t.Parallel()

	// Requirement 16.5: "A raw-body-only hash that differs from canonical stable
	// identity is not sufficient for economic identity."
	//
	// SourceDigest (raw body wire SHA-256) and IdentityDigest (canonical semantic
	// JSON SHA-256) are distinct types in package largebody. They must never be
	// interchangeable or substituted for one another.

	rawJSON := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hello raw wire!"}],"temperature":0.7}`)
	rawSum := sha256.Sum256(rawJSON)
	sourceDigest := largebody.NewSourceDigest(rawSum)

	// Construct equivalent canonical Call
	temp := 0.7
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "gpt-4o"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Hello raw wire!")}},
		},
		Options: aipapi.GenerationOptions{
			Temperature: &temp,
		},
	}

	identityDigest, err := streamCallIdentityWithChunkSize(t, call, 4)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	// 1. Distinct byte representations: raw wire hash differs from canonical stable hash
	if sourceDigest.Sum() == identityDigest.Sum() {
		t.Fatalf("CRITICAL SECURITY INVARIANT VIOLATION: SourceDigest and IdentityDigest must differ.\nRaw:       %x\nCanonical: %x",
			sourceDigest.Sum(), identityDigest.Sum())
	}

	// 2. Both must be non-zero
	if sourceDigest.Sum() == [32]byte{} {
		t.Fatal("SourceDigest is zero")
	}
	if identityDigest.IsZero() {
		t.Fatal("IdentityDigest is zero")
	}

	// 3. Verify that raw body hash alone CANNOT produce valid canonical Token or CallID
	rawToken := hex.EncodeToString(rawSum[:8])
	canonicalToken := identityDigest.Token()
	if rawToken == canonicalToken {
		t.Fatalf("raw body token collided with canonical identity token: %q", rawToken)
	}

	// 4. Verify type separation: IdentityDigest provides CallID, Token, Unix methods;
	// SourceDigest only provides raw Sum() and does NOT implement identity methods.
	// This compile-time type separation ensures a raw wire hash cannot silently satisfy
	// economic identity contracts.
	if identityDigest.CallID("") == "call_"+rawToken {
		t.Fatal("IdentityDigest.CallID matched raw token")
	}
}

// -----------------------------------------------------------------------------
// Test 7: Multi-Variant Identity Parity Corpus (Req 15, 16.2, 16.3)
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_CorpusDifferential(t *testing.T) {
	t.Parallel()

	type corpusCase struct {
		name       string
		explicitID string
		buildCall  func() *aipapi.Call
	}

	cases := []corpusCase{
		{
			name: "basic single text message",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "stub:model"},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Hello world")}},
					},
				}
			},
		},
		{
			name:       "multi-turn conversation with explicit caller ID",
			explicitID: "caller-conv-888",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					ID:    "caller-conv-888",
					Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("System prompt instructions")}},
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Question 1")}},
						{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("Answer 1")}},
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Question 2")}},
					},
				}
			},
		},
		{
			name: "items turn model with parts",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "openresponses:v1"},
					Items: []aipapi.Item{
						{
							ID:     "item-1",
							Kind:   aipapi.ItemKindMessage,
							Role:   aipapi.RoleUser,
							Status: "completed",
							Content: []aipapi.ContentPart{
								{Kind: aipapi.ContentPartText, Text: "Items turn content"},
							},
						},
					},
				}
			},
		},
		{
			name: "tools and schema parameters",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Run tools")}},
					},
					Tools: []aipapi.ToolDef{
						{
							Name:        "get_weather",
							Description: "Get weather for location",
							Parameters:  json.RawMessage(`{"type":"object","properties":{"loc":{"type":"string"}}}`),
						},
						{
							Name:        "search_db",
							Description: "Query database",
							Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
						},
					},
					ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
				}
			},
		},
		{
			name: "generation options and reasoning effort",
			buildCall: func() *aipapi.Call {
				temp := 0.2
				topP := 0.95
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "openai:o3-mini"},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Reasoning model query")}},
					},
					Options: aipapi.GenerationOptions{
						Temperature:     &temp,
						TopP:            &topP,
						MaxOutputTokens: new(1024),
						ReasoningEffort: "high",
					},
				}
			},
		},
		{
			name: "heavy unicode, emojis, and line separators",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "stub:model"},
					Messages: []aipapi.Message{
						{
							Role: aipapi.RoleUser,
							Parts: []aipapi.Part{
								aipapi.TextPart("Unicode: 日本語, Русский, 🌟🚀🔥, \u2028line sep\u2029, \U0001F600 emoji!"),
							},
						},
					},
				}
			},
		},
		{
			name: "JSON string escaping and HTML-sensitive characters",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "stub:model"},
					Messages: []aipapi.Message{
						{
							Role: aipapi.RoleUser,
							Parts: []aipapi.Part{
								aipapi.TextPart("Escapes: \"quotes\", \\backslashes\\, \nnew\nlines\t\tand <HTML> &amp; tags"),
							},
						},
					},
				}
			},
		},
		{
			name: "session header precedence",
			buildCall: func() *aipapi.Call {
				return &aipapi.Call{
					Route: aipapi.RouteIntent{Selector: "stub:model"},
					Session: aipapi.SessionRef{
						AuthoritativeSessionID: "sess-auth-win",
						ClientSessionID:        "sess-client-lose",
						ALegID:                 "aleg-fixed",
					},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Session precedence test")}},
					},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := tc.buildCall()

			// 1. Canonical oracle derived from Call
			canonSum := diag.StableCallSum(call)
			canonToken := diag.StableCallToken(call)
			canonID := diag.StableCallID(call)
			canonUnix := diag.StableUnix(call)
			canonTime := diag.StableTime(call)

			// 2. Stream through CallIdentityWriter across various chunk sizes
			for _, chunkSize := range []int{1, 3, 17, 128} {
				writerDigest, err := streamCallIdentityWithChunkSize(t, call, chunkSize)
				if err != nil {
					t.Fatalf("[chunkSize=%d] streamCallIdentity failed: %v", chunkSize, err)
				}

				if writerDigest.Sum() != canonSum {
					t.Fatalf("[chunkSize=%d] Sum mismatch:\ngot:  %x\nwant: %x", chunkSize, writerDigest.Sum(), canonSum)
				}
				if writerDigest.Token() != canonToken {
					t.Fatalf("[chunkSize=%d] Token mismatch: got %q, want %q", chunkSize, writerDigest.Token(), canonToken)
				}
				if gotID := writerDigest.CallID(tc.explicitID); gotID != canonID {
					t.Fatalf("[chunkSize=%d] CallID mismatch: got %q, want %q", chunkSize, gotID, canonID)
				}
				if writerDigest.Unix() != canonUnix {
					t.Fatalf("[chunkSize=%d] Unix mismatch: got %d, want %d", chunkSize, writerDigest.Unix(), canonUnix)
				}
				if !diag.StableTimeFromSum(writerDigest.Sum()).Equal(canonTime) {
					t.Fatalf("[chunkSize=%d] Time mismatch: got %v, want %v", chunkSize, diag.StableTimeFromSum(writerDigest.Sum()), canonTime)
				}

				// Checkpoint identity parity
				feFactID1, feSrcID1, seq1 := checkpoint.FrontendIngressIdentity(canonID)
				feFactID2, feSrcID2, seq2 := checkpoint.FrontendIngressIdentity(writerDigest.CallID(tc.explicitID))
				if feFactID1 != feFactID2 || feSrcID1 != feSrcID2 || seq1 != seq2 {
					t.Fatalf("[chunkSize=%d] FE ingress mismatch: %q vs %q", chunkSize, feFactID1, feFactID2)
				}
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Test 8: Task 10 Wiring Obligation Documentation & Contract Enforcement
// -----------------------------------------------------------------------------
func TestEconomicIdentityParity_Task10WiringObligationDocumentation(t *testing.T) {
	t.Parallel()

	// =========================================================================
	// TASK 10 WIRING OBLIGATION DOCUMENTATION
	//
	// Per Task 6.4 scope:
	// "If wire-native checkpoint constructors don't exist yet (Task 10 scope),
	// prove the parity rule on the digest/FromSum seam and document the Task 10
	// wiring obligation — do not build wire checkpoints here."
	//
	// In Task 10 (Tasks 10.1, 10.2, 10.3), the wire fast-path must create
	// wire-native checkpoint constructors:
	//   1. CaptureWireFrontendIngress(WireFrontendIngressInput) (Snapshot, error)
	//   2. CaptureWireBackendIngress(WireBackendIngressInput) (Snapshot, error)
	//
	// Obligations that Task 10 MUST fulfill:
	// 1. [No Call Retention - Req 15.1, 15.3]:
	//    The wire checkpoint snapshot MUST NOT clone, wrap, or retain aipapi.Call.
	//    The Call field in the Snapshot must remain zero/empty on the wire path.
	//    Widening and attempt verification must use SourceDigest and IdentityDigest
	//    rather than re-reading the prompt.
	//
	// 2. [Exact Bounded Facts - Req 15.2, Design 10]:
	//    Input must be constructed strictly from bounded LargeBodyProof facts:
	//      - RequestID: IdentityDigest.CallID(proof.ExplicitID)
	//      - IdentityDigest: LargeBodyProof.CanonicalDigest (sum [32]byte)
	//      - SourceDigest: LargeBodyProof.SourceDigest (sum [32]byte)
	//      - Scope: PrincipalScopeView
	//      - TraceID: context/caller trace ID, defaulting to RequestID
	//      - StreamID: "customer-request:" + RequestID
	//      - SessionID: SessionInput.AuthoritativeSessionID (or ClientSessionID)
	//      - ALegID: SessionInput.ALegID
	//      - Model: LargeBodyProof.ClientModel
	//      - MaxOutputQuantity: LargeBodyProof.MaxOutputTokens
	//
	// 3. [Identity Parity - Req 16.1, 16.4, 16.6]:
	//    Wire checkpointing must use the identical checkpoint.FrontendIngressIdentity(requestID)
	//    and checkpoint.BackendIngressIdentity(attemptID) functions to ensure:
	//      - FactID = "fe-ingress:" + requestID
	//      - SourceID = "fe-ingress:" + requestID
	//      - Sequence = checkpoint.IngressSequence (1)
	//    Explicit caller IDs MUST retain precedence (Req 16.6).
	//
	// 4. [Idempotency & Journal Deduplication - Req 15.7]:
	//    The resulting Fact.SourceEventKey() and Fact.IdempotencyKey() must be
	//    bit-for-bit identical to what the canonical path would have generated
	//    for the same logical request, guaranteeing journal dedupe parity.
	// =========================================================================

	// Simulate and verify the Task 10 wiring contract programmatically:
	proofSession := largebody.SessionInput{
		AuthoritativeSessionID: "sess-task10-auth",
		ClientSessionID:        "sess-task10-client",
		ALegID:                 "aleg-task10",
	}

	call := &aipapi.Call{
		ID:    "caller-assigned-id-10",
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: proofSession.AuthoritativeSessionID,
			ClientSessionID:        proofSession.ClientSessionID,
			ALegID:                 proofSession.ALegID,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Task 10 contract simulation")}},
		},
	}

	digest, err := streamCallIdentityWithChunkSize(t, call, 16)
	if err != nil {
		t.Fatalf("streamCallIdentity failed: %v", err)
	}

	// 1. Verify RequestID derived via CallID preserves caller ID
	requestID := digest.CallID(call.ID)
	if requestID != call.ID {
		t.Fatalf("Task 10 contract: RequestID must match explicit ID: got %q, want %q", requestID, call.ID)
	}

	// 2. Verify fallback when explicit ID omitted
	fallbackRequestID := digest.CallID("")
	expectedFallback := "call_" + digest.Token()
	if fallbackRequestID != expectedFallback {
		t.Fatalf("Task 10 contract: fallback RequestID must be %q, got %q", expectedFallback, fallbackRequestID)
	}

	// 3. Verify session correlation resolution
	authoritativeSession := proofSession.AuthoritativeSessionID
	if authoritativeSession != call.Session.CorrelationID() {
		t.Fatalf("Task 10 contract: session correlation mismatch: %q vs %q", authoritativeSession, call.Session.CorrelationID())
	}

	// 4. Verify checkpoint identity function compatibility
	feFactID, feSourceID, seq := checkpoint.FrontendIngressIdentity(requestID)
	if feFactID != "fe-ingress:"+call.ID || feSourceID != "fe-ingress:"+call.ID || seq != 1 {
		t.Fatalf("Task 10 contract: frontend ingress identity failed: (%q, %q, %d)", feFactID, feSourceID, seq)
	}

	// 5. Verify that FromSum helpers provide byte-for-byte equivalent inputs
	fromSumCallID := diag.StableCallIDFromSum(call.ID, digest.Sum())
	fromSumToken := diag.StableCallTokenFromSum(digest.Sum())
	if fromSumCallID != requestID {
		t.Fatalf("Task 10 contract: fromSumCallID %q != requestID %q", fromSumCallID, requestID)
	}
	if fromSumToken != digest.Token() {
		t.Fatalf("Task 10 contract: fromSumToken %q != digest.Token %q", fromSumToken, digest.Token())
	}
}
