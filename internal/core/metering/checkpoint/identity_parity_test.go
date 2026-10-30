package checkpoint_test

// =============================================================================
// Task 6.4: Checkpoint Identity Parity & Task 10 Wiring Contract
//
// Spec Requirements:
// - Requirement 15: Metering, Accounting, and Billing Need Wire-Native Evidence.
//   15.1: Wire path shall not call current Call-cloning frontend/backend metering
//         checkpoint helpers merely to satisfy existing APIs.
//   15.2: Add wire-native frontend/backend ingress checkpoint capture from exact
//         bounded facts: request/economic identity, scope, frontend/backend/
//         attempt/A-leg/session correlations, model, request count, and exact
//         max-output quantity.
//   15.3: Wire checkpoint storage shall not retain a hidden full canonical Call.
//         Widening/retry integrity shall instead use immutable source/proof
//         digests plus bounded rewrite/attempt evidence.
//   15.7: Economic/metering facts, reservations, settlement, terminal usage, and
//         retry idempotency occur exactly once and preserve current failure policy.
// - Requirement 16: Deterministic Request and Economic Identity Parity.
//   16.1: Inventory of stable Call IDs, tokens, timestamps, checkpoint IDs, response IDs.
//   16.4: Helpers may derive current stable Call ID/token/timestamp from that digest so
//         wire trace/economic identity and deterministic frontend fields remain path-stable.
//   16.6: Caller/provider-supplied explicit IDs retain current precedence.
//
// Design Sections:
// - Section 10: Wire-Native Checkpoint and Bounded Evidence.
// - Section 11: Post-Commit Runtime Facts: No Shadow Call.
// =============================================================================

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// TestCheckpointIdentityParity_FromSumSeam verifies that deriving request ID
// from canonical Call vs an already-computed canonical sum produces bit-for-bit
// identical checkpoint and fact identities across all caller ID permutations.
func TestCheckpointIdentityParity_FromSumSeam(t *testing.T) {
	t.Parallel()

	baseCall := func(explicitID string) *aipapi.Call {
		return &aipapi.Call{
			ID:    explicitID,
			Route: aipapi.RouteIntent{Selector: "stub:model-checkpoint-parity"},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Checkpoint parity test payload")}},
			},
		}
	}

	testCases := []struct {
		name       string
		explicitID string
	}{
		{name: "explicit ID provided", explicitID: "caller-fe-req-123"},
		{name: "untrimmed explicit ID", explicitID: "   caller-fe-trimmed-456   "},
		{name: "empty ID defaults to sum token", explicitID: ""},
		{name: "whitespace-only ID defaults to sum token", explicitID: "     "},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := baseCall(tc.explicitID)

			// 1. Canonical path derives ID directly from Call
			canonReqID := diag.StableCallID(call)
			canonFactID, canonSourceID, canonSeq := checkpoint.FrontendIngressIdentity(canonReqID)

			// 2. Wire path derives ID from already-computed sum + caller ID
			callSum := diag.StableCallSum(call)
			wireReqID := diag.StableCallIDFromSum(tc.explicitID, callSum)
			wireFactID, wireSourceID, wireSeq := checkpoint.FrontendIngressIdentity(wireReqID)

			// Assert exact request ID parity
			if canonReqID != wireReqID {
				t.Fatalf("request ID parity failed: canon=%q, wire=%q", canonReqID, wireReqID)
			}

			// Assert exact checkpoint identity parity
			if canonFactID != wireFactID {
				t.Fatalf("FactID parity failed: canon=%q, wire=%q", canonFactID, wireFactID)
			}
			if canonSourceID != wireSourceID {
				t.Fatalf("SourceID parity failed: canon=%q, wire=%q", canonSourceID, wireSourceID)
			}
			if canonSeq != wireSeq || canonSeq != checkpoint.IngressSequence {
				t.Fatalf("Sequence parity failed: canon=%d, wire=%d, want %d",
					canonSeq, wireSeq, checkpoint.IngressSequence)
			}
		})
	}
}

// TestCheckpointIdentityParity_FactParityAcrossSeams verifies that constructing
// metering Facts from canonical Snapshot vs bounded wire facts produces identical
// SourceEventKey and IdempotencyKey values (Req 15.7, 16.1).
func TestCheckpointIdentityParity_FactParityAcrossSeams(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715620500, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-checkpoint-fact-parity")}

	call := &aipapi.Call{
		ID:    "req-fe-parity-100",
		Route: aipapi.RouteIntent{Selector: "stub:model"},
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "auth-sess-999",
			ClientSessionID:        "client-hint-888",
			ALegID:                 "aleg-fe-1",
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Fact parity test payload")}},
		},
	}

	// 1. Canonical snapshot via CaptureFrontendIngress
	canonSnap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         *call,
		Scope:        sc,
		CheckpointID: "cp-canon-fe",
		StreamID:     "customer-request:req-fe-parity-100",
		TraceID:      "trace-fe-100",
		Now:          now,
	})
	if err != nil {
		t.Fatalf("CaptureFrontendIngress: %v", err)
	}

	canonFactID, canonSrcID, canonSeq := checkpoint.FrontendIngressIdentity(call.ID)
	canonFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint: canonSnap.Public,
		FactID:     canonFactID,
		Sequence:   canonSeq,
		SourceID:   canonSrcID,
		Now:        now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FactFromFrontendIngress (canonical): %v", err)
	}

	// 2. Wire representation: constructed strictly from bounded facts
	callSum := diag.StableCallSum(call)
	wireReqID := diag.StableCallIDFromSum(call.ID, callSum)

	wireCheckpoint := metering.Checkpoint{
		CheckpointID: "cp-wire-fe",
		StreamID:     "customer-request:" + wireReqID,
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  metering.PerspectiveCustomer,
		Correlation: metering.Correlation{
			RequestID: wireReqID,
			ALegID:    call.Session.ALegID,
			SessionID: call.Session.CorrelationID(),
			TraceID:   "trace-fe-100",
		},
		Scope:      sc.Clone(),
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityEstimated,
		Presence:   metering.PresenceUnknown,
		CapturedAt: now,
	}
	if err := wireCheckpoint.Validate(); err != nil {
		t.Fatalf("wireCheckpoint.Validate: %v", err)
	}

	wireFactID, wireSrcID, wireSeq := checkpoint.FrontendIngressIdentity(wireReqID)
	wireFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint: wireCheckpoint,
		FactID:     wireFactID,
		Sequence:   wireSeq,
		SourceID:   wireSrcID,
		Now:        now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("FactFromFrontendIngress (wire): %v", err)
	}

	// Assert Fact identities and keys match exactly
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

	// Journal deduplication keys must match exactly
	if canonFact.SourceEventKey() != wireFact.SourceEventKey() {
		t.Fatalf("SourceEventKey mismatch: %q vs %q", canonFact.SourceEventKey(), wireFact.SourceEventKey())
	}
	if canonFact.IdempotencyKey() != wireFact.IdempotencyKey() {
		t.Fatalf("IdempotencyKey mismatch: %q vs %q", canonFact.IdempotencyKey(), wireFact.IdempotencyKey())
	}
}

// TestCheckpointIdentityParity_Task10WiringObligation documents and enforces
// the contract for Task 10's wire-native checkpoint constructor.
func TestCheckpointIdentityParity_Task10WiringObligation(t *testing.T) {
	t.Parallel()

	// Task 10 Scope Contract:
	// When Task 10 introduces CaptureWireFrontendIngress and CaptureWireBackendIngress:
	// 1. It MUST NOT accept or store a aipapi.Call.
	// 2. It MUST construct metering.Checkpoint strictly from bounded facts:
	//    - RequestID: derived from explicit ID or diag.StableCallIDFromSum
	//    - TraceID: caller/context trace ID, falling back to RequestID
	//    - StreamID: "customer-request:" + RequestID (or "operator-attempt:" + AttemptID)
	//    - SessionID: AuthoritativeSessionID if present, else ClientSessionID
	//    - ALegID: explicit ALegID
	// 3. It MUST use FrontendIngressIdentity / BackendIngressIdentity.
	// 4. IngressSequence MUST remain 1.
	//
	// This test asserts these invariants against the public checkpoint contracts.

	now := time.Unix(1715620900, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-task10-contract")}

	explicitCallerID := "caller-task10-explicit"
	rawSum := diag.StableCallSum(&aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:model"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Task 10 wiring payload")}},
		},
	})

	// Derive wire request ID
	requestID := diag.StableCallIDFromSum(explicitCallerID, rawSum)
	if requestID != explicitCallerID {
		t.Fatalf("explicit caller ID must win in Task 10 contract: got %q, want %q", requestID, explicitCallerID)
	}

	// Derive checkpoint identities
	factID, sourceID, seq := checkpoint.FrontendIngressIdentity(requestID)
	if factID != "fe-ingress:"+explicitCallerID || sourceID != "fe-ingress:"+explicitCallerID {
		t.Fatalf("Task 10 frontend identity mismatch: %q / %q", factID, sourceID)
	}
	if seq != checkpoint.IngressSequence {
		t.Fatalf("Task 10 sequence must be IngressSequence (1), got %d", seq)
	}

	// Verify fallback when explicit caller ID is omitted
	fallbackReqID := diag.StableCallIDFromSum("", rawSum)
	expectedToken := diag.StableCallTokenFromSum(rawSum)
	if fallbackReqID != "call_"+expectedToken {
		t.Fatalf("Task 10 fallback request ID mismatch: got %q, want %q", fallbackReqID, "call_"+expectedToken)
	}

	fallbackFactID, fallbackSourceID, fallbackSeq := checkpoint.FrontendIngressIdentity(fallbackReqID)
	if fallbackFactID != "fe-ingress:call_"+expectedToken || fallbackSourceID != "fe-ingress:call_"+expectedToken {
		t.Fatalf("Task 10 fallback frontend identity mismatch: %q / %q", fallbackFactID, fallbackSourceID)
	}
	if fallbackSeq != checkpoint.IngressSequence {
		t.Fatalf("Task 10 fallback sequence must be IngressSequence (1), got %d", fallbackSeq)
	}

	// Backend attempt identity contract
	attemptID := "attempt-task10-001"
	beFactID, beSourceID, beSeq := checkpoint.BackendIngressIdentity(attemptID)
	if beFactID != "be-ingress:"+attemptID || beSourceID != "be-ingress:"+attemptID {
		t.Fatalf("Task 10 backend identity mismatch: %q / %q", beFactID, beSourceID)
	}
	if beSeq != checkpoint.IngressSequence {
		t.Fatalf("Task 10 backend sequence must be IngressSequence (1), got %d", beSeq)
	}

	// Correlation construction contract without Call struct
	corr := metering.Correlation{
		RequestID: requestID,
		ALegID:    "aleg-task10",
		SessionID: "sess-task10-auth",
		TraceID:   requestID,
	}

	wireCheckpoint := metering.Checkpoint{
		CheckpointID: "cp-task10-wire",
		StreamID:     "customer-request:" + requestID,
		Boundary:     metering.BoundaryFrontendIngress,
		Lifecycle:    metering.LifecycleLogicalRequest,
		Perspective:  metering.PerspectiveCustomer,
		Correlation:  corr,
		Scope:        sc.Clone(),
		Source:       metering.SourceObserved,
		Authority:    metering.AuthorityEstimated,
		Presence:     metering.PresenceUnknown,
		CapturedAt:   now,
	}
	if err := wireCheckpoint.Validate(); err != nil {
		t.Fatalf("wireCheckpoint.Validate: %v", err)
	}
}
