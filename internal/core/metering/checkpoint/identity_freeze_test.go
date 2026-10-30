package checkpoint_test

// Task 1.7 characterization: freeze metering checkpoint/fact/source identity
// for the future large-payload lane (requirements 15, 16; design sections 10
// read-only, 16).
//
// What this proves with real existing seams only:
//   - FrontendIngressIdentity / BackendIngressIdentity byte-for-byte format
//     ("fe-ingress:"+requestID / "be-ingress:"+attemptID), trimming, and
//     deterministic IngressSequence; FE/BE namespaces never collide.
//   - CaptureFrontendIngress / CaptureBackendIngress correlation precedence:
//     explicit TraceID wins, otherwise Call.ID; SessionID follows
//     SessionRef.CorrelationID (authoritative wins over client hint); ALeg
//     explicit wins over cloned session; StreamID and perspective defaults.
//   - FactFromFrontendIngress / FactFromIngress defaults (kind, authority,
//     source, IdentityVersion V1, SourceID defaults to FactID) so wire-native
//     checkpoints reproduce canonical journal identity.
//   - SourceEventKey retry stability: Sequence, FactID (when SourceID set),
//     RecordedAt, quantities, and money are excluded; StreamID, Boundary,
//     Kind, SourceID, revision, and identity version participate.
//   - IdempotencyKey shape (StreamID+FactID) and lookup-key ordering
//     (canonical first, idempotency last); NUL/colon-bearing values stay
//     field-aligned via length-prefixed CanonicalKey.
//   - Fact identity is request/attempt-derived, not content-derived: huge or
//     escaped content changes the Call clone but never the FactID/SourceID.
//
// Reused, not duplicated here:
//   - diag stable sums/tokens/timestamps — internal/core/diag/stable_identity_freeze_test.go.
//   - Billing bc_ ownership — internal/core/billing/call_id_test.go.
//   - Journal append/replay/store behavior — existing checkpoint fact/capture
//     tests and metering store suites.
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - No wire-native checkpoint constructor is added here; this freezes only
//     the current identity contract a wire path must reproduce.
//   - No production diff.

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func freezeScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{PrincipalID: scope.Known("p-freeze")}
}

func freezeCall(id string) aipapi.Call {
	return aipapi.Call{
		ID: id,
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze hello")},
		}},
	}
}

func freezeFrontendFact(t *testing.T, requestID string) metering.Fact {
	t.Helper()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         freezeCall(requestID),
		Scope:        freezeScope(),
		CheckpointID: "cp-freeze-fe",
		StreamID:     "customer-request:" + requestID,
		Now:          time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	factID, sourceID, seq := checkpoint.FrontendIngressIdentity(requestID)
	fact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
		Checkpoint: snap.Public,
		FactID:     factID,
		Sequence:   seq,
		SourceID:   sourceID,
		Now:        time.Unix(11, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return fact
}

func TestMeteringIdentityFreeze_IngressIdentityFormat(t *testing.T) {
	t.Parallel()
	factID, sourceID, seq := checkpoint.FrontendIngressIdentity("req-1")
	if factID != "fe-ingress:req-1" || sourceID != "fe-ingress:req-1" {
		t.Fatalf("FE identity=%q/%q", factID, sourceID)
	}
	if checkpoint.IngressSequence != 1 {
		t.Fatalf("IngressSequence=%d want 1", checkpoint.IngressSequence)
	}
	if seq != checkpoint.IngressSequence {
		t.Fatalf("FE seq=%d want IngressSequence 1", seq)
	}
	trimmed, trimmedSrc, trimmedSeq := checkpoint.FrontendIngressIdentity("  req-1  ")
	if trimmed != factID || trimmedSrc != sourceID || trimmedSeq != seq {
		t.Fatal("FE identity must trim request ID")
	}

	beFactID, beSourceID, beSeq := checkpoint.BackendIngressIdentity("att-1")
	if beFactID != "be-ingress:att-1" || beSourceID != "be-ingress:att-1" {
		t.Fatalf("BE identity=%q/%q", beFactID, beSourceID)
	}
	if beSeq != checkpoint.IngressSequence {
		t.Fatalf("BE seq=%d", beSeq)
	}
	if factID == beFactID {
		t.Fatal("FE and BE identity namespaces must not collide")
	}
	again, againSrc, againSeq := checkpoint.FrontendIngressIdentity("req-1")
	if again != factID || againSrc != sourceID || againSeq != seq {
		t.Fatal("ingress identity must be restart-stable (independent of retry count)")
	}
}

func TestMeteringIdentityFreeze_FrontendCorrelationPrecedence(t *testing.T) {
	t.Parallel()
	call := freezeCall("req-corr")
	call.Session = aipapi.SessionRef{
		ClientSessionID:        "client-1",
		AuthoritativeSessionID: "sess-auth-1",
		ALegID:                 "a-1",
	}
	explicit, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: call, Scope: freezeScope(), CheckpointID: "cp-fe-explicit",
		StreamID: "customer-request:req-corr", TraceID: "t_explicit", Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Public.Correlation.TraceID != "t_explicit" {
		t.Fatalf("explicit TraceID must win: %+v", explicit.Public.Correlation)
	}
	if explicit.Public.Correlation.RequestID != "req-corr" || explicit.Public.Correlation.ALegID != "a-1" {
		t.Fatalf("request/a-leg correlation=%+v", explicit.Public.Correlation)
	}
	if explicit.Public.Correlation.SessionID != "sess-auth-1" {
		t.Fatalf("session must follow authoritative CorrelationID: %+v", explicit.Public.Correlation)
	}

	fallback, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: call, Scope: freezeScope(), CheckpointID: "cp-fe-fallback",
		StreamID: "customer-request:req-corr", Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Public.Correlation.TraceID != "req-corr" {
		t.Fatalf("empty TraceID must default to Call.ID: %+v", fallback.Public.Correlation)
	}

	defStream, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: freezeCall("req-default"), Scope: freezeScope(), CheckpointID: "cp-fe-def",
		Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if defStream.Public.StreamID != "customer-request:req-default" {
		t.Fatalf("stream default=%q", defStream.Public.StreamID)
	}
	if defStream.Public.Boundary != metering.BoundaryFrontendIngress ||
		defStream.Public.Lifecycle != metering.LifecycleLogicalRequest ||
		defStream.Public.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("FE checkpoint site=%v/%v/%v", defStream.Public.Boundary, defStream.Public.Lifecycle, defStream.Public.Perspective)
	}
}

func TestMeteringIdentityFreeze_BackendCorrelationPrecedence(t *testing.T) {
	t.Parallel()
	call := freezeCall("req-be")
	call.Session = aipapi.SessionRef{ALegID: "a-clone", AuthoritativeSessionID: "sess-auth-be"}
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: call, Scope: freezeScope(), AttemptID: "att-1", BLegID: "b-1",
		ALegID: "a-explicit", BackendID: "be-1", Model: "m-1",
		CheckpointID: "cp-be-1", StreamID: "operator-attempt:att-1",
		TraceID: "t_be", Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	corr := snap.Public.Correlation
	if corr.ALegID != "a-explicit" || corr.TraceID != "t_be" {
		t.Fatalf("explicit A-leg/trace must win: %+v", corr)
	}
	if corr.RequestID != "req-be" || corr.BLegID != "b-1" || corr.AttemptID != "att-1" {
		t.Fatalf("attempt correlation=%+v", corr)
	}
	if corr.SessionID != "sess-auth-be" {
		t.Fatalf("session=%q", corr.SessionID)
	}

	defSnap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: freezeCall("req-be-def"), Scope: freezeScope(), AttemptID: "att-9",
		BLegID: "b-9", CheckpointID: "cp-be-def", Now: time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if defSnap.Public.StreamID != "operator-attempt:att-9" {
		t.Fatalf("BE stream default=%q", defSnap.Public.StreamID)
	}
	if defSnap.Public.Correlation.TraceID != "req-be-def" {
		t.Fatalf("BE trace default=%+v", defSnap.Public.Correlation)
	}
	if defSnap.Public.Perspective != metering.PerspectiveOperator ||
		defSnap.Public.Boundary != metering.BoundaryBackendIngress ||
		defSnap.Public.Lifecycle != metering.LifecycleBackendAttempt {
		t.Fatalf("BE site=%v/%v/%v", defSnap.Public.Boundary, defSnap.Public.Lifecycle, defSnap.Public.Perspective)
	}
}

func TestMeteringIdentityFreeze_FrontendFactDefaults(t *testing.T) {
	t.Parallel()
	fact := freezeFrontendFact(t, "req-fact-def")
	if fact.Kind != metering.FactKindReservationEstimate {
		t.Fatalf("kind=%q want reservation_estimate", fact.Kind)
	}
	if fact.Authority != metering.AuthorityEstimated || fact.Source != metering.SourceObserved {
		t.Fatalf("authority/source=%q/%q", fact.Authority, fact.Source)
	}
	if fact.EffectiveIdentityVersion() != metering.IdentityVersionV1 {
		t.Fatalf("identity version=%d want V1", fact.EffectiveIdentityVersion())
	}
	if fact.SourceID != "fe-ingress:req-fact-def" || fact.FactID != "fe-ingress:req-fact-def" {
		t.Fatalf("fact/source=%q/%q", fact.FactID, fact.SourceID)
	}
	if fact.Sequence != checkpoint.IngressSequence {
		t.Fatalf("seq=%d", fact.Sequence)
	}
	if err := fact.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMeteringIdentityFreeze_SourceEventKeyRetryStability(t *testing.T) {
	t.Parallel()
	base := freezeFrontendFact(t, "req-retry")
	wantKey := base.SourceEventKey()
	if wantKey == "" {
		t.Fatal("SourceEventKey empty")
	}

	retry := base
	retry.Sequence = 99
	retry.FactID = "fe-ingress:req-retry-other"
	retry.RecordedAt = time.Unix(9999, 0).UTC()
	retry.Quantities = checkpoint.QuantitiesFromTokenCounts(10, 5, 0, 0, 0, 15, true)
	retry.Money = &metering.MoneyObservation{NanoUnits: 7, Currency: "USD", Present: true, Source: metering.SourceObserved}
	if got := retry.SourceEventKey(); got != wantKey {
		t.Fatalf("retry/restart must keep SourceEventKey stable: %q vs %q", got, wantKey)
	}

	for name, mutate := range map[string]func(*metering.Fact){
		"source":   func(f *metering.Fact) { f.SourceID = "fe-ingress:other" },
		"stream":   func(f *metering.Fact) { f.StreamID = "customer-request:other" },
		"kind":     func(f *metering.Fact) { f.Kind = metering.FactKindDelta },
		"revision": func(f *metering.Fact) { f.SourceRevision = 3 },
		"version":  func(f *metering.Fact) { f.IdentityVersion = 2 },
	} {
		other := base
		mutate(&other)
		if other.SourceEventKey() == wantKey {
			t.Fatalf("%s must change SourceEventKey", name)
		}
	}

	zero := base
	zero.IdentityVersion = 0
	explicitV1 := base
	explicitV1.IdentityVersion = metering.IdentityVersionV1
	if zero.SourceEventKey() != explicitV1.SourceEventKey() {
		t.Fatal("IdentityVersion 0 must default to V1 key")
	}
}

func TestMeteringIdentityFreeze_IdempotencyAndLookupKeys(t *testing.T) {
	t.Parallel()
	fact := freezeFrontendFact(t, "req-keys")
	if got, want := fact.IdempotencyKey(), "customer-request:req-keys\x00fe-ingress:req-keys"; got != want {
		t.Fatalf("IdempotencyKey=%q want %q", got, want)
	}
	keys := fact.SourceEventLookupKeys()
	if len(keys) == 0 {
		t.Fatal("lookup keys empty")
	}
	if keys[0] != fact.SourceEventKey() {
		t.Fatal("canonical SourceEventKey must be first")
	}
	if keys[len(keys)-1] != fact.IdempotencyKey() {
		t.Fatal("IdempotencyKey must be last")
	}

	nasty := freezeFrontendFact(t, "req:nul\x00colon")
	nastyKey := nasty.SourceEventKey()
	if nastyKey == "" || nastyKey == fact.SourceEventKey() {
		t.Fatal("NUL/colon-bearing IDs must produce a distinct non-empty key")
	}
	again := freezeFrontendFact(t, "req:nul\x00colon")
	if again.SourceEventKey() != nastyKey {
		t.Fatal("delimiter-bearing key must be stable")
	}
	if !strings.Contains(nasty.IdempotencyKey(), "req:nul\x00colon") {
		t.Fatalf("idempotency preserves raw id: %q", nasty.IdempotencyKey())
	}
}

func TestMeteringIdentityFreeze_ContentDoesNotShiftFactIdentity(t *testing.T) {
	t.Parallel()
	small := freezeCall("req-content-stable")
	hugeCall := freezeCall("req-content-stable")
	hugeCall.Messages[0].Parts[0] = aipapi.TextPart(strings.Repeat("b", 1<<20))
	escapedCall := freezeCall("req-content-stable")
	escapedCall.Messages[0].Parts[0] = aipapi.TextPart("<>&\"'\u2028🧪")

	for name, call := range map[string]aipapi.Call{"small": small, "huge": hugeCall, "escaped": escapedCall} {
		snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
			Call: call, Scope: freezeScope(), CheckpointID: "cp-" + name,
			StreamID: "customer-request:req-content-stable", Now: time.Unix(10, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		factID, sourceID, seq := checkpoint.FrontendIngressIdentity("req-content-stable")
		fact, err := checkpoint.FactFromFrontendIngress(checkpoint.IngressFactInput{
			Checkpoint: snap.Public, FactID: factID, Sequence: seq, SourceID: sourceID,
			Now: time.Unix(11, 0).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if fact.FactID != "fe-ingress:req-content-stable" || fact.SourceID != "fe-ingress:req-content-stable" {
			t.Fatalf("%s: fact identity must stay request-derived: %q/%q", name, fact.FactID, fact.SourceID)
		}
		if fact.SourceEventKey() != freezeFrontendFact(t, "req-content-stable").SourceEventKey() {
			t.Fatalf("%s: content must not shift SourceEventKey", name)
		}
	}
}
