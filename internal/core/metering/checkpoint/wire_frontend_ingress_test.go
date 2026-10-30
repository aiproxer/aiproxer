package checkpoint_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCaptureWireFrontendIngress_ParityWithCanonical(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715620800, 0).UTC()
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("p-wire-fe-test"),
		TenantID:    scope.Known("t-wire-tenant"),
	}
	maxOutput := 2048

	testCases := []struct {
		name       string
		explicitID string
		maxOutput  *int
		frontendID string
	}{
		{
			name:       "explicit caller ID with max output tokens",
			explicitID: "caller-req-fe-001",
			maxOutput:  &maxOutput,
			frontendID: "openai_responses",
		},
		{
			name:       "empty ID falling back to sum token without max output tokens",
			explicitID: "",
			maxOutput:  nil,
			frontendID: "openresponses",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			call := &aipapi.Call{
				ID:    tc.explicitID,
				Route: aipapi.RouteIntent{Selector: "openai:gpt-4o"},
				Session: aipapi.SessionRef{
					AuthoritativeSessionID: "auth-sess-wire-01",
					ClientSessionID:        "client-hint-wire-01",
					ALegID:                 "aleg-wire-01",
				},
				Options: aipapi.GenerationOptions{
					MaxOutputTokens: tc.maxOutput,
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Parity test prompt payload")}},
				},
			}

			// 1. Canonical snapshot (in canonical pipeline, Call.ID is set to StableCallID)
			canonCallID := diag.StableCallID(call)
			canonCall := *call
			canonCall.ID = canonCallID

			canonSnap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
				Call:         canonCall,
				Scope:        sc,
				FrontendID:   tc.frontendID,
				CheckpointID: "customer-request:" + canonCallID,
				StreamID:     "customer-request:" + canonCallID,
				TraceID:      canonCallID,
				Now:          now,
			})
			if err != nil {
				t.Fatalf("CaptureFrontendIngress failed: %v", err)
			}

			// 2. Wire snapshot
			callSum := diag.StableCallSum(call)
			reqID := diag.StableCallIDFromSum(tc.explicitID, callSum)

			wireSnap, err := checkpoint.CaptureWireFrontendIngress(checkpoint.WireFrontendIngressInput{
				RequestID:       reqID,
				TraceID:         reqID,
				CheckpointID:    "customer-request:" + reqID,
				StreamID:        "customer-request:" + reqID,
				Scope:           sc,
				FrontendID:      tc.frontendID,
				ALegID:          call.Session.ALegID,
				SessionID:       call.Session.CorrelationID(),
				MaxOutputTokens: tc.maxOutput,
				Now:             now,
			})
			if err != nil {
				t.Fatalf("CaptureWireFrontendIngress failed: %v", err)
			}

			// Public checkpoint field-by-field parity
			canonPub := canonSnap.Public
			wirePub := wireSnap.Public

			if canonPub.CheckpointID != wirePub.CheckpointID {
				t.Fatalf("CheckpointID mismatch: canon=%q, wire=%q", canonPub.CheckpointID, wirePub.CheckpointID)
			}
			if canonPub.StreamID != wirePub.StreamID {
				t.Fatalf("StreamID mismatch: canon=%q, wire=%q", canonPub.StreamID, wirePub.StreamID)
			}
			if canonPub.Boundary != wirePub.Boundary || canonPub.Boundary != metering.BoundaryFrontendIngress {
				t.Fatalf("Boundary mismatch: canon=%q, wire=%q", canonPub.Boundary, wirePub.Boundary)
			}
			if canonPub.Lifecycle != wirePub.Lifecycle || canonPub.Lifecycle != metering.LifecycleLogicalRequest {
				t.Fatalf("Lifecycle mismatch: canon=%q, wire=%q", canonPub.Lifecycle, wirePub.Lifecycle)
			}
			if canonPub.Perspective != wirePub.Perspective || canonPub.Perspective != metering.PerspectiveCustomer {
				t.Fatalf("Perspective mismatch: canon=%q, wire=%q", canonPub.Perspective, wirePub.Perspective)
			}
			if canonPub.FrontendID != wirePub.FrontendID {
				t.Fatalf("FrontendID mismatch: canon=%q, wire=%q", canonPub.FrontendID, wirePub.FrontendID)
			}
			if !canonPub.CapturedAt.Equal(wirePub.CapturedAt) {
				t.Fatalf("CapturedAt mismatch: canon=%v, wire=%v", canonPub.CapturedAt, wirePub.CapturedAt)
			}
			if canonPub.Source != wirePub.Source || canonPub.Authority != wirePub.Authority {
				t.Fatalf("Source/Authority mismatch: canon=(%v,%v), wire=(%v,%v)",
					canonPub.Source, canonPub.Authority, wirePub.Source, wirePub.Authority)
			}
			if canonPub.Presence != wirePub.Presence {
				t.Fatalf("Presence mismatch: canon=%v, wire=%v", canonPub.Presence, wirePub.Presence)
			}

			// Correlation parity
			if canonPub.Correlation != wirePub.Correlation {
				t.Fatalf("Correlation mismatch:\ncanon: %+v\nwire:  %+v", canonPub.Correlation, wirePub.Correlation)
			}

			// Scope parity
			if !reflect.DeepEqual(canonPub.Scope, wirePub.Scope) {
				t.Fatalf("Scope mismatch:\ncanon: %+v\nwire:  %+v", canonPub.Scope, wirePub.Scope)
			}

			// Quantities parity
			if len(canonPub.Quantities) != len(wirePub.Quantities) {
				t.Fatalf("Quantities count mismatch: canon=%d, wire=%d", len(canonPub.Quantities), len(wirePub.Quantities))
			}
			for i := range canonPub.Quantities {
				if canonPub.Quantities[i] != wirePub.Quantities[i] {
					t.Fatalf("Quantity [%d] mismatch:\ncanon: %+v\nwire:  %+v",
						i, canonPub.Quantities[i], wirePub.Quantities[i])
				}
			}

			// Fact derivation parity
			canonFactID, canonSrcID, canonSeq := checkpoint.FrontendIngressIdentity(canonPub.Correlation.RequestID)
			canonFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
				Checkpoint: canonPub,
				FactID:     canonFactID,
				Sequence:   canonSeq,
				SourceID:   canonSrcID,
				Now:        now.Add(time.Millisecond),
			})
			if err != nil {
				t.Fatalf("FactFromFrontendIngress canon: %v", err)
			}

			wireFactID, wireSrcID, wireSeq := checkpoint.FrontendIngressIdentity(wirePub.Correlation.RequestID)
			wireFact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
				Checkpoint: wirePub,
				FactID:     wireFactID,
				Sequence:   wireSeq,
				SourceID:   wireSrcID,
				Now:        now.Add(time.Millisecond),
			})
			if err != nil {
				t.Fatalf("FactFromFrontendIngress wire: %v", err)
			}

			if canonFact.FactID != wireFact.FactID || canonFact.SourceID != wireFact.SourceID || canonFact.Sequence != wireFact.Sequence {
				t.Fatalf("Fact identity mismatch: canon=(%q,%q,%d), wire=(%q,%q,%d)",
					canonFact.FactID, canonFact.SourceID, canonFact.Sequence,
					wireFact.FactID, wireFact.SourceID, wireFact.Sequence)
			}
			if canonFact.SourceEventKey() != wireFact.SourceEventKey() {
				t.Fatalf("SourceEventKey mismatch: canon=%q, wire=%q", canonFact.SourceEventKey(), wireFact.SourceEventKey())
			}
			if canonFact.IdempotencyKey() != wireFact.IdempotencyKey() {
				t.Fatalf("IdempotencyKey mismatch: canon=%q, wire=%q", canonFact.IdempotencyKey(), wireFact.IdempotencyKey())
			}
		})
	}
}

func TestCaptureWireFrontendIngress_NoCallRetentionProof(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715621000, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-no-retention")}
	maxOutput := 4096

	snap, err := checkpoint.CaptureWireFrontendIngress(checkpoint.WireFrontendIngressInput{
		RequestID:       "req-wire-proof-1",
		TraceID:         "trace-wire-proof-1",
		CheckpointID:    "cp-wire-proof-1",
		StreamID:        "stream-wire-proof-1",
		Scope:           sc,
		FrontendID:      "openai_responses",
		ALegID:          "aleg-proof-1",
		SessionID:       "sess-proof-1",
		MaxOutputTokens: &maxOutput,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("CaptureWireFrontendIngress failed: %v", err)
	}

	// Requirement 15.3 & 19: The wire checkpoint MUST NOT retain a hidden full canonical Call.
	if !snap.IsWire() {
		t.Fatal("expected snap.IsWire() to be true")
	}
	if snap.Call.ID != "" {
		t.Fatalf("wire snapshot must have empty Call.ID, got %q", snap.Call.ID)
	}
	if len(snap.Call.Messages) != 0 {
		t.Fatalf("wire snapshot must have nil/empty Call.Messages, got len %d", len(snap.Call.Messages))
	}
	if len(snap.Call.Items) != 0 {
		t.Fatalf("wire snapshot must have nil/empty Call.Items, got len %d", len(snap.Call.Items))
	}
	if len(snap.Call.Tools) != 0 {
		t.Fatalf("wire snapshot must have nil/empty Call.Tools, got len %d", len(snap.Call.Tools))
	}
	if !reflect.DeepEqual(snap.Call.Session, aipapi.SessionRef{}) {
		t.Fatalf("wire snapshot must have empty Call.Session, got %+v", snap.Call.Session)
	}
	if !reflect.DeepEqual(snap.Call.Route, aipapi.RouteIntent{}) {
		t.Fatalf("wire snapshot must have empty Call.Route, got %+v", snap.Call.Route)
	}
	if !reflect.DeepEqual(snap.Call.Options, aipapi.GenerationOptions{}) {
		t.Fatalf("wire snapshot must have empty Call.Options, got %+v", snap.Call.Options)
	}
	// Verify that the whole Call struct in Snapshot is the exact zero value.
	if !reflect.DeepEqual(snap.Call, aipapi.Call{}) {
		t.Fatalf("wire snapshot Call must be exact zero value, got %+v", snap.Call)
	}
}

func TestRequestHolder_CaptureOrReuseWireFrontendIngress_ParityAndReuse(t *testing.T) {
	t.Parallel()

	holder := &checkpoint.RequestHolder{}
	now := time.Unix(1715621200, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-holder-reuse")}
	maxOutput := 512

	in := checkpoint.WireFrontendIngressInput{
		RequestID:       "req-holder-wire-1",
		TraceID:         "trace-holder-wire-1",
		Scope:           sc,
		FrontendID:      "openai_responses",
		ALegID:          "aleg-holder-1",
		SessionID:       "sess-holder-1",
		MaxOutputTokens: &maxOutput,
		Now:             now,
	}

	first, err := holder.CaptureOrReuseWireFrontendIngress(in)
	if err != nil {
		t.Fatalf("first CaptureOrReuseWireFrontendIngress: %v", err)
	}

	if holder.FrontendIngress == nil {
		t.Fatal("holder.FrontendIngress must be populated")
	}
	if !holder.FrontendIngress.IsWire() {
		t.Fatal("holder.FrontendIngress must be wire")
	}

	// Second call with mutated input must return the cached first snapshot (reuse semantics).
	mutatedIn := in
	mutatedIn.FrontendID = "mutated-frontend"
	mutatedIn.ALegID = "mutated-aleg"

	second, err := holder.CaptureOrReuseWireFrontendIngress(mutatedIn)
	if err != nil {
		t.Fatalf("second CaptureOrReuseWireFrontendIngress: %v", err)
	}

	if second.Public.FrontendID != "openai_responses" {
		t.Fatalf("second call should reuse first snapshot frontend: got %q, want %q",
			second.Public.FrontendID, "openai_responses")
	}
	if second.Public.Correlation.ALegID != "aleg-holder-1" {
		t.Fatalf("second call should reuse first snapshot aleg: got %q, want %q",
			second.Public.Correlation.ALegID, "aleg-holder-1")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first and second snapshots must be identical:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestQuantitiesFromCountAndMaxOutput_SharedHelper(t *testing.T) {
	t.Parallel()

	maxVal := 1024
	testCases := []struct {
		name      string
		maxOutput *int
	}{
		{name: "with max output", maxOutput: &maxVal},
		{name: "without max output", maxOutput: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				Options: aipapi.GenerationOptions{
					MaxOutputTokens: tc.maxOutput,
				},
			}
			fromCall := checkpoint.QuantitiesFromCall(call)
			fromHelper := checkpoint.QuantitiesFromCountAndMaxOutput(tc.maxOutput)

			if !reflect.DeepEqual(fromCall, fromHelper) {
				t.Fatalf("QuantitiesFromCall and QuantitiesFromCountAndMaxOutput diverged:\nfromCall:   %+v\nfromHelper: %+v",
					fromCall, fromHelper)
			}
		})
	}
}
