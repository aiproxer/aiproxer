package checkpoint_test

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCaptureWireBackendIngress_ParityWithCanonical(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715622400, 0).UTC()
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("p-wire-be-test"),
		TenantID:    scope.Known("t-wire-tenant"),
	}
	maxOutput := 2048

	sourcePayload := []byte("wire-attempt-source-payload-data-stream-4096")
	sourceDigest := sha256.Sum256(sourcePayload)
	rewriteDigest := checkpoint.ComputeRewriteDigest(100, 12, `"gpt-4o"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "openai:gpt-4o")

	testCases := []struct {
		name       string
		explicitID string
		maxOutput  *int
		backendID  string
		model      string
	}{
		{
			name:       "explicit caller ID with max output tokens",
			explicitID: "caller-req-be-001",
			maxOutput:  &maxOutput,
			backendID:  "openai_direct",
			model:      "openai:gpt-4o",
		},
		{
			name:       "empty ID falling back to sum token without max output tokens",
			explicitID: "",
			maxOutput:  nil,
			backendID:  "anthropic_direct",
			model:      "claude-3-5-sonnet",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			call := &aipapi.Call{
				ID:    tc.explicitID,
				Route: aipapi.RouteIntent{Selector: tc.model},
				Session: aipapi.SessionRef{
					AuthoritativeSessionID: "auth-sess-wire-be-01",
					ClientSessionID:        "client-hint-wire-be-01",
					ALegID:                 "aleg-wire-be-01",
				},
				Options: aipapi.GenerationOptions{
					MaxOutputTokens: tc.maxOutput,
				},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Wire attempt parity prompt payload")}},
				},
			}

			// 1. Canonical backend-ingress snapshot
			canonCallID := diag.StableCallID(call)
			canonCall := *call
			canonCall.ID = canonCallID

			attemptID := "att-wire-be-001"
			bLegID := "bleg-wire-be-001"

			canonSnap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
				Call:         canonCall,
				Scope:        sc,
				AttemptID:    attemptID,
				BLegID:       bLegID,
				ALegID:       call.Session.ALegID,
				BackendID:    tc.backendID,
				Model:        tc.model,
				CheckpointID: "operator-attempt:" + attemptID,
				StreamID:     "operator-attempt:" + attemptID,
				TraceID:      canonCallID,
				Perspective:  metering.PerspectiveOperator,
				Now:          now,
			})
			if err != nil {
				t.Fatalf("CaptureBackendIngress failed: %v", err)
			}

			// 2. Wire backend-ingress snapshot
			callSum := diag.StableCallSum(call)
			reqID := diag.StableCallIDFromSum(tc.explicitID, callSum)

			wireSnap, err := checkpoint.CaptureWireBackendIngress(checkpoint.WireBackendIngressInput{
				RequestID:       reqID,
				TraceID:         reqID,
				AttemptID:       attemptID,
				BLegID:          bLegID,
				ALegID:          call.Session.ALegID,
				SessionID:       call.Session.CorrelationID(),
				Scope:           sc,
				BackendID:       tc.backendID,
				Model:           tc.model,
				CheckpointID:    "operator-attempt:" + attemptID,
				StreamID:        "operator-attempt:" + attemptID,
				MaxOutputTokens: tc.maxOutput,
				Perspective:     metering.PerspectiveOperator,
				Now:             now,
				SourceDigest:    sourceDigest,
				RewriteDigest:   rewriteDigest,
				AttemptDigest:   attemptDigest,
			})
			if err != nil {
				t.Fatalf("CaptureWireBackendIngress failed: %v", err)
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
			if canonPub.Boundary != wirePub.Boundary || canonPub.Boundary != metering.BoundaryBackendIngress {
				t.Fatalf("Boundary mismatch: canon=%q, wire=%q", canonPub.Boundary, wirePub.Boundary)
			}
			if canonPub.Lifecycle != wirePub.Lifecycle || canonPub.Lifecycle != metering.LifecycleBackendAttempt {
				t.Fatalf("Lifecycle mismatch: canon=%q, wire=%q", canonPub.Lifecycle, wirePub.Lifecycle)
			}
			if canonPub.Perspective != wirePub.Perspective || canonPub.Perspective != metering.PerspectiveOperator {
				t.Fatalf("Perspective mismatch: canon=%q, wire=%q", canonPub.Perspective, wirePub.Perspective)
			}
			if canonPub.BackendID != wirePub.BackendID {
				t.Fatalf("BackendID mismatch: canon=%q, wire=%q", canonPub.BackendID, wirePub.BackendID)
			}
			if canonPub.Model != wirePub.Model {
				t.Fatalf("Model mismatch: canon=%q, wire=%q", canonPub.Model, wirePub.Model)
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
			canonFactID, canonSrcID, canonSeq := checkpoint.BackendIngressIdentity(attemptID)
			canonFact, err := checkpoint.FactFromIngress(checkpoint.IngressFactInput{
				Checkpoint: canonPub,
				FactID:     canonFactID,
				Sequence:   canonSeq,
				SourceID:   canonSrcID,
				Now:        now.Add(time.Millisecond),
			})
			if err != nil {
				t.Fatalf("FactFromIngress canon: %v", err)
			}

			wireFactID, wireSrcID, wireSeq := checkpoint.BackendIngressIdentity(attemptID)
			wireFact, err := checkpoint.FactFromIngress(checkpoint.IngressFactInput{
				Checkpoint: wirePub,
				FactID:     wireFactID,
				Sequence:   wireSeq,
				SourceID:   wireSrcID,
				Now:        now.Add(time.Millisecond),
			})
			if err != nil {
				t.Fatalf("FactFromIngress wire: %v", err)
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

func TestCaptureWireBackendIngress_NoCallRetentionProof(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715622500, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-no-retention-be")}
	maxOutput := 4096

	sourceDigest := sha256.Sum256([]byte("prompt-source-body"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(50, 10, `"gpt-4o"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o")

	snap, err := checkpoint.CaptureWireBackendIngress(checkpoint.WireBackendIngressInput{
		RequestID:       "req-wire-proof-be-1",
		TraceID:         "trace-wire-proof-be-1",
		AttemptID:       "att-wire-proof-be-1",
		BLegID:          "bleg-wire-proof-be-1",
		ALegID:          "aleg-wire-proof-be-1",
		SessionID:       "sess-wire-proof-be-1",
		Scope:           sc,
		BackendID:       "openai_direct",
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
		Now:             now,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
	})
	if err != nil {
		t.Fatalf("CaptureWireBackendIngress failed: %v", err)
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

	// Verify evidence is attached
	ev, ok := snap.WireAttemptEvidence()
	if !ok {
		t.Fatal("expected wire attempt evidence on snapshot")
	}
	if ev.SourceDigest != sourceDigest {
		t.Fatalf("SourceDigest=%x want %x", ev.SourceDigest, sourceDigest)
	}
	if ev.RewriteDigest != rewriteDigest {
		t.Fatalf("RewriteDigest=%x want %x", ev.RewriteDigest, rewriteDigest)
	}
	if ev.AttemptDigest != attemptDigest {
		t.Fatalf("AttemptDigest=%x want %x", ev.AttemptDigest, attemptDigest)
	}
	if ev.Model != "gpt-4o" {
		t.Fatalf("Model=%q want %q", ev.Model, "gpt-4o")
	}
	if ev.MaxOutputTokens == nil || *ev.MaxOutputTokens != maxOutput {
		t.Fatalf("MaxOutputTokens=%v want %d", ev.MaxOutputTokens, maxOutput)
	}
}

func TestRequestHolder_StoreWireBackendIngress_ParityAndParallelAttempts(t *testing.T) {
	t.Parallel()

	holder := &checkpoint.RequestHolder{}
	now := time.Unix(1715622600, 0).UTC()
	sc := scope.PrincipalScopeView{PrincipalID: scope.Known("p-holder-be-parallel")}

	sourceDigest := sha256.Sum256([]byte("parallel-holder-source-payload"))
	max1 := 1024
	max2 := 2048

	att1 := checkpoint.WireBackendIngressInput{
		RequestID:       "req-holder-be-1",
		TraceID:         "trace-holder-be-1",
		AttemptID:       "att-1",
		BLegID:          "bleg-1",
		ALegID:          "aleg-holder-1",
		SessionID:       "sess-holder-1",
		Scope:           sc,
		BackendID:       "openai_primary",
		Model:           "gpt-4o",
		MaxOutputTokens: &max1,
		Now:             now,
		SourceDigest:    sourceDigest,
		RewriteDigest:   checkpoint.ComputeRewriteDigest(10, 8, `"gpt-4o"`),
	}

	att2 := checkpoint.WireBackendIngressInput{
		RequestID:       "req-holder-be-1",
		TraceID:         "trace-holder-be-1",
		AttemptID:       "att-2",
		BLegID:          "bleg-2",
		ALegID:          "aleg-holder-1",
		SessionID:       "sess-holder-1",
		Scope:           sc,
		BackendID:       "anthropic_fallback",
		Model:           "claude-3-5-sonnet",
		MaxOutputTokens: &max2,
		Now:             now.Add(time.Second),
		SourceDigest:    sourceDigest,
		RewriteDigest:   checkpoint.ComputeRewriteDigest(10, 19, `"claude-3-5-sonnet"`),
	}

	// Store attempt 1
	snap1, err := holder.StoreWireBackendIngress(att1)
	if err != nil {
		t.Fatalf("StoreWireBackendIngress att1: %v", err)
	}
	if !snap1.IsWire() {
		t.Fatal("snap1 must be wire")
	}

	// Store attempt 2 (race/failover)
	snap2, err := holder.StoreWireBackendIngress(att2)
	if err != nil {
		t.Fatalf("StoreWireBackendIngress att2: %v", err)
	}
	if !snap2.IsWire() {
		t.Fatal("snap2 must be wire")
	}

	// Retrieve attempt 1
	got1 := holder.BackendIngressFor("att-1")
	if got1 == nil {
		t.Fatal("expected BackendIngressFor(att-1) to be found")
	}
	if got1.Public.BackendID != "openai_primary" || got1.Public.Model != "gpt-4o" {
		t.Fatalf("got1 backend/model mismatch: %+v", got1.Public)
	}
	ev1, ok := got1.WireAttemptEvidence()
	if !ok || ev1.Model != "gpt-4o" || *ev1.MaxOutputTokens != max1 {
		t.Fatalf("ev1 mismatch: %+v", ev1)
	}

	// Retrieve attempt 2
	got2 := holder.BackendIngressFor("att-2")
	if got2 == nil {
		t.Fatal("expected BackendIngressFor(att-2) to be found")
	}
	if got2.Public.BackendID != "anthropic_fallback" || got2.Public.Model != "claude-3-5-sonnet" {
		t.Fatalf("got2 backend/model mismatch: %+v", got2.Public)
	}
	ev2, ok := got2.WireAttemptEvidence()
	if !ok || ev2.Model != "claude-3-5-sonnet" || *ev2.MaxOutputTokens != max2 {
		t.Fatalf("ev2 mismatch: %+v", ev2)
	}

	// Fact binding
	holder.BindBackendIngressFactID("att-1", "be-ingress:att-1")
	holder.BindBackendIngressFactID("att-2", "be-ingress:att-2")
	if holder.BackendIngressFactID("att-1") != "be-ingress:att-1" {
		t.Fatalf("att-1 FactID mismatch: %q", holder.BackendIngressFactID("att-1"))
	}
	if holder.BackendIngressFactID("att-2") != "be-ingress:att-2" {
		t.Fatalf("att-2 FactID mismatch: %q", holder.BackendIngressFactID("att-2"))
	}
}

func TestAssertWireNotWidened_BoundedEvidence(t *testing.T) {
	t.Parallel()

	sourceDigest := sha256.Sum256([]byte("stable-prompt-body-evidence"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(40, 10, `"gpt-4o"`)
	model := "gpt-4o"
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, model)
	maxVal := 2048

	base := checkpoint.WireAttemptEvidence{
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Model:           model,
		MaxOutputTokens: &maxVal,
	}

	t.Run("exact match is not widened", func(t *testing.T) {
		t.Parallel()
		same := base
		if err := checkpoint.AssertWireNotWidened(base, same); err != nil {
			t.Fatalf("expected exact match to pass: %v", err)
		}
	})

	t.Run("lowered max output tokens is narrowing (not widened)", func(t *testing.T) {
		t.Parallel()
		loweredMax := 1024
		narrowed := base
		narrowed.MaxOutputTokens = &loweredMax
		if err := checkpoint.AssertWireNotWidened(base, narrowed); err != nil {
			t.Fatalf("expected lowered max to pass: %v", err)
		}
	})

	t.Run("binding max when authorized had none is narrowing (not widened)", func(t *testing.T) {
		t.Parallel()
		unboundedAuth := base
		unboundedAuth.MaxOutputTokens = nil

		boundCur := base
		boundCur.MaxOutputTokens = &maxVal

		if err := checkpoint.AssertWireNotWidened(unboundedAuth, boundCur); err != nil {
			t.Fatalf("expected binding max on unbounded authorized to pass: %v", err)
		}
	})

	t.Run("both unbounded is not widened", func(t *testing.T) {
		t.Parallel()
		unboundedAuth := base
		unboundedAuth.MaxOutputTokens = nil
		unboundedCur := base
		unboundedCur.MaxOutputTokens = nil

		if err := checkpoint.AssertWireNotWidened(unboundedAuth, unboundedCur); err != nil {
			t.Fatalf("expected both unbounded to pass: %v", err)
		}
	})

	t.Run("raised max output tokens is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		raisedMax := 4096
		widened := base
		widened.MaxOutputTokens = &raisedMax

		err := checkpoint.AssertWireNotWidened(base, widened)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})

	t.Run("removing max output tokens bound is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		unboundedCur := base
		unboundedCur.MaxOutputTokens = nil

		err := checkpoint.AssertWireNotWidened(base, unboundedCur)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})

	t.Run("mutated source digest is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.SourceDigest = sha256.Sum256([]byte("different-source-body"))

		err := checkpoint.AssertWireNotWidened(base, mutated)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})

	t.Run("mutated rewrite digest is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.RewriteDigest = checkpoint.ComputeRewriteDigest(40, 15, `"gpt-4o-mini"`)

		err := checkpoint.AssertWireNotWidened(base, mutated)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})

	t.Run("mutated attempt digest is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.AttemptDigest = sha256.Sum256([]byte("different-attempt-digest"))

		err := checkpoint.AssertWireNotWidened(base, mutated)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})

	t.Run("mutated model is unmeasured widening", func(t *testing.T) {
		t.Parallel()
		mutated := base
		mutated.Model = "gpt-4o-mini"

		err := checkpoint.AssertWireNotWidened(base, mutated)
		if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
			t.Fatalf("expected ErrUnmeasuredWidening, got %v", err)
		}
	})
}
