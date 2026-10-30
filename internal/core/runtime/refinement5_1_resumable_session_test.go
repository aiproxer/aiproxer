package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestRefinement51SequentialCallsKeepLocalClosureAndALegContinuity(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, store)
	sink := &refinement51Sink{}
	ex := refinement51Executor(t, store, sink, mgr)

	firstCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "refinement5-1-session",
			ContinuityKey:          "refinement5-1-session",
		},
		Route: aipapi.RouteIntent{Selector: "bad1:model!bad2:model|ok:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("call one")},
		}},
	}

	firstStream, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	firstID, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	firstALegID := strings.TrimSpace(firstCall.Session.ALegID)
	if firstALegID == "" {
		t.Fatal("first invocation did not expose an A-leg")
	}
	if firstCall.Session.ResumeToken == "" || firstCall.Session.AuthoritativeSessionID == "" {
		t.Fatal("first invocation did not expose resumable session identity")
	}

	firstRecords := snapshotTerminalUsage(sink)
	if len(firstRecords.calls) != 1 {
		t.Fatalf("call closures after call 1 = %d, want 1", len(firstRecords.calls))
	}
	firstClosure := cloneRefinement51CallRecord(firstRecords.calls[0])
	firstExpected := append([]string(nil), firstClosure.ExpectedBLegIDs...)
	if firstClosure.CallID != firstID {
		t.Fatalf("call 1 closure ID = %q, want stream ID %q", firstClosure.CallID, firstID)
	}
	if firstClosure.ALegID != firstALegID {
		t.Fatalf("call 1 closure A-leg = %q, want %q", firstClosure.ALegID, firstALegID)
	}
	if len(firstExpected) != 3 {
		t.Fatalf("call 1 expected B-leg set = %#v, want two raced failures plus winner", firstExpected)
	}
	if len(firstRecords.legs) != 3 {
		t.Fatalf("legs after call 1 = %d, want two raced failures plus winner", len(firstRecords.legs))
	}
	firstLegIDs := make(map[string]struct{}, len(firstRecords.legs))
	firstLegSnapshots := make(map[string]usageevidence.CallLegUsageRecord, len(firstRecords.legs))
	firstAttemptSeqs := make(map[int]struct{}, len(firstRecords.legs))
	for _, leg := range firstRecords.legs {
		if leg.CallID != firstID {
			t.Fatalf("call 1 leg %q has foreign BillingCallID %q", leg.BLegID, leg.CallID)
		}
		if leg.BLegID == "" {
			t.Fatal("call 1 has an empty B-leg ID")
		}
		firstLegIDs[leg.BLegID] = struct{}{}
		firstLegSnapshots[leg.BLegID] = cloneRefinement51CallLegRecord(leg)
		firstAttemptSeqs[leg.AttemptSeq] = struct{}{}
	}
	if len(firstLegIDs) != len(firstExpected) {
		t.Fatalf("call 1 expected B-leg set %v does not match terminal legs %v", firstExpected, firstLegIDs)
	}
	for _, bLegID := range firstExpected {
		if _, ok := firstLegIDs[bLegID]; !ok {
			t.Fatalf("call 1 expected B-leg %q was not terminalized", bLegID)
		}
	}

	firstAttempts, err := store.LoadAttempts(context.Background(), firstALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstAttempts) != 3 {
		t.Fatalf("persisted attempts after call 1 = %d, want 3", len(firstAttempts))
	}
	firstAttemptSnapshots := make(map[string]aipapi.AttemptRecord, len(firstAttempts))
	for _, att := range firstAttempts {
		firstAttemptSnapshots[att.BLegID] = att
	}

	// Review Finding 3: Assert semantic ownership of failure/race/fallback attempts
	legsByBackend := make(map[string]usageevidence.CallLegUsageRecord, len(firstRecords.legs))
	for _, leg := range firstRecords.legs {
		legsByBackend[leg.BackendID] = leg
	}
	attemptsByBackend := make(map[string]aipapi.AttemptRecord, len(firstAttempts))
	for _, att := range firstAttempts {
		attemptsByBackend[att.BackendID] = att
	}
	for _, backendID := range []string{"bad1", "bad2", "ok"} {
		leg, ok := legsByBackend[backendID]
		if !ok {
			t.Fatalf("call 1 missing leg record for backend %q", backendID)
		}
		att, ok := attemptsByBackend[backendID]
		if !ok {
			t.Fatalf("call 1 missing persisted attempt for backend %q", backendID)
		}
		if leg.CallID != firstID {
			t.Fatalf("leg for %s has CallID %q, want %q", backendID, leg.CallID, firstID)
		}
		if leg.ALegID != firstALegID {
			t.Fatalf("leg for %s has ALegID %q, want %q", backendID, leg.ALegID, firstALegID)
		}
		if att.ALegID != firstALegID {
			t.Fatalf("attempt for %s has ALegID %q, want %q", backendID, att.ALegID, firstALegID)
		}
		if leg.BLegID != att.BLegID {
			t.Fatalf("B-leg identity mismatch for %s: leg=%q attempt=%q", backendID, leg.BLegID, att.BLegID)
		}
		if leg.AttemptSeq != att.Seq {
			t.Fatalf("sequence mismatch for %s: leg=%d attempt=%d", backendID, leg.AttemptSeq, att.Seq)
		}

		switch backendID {
		case "bad1", "bad2":
			if leg.Outcome != usageevidence.LegOutcomeFailed {
				t.Fatalf("leg %s outcome = %q, want %q", backendID, leg.Outcome, usageevidence.LegOutcomeFailed)
			}
			if leg.Surfaced != usageevidence.SurfacedNo {
				t.Fatalf("leg %s surfaced = %v, want SurfacedNo", backendID, leg.Surfaced)
			}
			if att.Outcome != aipapi.AttemptSwallowedFailure {
				t.Fatalf("attempt %s outcome = %q, want %q", backendID, att.Outcome, aipapi.AttemptSwallowedFailure)
			}
			if att.Reason != "parallel leg failed before winner" {
				t.Fatalf("attempt %s reason = %q, want 'parallel leg failed before winner'", backendID, att.Reason)
			}
		case "ok":
			if leg.Outcome != usageevidence.LegOutcomeWinner {
				t.Fatalf("leg %s outcome = %q, want %q", backendID, leg.Outcome, usageevidence.LegOutcomeWinner)
			}
			if leg.Surfaced != usageevidence.SurfacedYes {
				t.Fatalf("leg %s surfaced = %v, want SurfacedYes", backendID, leg.Surfaced)
			}
			assertNonEmptyEconomicData(t, leg)
			if att.Outcome != aipapi.AttemptSuccess {
				t.Fatalf("attempt %s outcome = %q, want %q", backendID, att.Outcome, aipapi.AttemptSuccess)
			}
			if att.Reason != "success" {
				t.Fatalf("attempt %s unexpected reason: %q, want 'success'", backendID, att.Reason)
			}
		}
	}

	secondCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
			ContinuityKey:          firstCall.Session.ContinuityKey,
			ResumeToken:            firstCall.Session.ResumeToken,
			ALegID:                 firstALegID,
		},
		Route: aipapi.RouteIntent{Selector: "ok:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("call two")},
		}},
	}

	secondStream, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	secondID, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("second invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatalf("call 2 reused call 1 BillingCallID %q", firstID)
	}
	if strings.TrimSpace(secondCall.Session.ALegID) != firstALegID {
		t.Fatalf("call 2 A-leg = %q, want resumed A-leg %q", secondCall.Session.ALegID, firstALegID)
	}

	secondRecords := snapshotTerminalUsage(sink)
	if len(secondRecords.calls) != 2 {
		t.Fatalf("call closures after call 2 = %d, want 2", len(secondRecords.calls))
	}
	if len(secondRecords.legs) != 4 {
		t.Fatalf("legs after call 2 = %d, want 4 total", len(secondRecords.legs))
	}
	var secondClosure usageevidence.CallUsageRecord
	for _, closure := range secondRecords.calls {
		switch closure.CallID {
		case firstID:
			assertCallUsageRecordEqual(t, closure, firstClosure, "call 1 closure changed after call 2")
		case secondID:
			secondClosure = closure
		default:
			t.Fatalf("unexpected BillingCallID in closure record: %q", closure.CallID)
		}
	}
	if secondClosure.CallID == "" {
		t.Fatalf("call 2 closure missing for BillingCallID %q", secondID)
	}
	if secondClosure.ALegID != firstALegID {
		t.Fatalf("call 2 closure A-leg = %q, want %q", secondClosure.ALegID, firstALegID)
	}
	if len(secondClosure.ExpectedBLegIDs) != 1 {
		t.Fatalf("call 2 expected B-leg set = %#v, want only its new winner", secondClosure.ExpectedBLegIDs)
	}

	// Review Finding 1: Explicit cross-call B-leg identity disjointness
	for _, bLegID := range secondClosure.ExpectedBLegIDs {
		if _, found := firstLegIDs[bLegID]; found {
			t.Fatalf("call 2 expected B-leg %q was present in call 1 legs %v", bLegID, firstLegIDs)
		}
	}

	var secondLeg usageevidence.CallLegUsageRecord
	call1Legs, call2Legs := 0, 0
	for _, leg := range secondRecords.legs {
		switch leg.CallID {
		case firstID:
			call1Legs++
			// Review Finding 2: Prove full call-1 B-leg record immutability (deep compare)
			initialLeg, ok := firstLegSnapshots[leg.BLegID]
			if !ok {
				t.Fatalf("unexpected call 1 leg %q in terminal sink after call 2", leg.BLegID)
			}
			assertCallLegUsageRecordEqual(t, leg, initialLeg, "call 1 leg mutated after call 2")
		case secondID:
			call2Legs++
			secondLeg = leg
			// Review Finding 1: Disjointness for secondLeg
			if _, found := firstLegIDs[leg.BLegID]; found {
				t.Fatalf("call 2 terminal B-leg %q was present in call 1 legs %v", leg.BLegID, firstLegIDs)
			}
		default:
			t.Fatalf("unexpected BillingCallID in B-leg record: %q", leg.CallID)
		}
	}
	if call1Legs != len(firstLegIDs) || call2Legs != 1 {
		t.Fatalf("B-leg ownership after call 2 = call1:%d call2:%d, want call1:%d call2:1", call1Legs, call2Legs, len(firstLegIDs))
	}
	if secondLeg.BLegID == "" {
		t.Fatal("call 2 terminal B-leg is empty")
	}
	if secondClosure.ExpectedBLegIDs[0] != secondLeg.BLegID {
		t.Fatalf("call 2 expected B-leg %q does not match terminal leg %q", secondClosure.ExpectedBLegIDs[0], secondLeg.BLegID)
	}

	// Finding 1 & 2: Explicit cross-call attempt sequence disjointness
	if secondLeg.AttemptSeq <= 0 {
		t.Fatalf("call 2 attempt sequence %d must be positive", secondLeg.AttemptSeq)
	}
	if _, found := firstAttemptSeqs[secondLeg.AttemptSeq]; found {
		t.Fatalf("call 2 attempt sequence %d was present in call 1 sequences %v", secondLeg.AttemptSeq, firstAttemptSeqs)
	}

	// Finding 2: Prove actual Call 1 deep immutability across Call 2.
	// Mutate the actual stored/returned Call 2 record in sink deeply across every populated nested carrier:
	sink.mu.Lock()
	var call2LegInSink *usageevidence.CallLegUsageRecord
	for i := range sink.legs {
		if sink.legs[i].CallID == secondID {
			call2LegInSink = &sink.legs[i]
			break
		}
	}
	if call2LegInSink == nil {
		sink.mu.Unlock()
		t.Fatal("missing call 2 leg in sink for mutation test")
	}
	if call2LegInSink.AttemptSeq != secondLeg.AttemptSeq {
		sink.mu.Unlock()
		t.Fatalf("call 2 stored sink leg AttemptSeq %d != captured leg AttemptSeq %d", call2LegInSink.AttemptSeq, secondLeg.AttemptSeq)
	}
	if len(call2LegInSink.Observations) > 0 {
		obs := &call2LegInSink.Observations[0]
		obs.Scope.Roles[0] = "MUTATED_ROLE_CALL2"
		obs.Scope.SafeClaims["tier"] = "MUTATED_CLAIM_CALL2"
		obs.Scope.PolicyLabels["env"] = "MUTATED_LABEL_CALL2"
		if len(obs.Measures) > 0 {
			obs.Measures[0].Key.Dimensions[0].Value = "MUTATED_DIM_CALL2"
			if obs.Measures[0].Value != nil {
				*obs.Measures[0].Value = metering.Decimal{Coefficient: "999999", Scale: 4}
			}
		}
		if len(obs.Charges) > 0 {
			if obs.Charges[0].Component != nil && len(obs.Charges[0].Component.Dimensions) > 0 {
				obs.Charges[0].Component.Dimensions[0].Value = "MUTATED_CHG_DIM_CALL2"
			}
			if obs.Charges[0].Amount != nil {
				*obs.Charges[0].Amount = metering.Decimal{Coefficient: "888888", Scale: 4}
			}
			if len(obs.Charges[0].Covers) > 0 {
				obs.Charges[0].Covers[0].Ref.ObservationID = "MUTATED_COV_CALL2"
			}
		}
		if len(obs.Evidence) > 0 {
			obs.Evidence[0].Lexeme = "MUTATED_LEXEME_CALL2"
		}
		if len(obs.Supersedes) > 0 {
			obs.Supersedes[0].PayloadHash = "MUTATED_HASH_CALL2"
		}
	}
	if len(call2LegInSink.EvidenceConflicts) > 0 {
		call2LegInSink.EvidenceConflicts[0].Identity = "MUTATED_CONFLICT_CALL2"
	}
	if len(call2LegInSink.EconomicDispositions) > 0 {
		call2LegInSink.EconomicDispositions[0].CoverageReason = "MUTATED_DISP_CALL2"
	}

	// Assert the actual Call 1 records stored in sink remain completely unaffected by Call 2 mutations:
	for _, leg := range sink.legs {
		if leg.CallID == firstID {
			initialLeg, ok := firstLegSnapshots[leg.BLegID]
			if !ok {
				t.Fatalf("unexpected call 1 leg %q in sink", leg.BLegID)
			}
			assertCallLegUsageRecordEqual(t, leg, initialLeg, "call 1 actual leg in sink mutated after call 2 deep mutation")
			if leg.Outcome == usageevidence.LegOutcomeWinner && len(leg.Observations) > 0 {
				if leg.Observations[0].Scope.Roles[0] == "MUTATED_ROLE_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Scope.Roles aliased call 2")
				}
				if leg.Observations[0].Scope.SafeClaims["tier"] == "MUTATED_CLAIM_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Scope.SafeClaims aliased call 2")
				}
				if leg.Observations[0].Scope.PolicyLabels["env"] == "MUTATED_LABEL_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Scope.PolicyLabels aliased call 2")
				}
				if leg.Observations[0].Measures[0].Key.Dimensions[0].Value == "MUTATED_DIM_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Measures.Key.Dimensions aliased call 2")
				}
				if leg.Observations[0].Measures[0].Value != nil && leg.Observations[0].Measures[0].Value.Coefficient == "999999" {
					t.Fatal("cross-call aliasing detected: call 1 Measure.Value pointer aliased call 2")
				}
				if leg.Observations[0].Charges[0].Component.Dimensions[0].Value == "MUTATED_CHG_DIM_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Charges.Component.Dimensions aliased call 2")
				}
				if leg.Observations[0].Charges[0].Amount != nil && leg.Observations[0].Charges[0].Amount.Coefficient == "888888" {
					t.Fatal("cross-call aliasing detected: call 1 Charges.Amount pointer aliased call 2")
				}
				if len(leg.Observations[0].Charges[0].Covers) > 0 && leg.Observations[0].Charges[0].Covers[0].Ref.ObservationID == "MUTATED_COV_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Charges.Covers aliased call 2")
				}
				if leg.Observations[0].Evidence[0].Lexeme == "MUTATED_LEXEME_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Evidence aliased call 2")
				}
				if leg.Observations[0].Supersedes[0].PayloadHash == "MUTATED_HASH_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 Supersedes aliased call 2")
				}
				if len(leg.EvidenceConflicts) > 0 && leg.EvidenceConflicts[0].Identity == "MUTATED_CONFLICT_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 EvidenceConflicts aliased call 2")
				}
				if len(leg.EconomicDispositions) > 0 && leg.EconomicDispositions[0].CoverageReason == "MUTATED_DISP_CALL2" {
					t.Fatal("cross-call aliasing detected: call 1 EconomicDispositions aliased call 2")
				}
			}
		}
	}
	sink.mu.Unlock()

	attempts, err := store.LoadAttempts(context.Background(), firstALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 4 {
		t.Fatalf("persisted attempts = %d, want 4 across two calls", len(attempts))
	}
	seenSequences := make(map[int]struct{}, len(attempts))
	for _, attempt := range attempts {
		if attempt.Seq <= 0 || attempt.BLegID == "" {
			t.Fatalf("invalid persisted attempt: %#v", attempt)
		}
		if _, duplicate := seenSequences[attempt.Seq]; duplicate {
			t.Fatalf("duplicate persisted B-leg sequence %d", attempt.Seq)
		}
		seenSequences[attempt.Seq] = struct{}{}

		// Review Finding 2: Compare call 1 persisted attempts for immutability
		if initialAtt, ok := firstAttemptSnapshots[attempt.BLegID]; ok {
			if !reflect.DeepEqual(attempt, initialAtt) {
				t.Fatalf("call 1 persisted attempt %q mutated after call 2: before=%#v after=%#v", attempt.BLegID, initialAtt, attempt)
			}
		} else {
			// Review Finding 1: Disjointness of call 2 attempt
			if _, found := firstLegIDs[attempt.BLegID]; found {
				t.Fatalf("call 2 persisted attempt B-leg %q was present in call 1 legs %v", attempt.BLegID, firstLegIDs)
			}
		}
	}

	// Finding 3: Semantic ownership of Call 2 execution
	if secondLeg.Outcome != usageevidence.LegOutcomeWinner || secondLeg.Surfaced != usageevidence.SurfacedYes {
		t.Fatalf("call 2 leg unexpected outcome/surfaced: outcome=%q surfaced=%v", secondLeg.Outcome, secondLeg.Surfaced)
	}
	if secondLeg.CallID != secondID || secondLeg.ALegID != firstALegID {
		t.Fatalf("call 2 leg unexpected CallID/ALegID: callID=%q aLegID=%q", secondLeg.CallID, secondLeg.ALegID)
	}
	assertNonEmptyEconomicData(t, secondLeg)
	var secondAttempt aipapi.AttemptRecord
	for _, att := range attempts {
		if att.BLegID == secondLeg.BLegID {
			secondAttempt = att
			break
		}
	}
	if secondAttempt.BLegID == "" {
		t.Fatalf("call 2 attempt missing for B-leg %q", secondLeg.BLegID)
	}
	if secondAttempt.Outcome != aipapi.AttemptSuccess || secondAttempt.Reason != "success" {
		t.Fatalf("call 2 attempt unexpected outcome/reason: outcome=%q reason=%q", secondAttempt.Outcome, secondAttempt.Reason)
	}
	if secondAttempt.BackendID != "ok" || secondAttempt.ALegID != firstALegID {
		t.Fatalf("call 2 attempt unexpected backend/aLeg: backend=%q aLeg=%q", secondAttempt.BackendID, secondAttempt.ALegID)
	}
	// Direct exact correlation: actual captured/stored Call 2 leg AttemptSeq == actual Call 2 b2bua attempt.Seq
	if secondLeg.AttemptSeq != secondAttempt.Seq {
		t.Fatalf("call 2 captured leg AttemptSeq %d != b2bua attempt Seq %d", secondLeg.AttemptSeq, secondAttempt.Seq)
	}
	if call2LegInSink.AttemptSeq != secondAttempt.Seq {
		t.Fatalf("call 2 stored sink leg AttemptSeq %d != b2bua attempt Seq %d", call2LegInSink.AttemptSeq, secondAttempt.Seq)
	}
}

func TestRefinement51DurableResumeAfterExecutorRestartKeepsCallOwnership(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	sink := &refinement51Sink{}
	firstExecutor := refinement51Executor(t, store, sink, testSecureManager(t, memSS, store))

	firstCall := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: "refinement5-1-restart-session",
		ContinuityKey:          "refinement5-1-restart-session",
	}, "bad1:model!bad2:model|ok:model", "before restart")
	firstStream, err := firstExecutor.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	firstID, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	firstALegID := strings.TrimSpace(firstCall.Session.ALegID)
	if firstALegID == "" || firstCall.Session.ResumeToken == "" {
		t.Fatalf("first invocation missing durable continuation identity: %+v", firstCall.Session)
	}
	firstRecords := snapshotTerminalUsage(sink)
	if len(firstRecords.calls) != 1 || len(firstRecords.legs) != 3 {
		t.Fatalf("first invocation records = calls:%d legs:%d, want 1 call and 3 legs", len(firstRecords.calls), len(firstRecords.legs))
	}
	firstClosure := cloneRefinement51CallRecord(firstRecords.calls[0])
	firstLegIDs := make(map[string]struct{}, len(firstRecords.legs))
	firstLegSnapshots := make(map[string]usageevidence.CallLegUsageRecord, len(firstRecords.legs))
	firstAttemptSeqs := make(map[int]struct{}, len(firstRecords.legs))
	for _, leg := range firstRecords.legs {
		if leg.CallID != firstID {
			t.Fatalf("call 1 leg has foreign call ID %q", leg.CallID)
		}
		if leg.Outcome == usageevidence.LegOutcomeWinner {
			assertNonEmptyEconomicData(t, leg)
		}
		firstLegIDs[leg.BLegID] = struct{}{}
		firstLegSnapshots[leg.BLegID] = cloneRefinement51CallLegRecord(leg)
		firstAttemptSeqs[leg.AttemptSeq] = struct{}{}
	}

	firstAttempts, err := store.LoadAttempts(context.Background(), firstALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstAttempts) != 3 {
		t.Fatalf("first invocation attempts = %d, want 3", len(firstAttempts))
	}
	firstAttemptSnapshots := make(map[string]aipapi.AttemptRecord, len(firstAttempts))
	for _, att := range firstAttempts {
		firstAttemptSnapshots[att.BLegID] = att
		switch att.BackendID {
		case "bad1", "bad2":
			if att.Outcome != aipapi.AttemptSwallowedFailure || att.Reason != "parallel leg failed before winner" {
				t.Fatalf("attempt %s unexpected: %+v", att.BackendID, att)
			}
		case "ok":
			if att.Outcome != aipapi.AttemptSuccess || att.Reason != "success" {
				t.Fatalf("attempt %s unexpected: %+v", att.BackendID, att)
			}
		}
	}

	// Recreate the secure-session manager over the same durable store. The
	// executor and manager instances are intentionally distinct across calls.
	secondExecutor := refinement51Executor(t, store, sink, testSecureManager(t, memSS, store))
	secondCall := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
		ContinuityKey:          firstCall.Session.ContinuityKey,
		ResumeToken:            firstCall.Session.ResumeToken,
		ALegID:                 firstALegID,
	}, "ok:model", "after restart")
	secondStream, err := secondExecutor.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	secondID, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("resumed invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatalf("resumed invocation reused BillingCallID %q after restart", firstID)
	}
	if secondCall.Session.ALegID != firstALegID {
		t.Fatalf("resumed A-leg = %q, want %q", secondCall.Session.ALegID, firstALegID)
	}

	secondRecords := snapshotTerminalUsage(sink)
	if len(secondRecords.calls) != 2 || len(secondRecords.legs) != 4 {
		t.Fatalf("records after restart = calls:%d legs:%d, want calls:2 legs:4", len(secondRecords.calls), len(secondRecords.legs))
	}
	var secondClosure usageevidence.CallUsageRecord
	var secondLeg usageevidence.CallLegUsageRecord
	call1LegCount := 0
	for _, closure := range secondRecords.calls {
		switch closure.CallID {
		case firstID:
			assertCallUsageRecordEqual(t, closure, firstClosure, "call 1 closure changed after restart")
		case secondID:
			secondClosure = cloneRefinement51CallRecord(closure)
		default:
			t.Fatalf("unexpected BillingCallID after restart: %q", closure.CallID)
		}
	}
	for _, leg := range secondRecords.legs {
		switch leg.CallID {
		case firstID:
			call1LegCount++
			// Review Finding 2: Deep compare every call 1 leg after manager restart
			snapshot, ok := firstLegSnapshots[leg.BLegID]
			if !ok {
				t.Fatalf("unexpected call 1 leg %q in sink after restart", leg.BLegID)
			}
			assertCallLegUsageRecordEqual(t, leg, snapshot, "call 1 leg changed after restart")
		case secondID:
			secondLeg = leg
			// Review Finding 1: Disjointness for call 2 terminal leg
			if _, found := firstLegIDs[leg.BLegID]; found {
				t.Fatalf("call 2 terminal B-leg %q was present in call 1 legs %v after restart", leg.BLegID, firstLegIDs)
			}
		default:
			t.Fatalf("unexpected B-leg call ID after restart: %q", leg.CallID)
		}
	}
	if call1LegCount != 3 {
		t.Fatalf("call 1 legs after restart = %d, want 3", call1LegCount)
	}
	if secondClosure.CallID != secondID || secondLeg.CallID != secondID {
		t.Fatalf("call 2 records missing after restart: closure=%q leg=%q want %q", secondClosure.CallID, secondLeg.CallID, secondID)
	}
	if len(secondClosure.ExpectedBLegIDs) != 1 || secondClosure.ExpectedBLegIDs[0] != secondLeg.BLegID {
		t.Fatalf("call 2 expected B-legs after restart = %v, want [%s]", secondClosure.ExpectedBLegIDs, secondLeg.BLegID)
	}

	// Review Finding 1: Explicit cross-call B-leg identity disjointness after manager recreation
	for _, bLegID := range secondClosure.ExpectedBLegIDs {
		if _, found := firstLegIDs[bLegID]; found {
			t.Fatalf("call 2 expected B-leg %q was present in call 1 legs %v after restart", bLegID, firstLegIDs)
		}
	}

	// Finding 1 & 2: Explicit cross-call attempt sequence disjointness after restart
	if secondLeg.AttemptSeq <= 0 {
		t.Fatalf("call 2 attempt sequence %d must be positive after restart", secondLeg.AttemptSeq)
	}
	if _, found := firstAttemptSeqs[secondLeg.AttemptSeq]; found {
		t.Fatalf("call 2 attempt sequence %d was present in call 1 sequences %v after restart", secondLeg.AttemptSeq, firstAttemptSeqs)
	}

	// Finding 2: Prove actual Call 1 deep immutability across Call 2 after manager restart.
	// Mutate the actual stored/returned Call 2 record in sink deeply across every populated nested carrier:
	sink.mu.Lock()
	var call2LegInSink *usageevidence.CallLegUsageRecord
	for i := range sink.legs {
		if sink.legs[i].CallID == secondID {
			call2LegInSink = &sink.legs[i]
			break
		}
	}
	if call2LegInSink == nil {
		sink.mu.Unlock()
		t.Fatal("missing call 2 leg in sink for mutation test after restart")
	}
	if call2LegInSink.AttemptSeq != secondLeg.AttemptSeq {
		sink.mu.Unlock()
		t.Fatalf("call 2 stored sink leg AttemptSeq %d != captured leg AttemptSeq %d after restart", call2LegInSink.AttemptSeq, secondLeg.AttemptSeq)
	}
	if len(call2LegInSink.Observations) > 0 {
		obs := &call2LegInSink.Observations[0]
		obs.Scope.Roles[0] = "MUTATED_ROLE_RESTART"
		obs.Scope.SafeClaims["tier"] = "MUTATED_CLAIM_RESTART"
		obs.Scope.PolicyLabels["env"] = "MUTATED_LABEL_RESTART"
		if len(obs.Measures) > 0 {
			obs.Measures[0].Key.Dimensions[0].Value = "MUTATED_DIM_RESTART"
			if obs.Measures[0].Value != nil {
				*obs.Measures[0].Value = metering.Decimal{Coefficient: "999999", Scale: 4}
			}
		}
		if len(obs.Charges) > 0 {
			if obs.Charges[0].Component != nil && len(obs.Charges[0].Component.Dimensions) > 0 {
				obs.Charges[0].Component.Dimensions[0].Value = "MUTATED_CHG_DIM_RESTART"
			}
			if obs.Charges[0].Amount != nil {
				*obs.Charges[0].Amount = metering.Decimal{Coefficient: "888888", Scale: 4}
			}
			if len(obs.Charges[0].Covers) > 0 {
				obs.Charges[0].Covers[0].Ref.ObservationID = "MUTATED_COV_RESTART"
			}
		}
		if len(obs.Evidence) > 0 {
			obs.Evidence[0].Lexeme = "MUTATED_LEXEME_RESTART"
		}
		if len(obs.Supersedes) > 0 {
			obs.Supersedes[0].PayloadHash = "MUTATED_HASH_RESTART"
		}
	}
	if len(call2LegInSink.EvidenceConflicts) > 0 {
		call2LegInSink.EvidenceConflicts[0].Identity = "MUTATED_CONFLICT_RESTART"
	}
	if len(call2LegInSink.EconomicDispositions) > 0 {
		call2LegInSink.EconomicDispositions[0].CoverageReason = "MUTATED_DISP_RESTART"
	}

	// Assert the actual Call 1 records stored in sink remain completely unaffected by Call 2 mutations:
	for _, leg := range sink.legs {
		if leg.CallID == firstID {
			initialLeg, ok := firstLegSnapshots[leg.BLegID]
			if !ok {
				t.Fatalf("unexpected call 1 leg %q in sink after restart", leg.BLegID)
			}
			assertCallLegUsageRecordEqual(t, leg, initialLeg, "call 1 actual leg in sink mutated after call 2 deep mutation in restart test")
			if leg.Outcome == usageevidence.LegOutcomeWinner && len(leg.Observations) > 0 {
				if leg.Observations[0].Scope.Roles[0] == "MUTATED_ROLE_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Scope.Roles aliased call 2")
				}
				if leg.Observations[0].Scope.SafeClaims["tier"] == "MUTATED_CLAIM_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Scope.SafeClaims aliased call 2")
				}
				if leg.Observations[0].Scope.PolicyLabels["env"] == "MUTATED_LABEL_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Scope.PolicyLabels aliased call 2")
				}
				if leg.Observations[0].Measures[0].Key.Dimensions[0].Value == "MUTATED_DIM_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Measures.Key.Dimensions aliased call 2")
				}
				if leg.Observations[0].Measures[0].Value != nil && leg.Observations[0].Measures[0].Value.Coefficient == "999999" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Measure.Value pointer aliased call 2")
				}
				if leg.Observations[0].Charges[0].Component.Dimensions[0].Value == "MUTATED_CHG_DIM_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Charges.Component.Dimensions aliased call 2")
				}
				if leg.Observations[0].Charges[0].Amount != nil && leg.Observations[0].Charges[0].Amount.Coefficient == "888888" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Charges.Amount pointer aliased call 2")
				}
				if len(leg.Observations[0].Charges[0].Covers) > 0 && leg.Observations[0].Charges[0].Covers[0].Ref.ObservationID == "MUTATED_COV_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Charges.Covers aliased call 2")
				}
				if leg.Observations[0].Evidence[0].Lexeme == "MUTATED_LEXEME_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Evidence aliased call 2")
				}
				if leg.Observations[0].Supersedes[0].PayloadHash == "MUTATED_HASH_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 Supersedes aliased call 2")
				}
				if len(leg.EvidenceConflicts) > 0 && leg.EvidenceConflicts[0].Identity == "MUTATED_CONFLICT_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 EvidenceConflicts aliased call 2")
				}
				if len(leg.EconomicDispositions) > 0 && leg.EconomicDispositions[0].CoverageReason == "MUTATED_DISP_RESTART" {
					t.Fatal("cross-call aliasing detected after restart: call 1 EconomicDispositions aliased call 2")
				}
			}
		}
	}
	sink.mu.Unlock()

	secondAttempts, err := store.LoadAttempts(context.Background(), firstALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondAttempts) != 4 {
		t.Fatalf("persisted attempts after restart = %d, want 4", len(secondAttempts))
	}
	var secondAttempt aipapi.AttemptRecord
	for _, att := range secondAttempts {
		if initialAtt, ok := firstAttemptSnapshots[att.BLegID]; ok {
			// Review Finding 2: Immutability of call 1 attempts after manager restart
			if !reflect.DeepEqual(att, initialAtt) {
				t.Fatalf("call 1 persisted attempt %q mutated after restart: before=%#v after=%#v", att.BLegID, initialAtt, att)
			}
		} else {
			// Review Finding 1: Disjointness of call 2 attempt after restart
			if _, found := firstLegIDs[att.BLegID]; found {
				t.Fatalf("call 2 persisted attempt B-leg %q matches call 1 B-leg", att.BLegID)
			}
			secondAttempt = att
		}
	}
	if secondAttempt.BLegID == "" {
		t.Fatal("missing call 2 attempt after restart")
	}
	if secondAttempt.BackendID != "ok" || secondAttempt.Outcome != aipapi.AttemptSuccess || secondAttempt.Reason != "success" {
		t.Fatalf("call 2 attempt unexpected state after restart: %+v", secondAttempt)
	}
	// Direct exact correlation: actual captured/stored Call 2 leg AttemptSeq == actual Call 2 b2bua attempt.Seq
	if secondLeg.AttemptSeq != secondAttempt.Seq {
		t.Fatalf("call 2 captured leg AttemptSeq %d != b2bua attempt Seq %d after restart", secondLeg.AttemptSeq, secondAttempt.Seq)
	}
	if call2LegInSink.AttemptSeq != secondAttempt.Seq {
		t.Fatalf("call 2 stored sink leg AttemptSeq %d != b2bua attempt Seq %d after restart", call2LegInSink.AttemptSeq, secondAttempt.Seq)
	}
	assertNonEmptyEconomicData(t, secondLeg)
}

// TestRefinement51IdleRetirementDisconnectLifecycle exercises real production
// continuity/retirement APIs to prove:
//  1. Idle session quiescent state produces zero usage, leg, or call records.
//  2. Disconnect / context cancellation between calls does not mutate prior call
//     records or gate settlement.
//  3. A-leg TTL expiry / retirement is solely a storage concern; it fires the
//     retirement observer and cleans continuity state, but emits ZERO new economic
//     records or journals.
//  4. Completed call economics existed BEFORE retirement and remain intact.
func TestRefinement51IdleRetirementDisconnectLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	var nowMu sync.Mutex
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	advanceNow := func(d time.Duration) {
		nowMu.Lock()
		now = now.Add(d)
		nowMu.Unlock()
	}

	// Real non-zero TTL of 10 minutes
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{
		TTL: 10 * time.Minute,
		Now: getNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	retiredALegs := make(chan string, 10)
	store.SetALegRetirementObserver(func(aLegID string) {
		retiredALegs <- aLegID
	})

	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, store)
	sink := &refinement51Sink{}
	ex := refinement51Executor(t, store, sink, mgr)

	firstCall := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: "refinement5-1-lifecycle-session",
		ContinuityKey:          "refinement5-1-lifecycle-session",
	}, "ok:model", "call one before idle")

	firstStream, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	firstID, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatal(err)
	}
	firstALegID := strings.TrimSpace(firstCall.Session.ALegID)
	if firstALegID == "" {
		t.Fatal("missing ALegID")
	}

	// Call 1 terminal snapshot
	recordsAfterCall1 := snapshotTerminalUsage(sink)
	if len(recordsAfterCall1.calls) != 1 || len(recordsAfterCall1.legs) != 1 {
		t.Fatalf("records after call 1 = calls:%d legs:%d, want 1 each", len(recordsAfterCall1.calls), len(recordsAfterCall1.legs))
	}
	call1ClosureSnapshot := cloneRefinement51CallRecord(recordsAfterCall1.calls[0])
	call1LegSnapshot := cloneRefinement51CallLegRecord(recordsAfterCall1.legs[0])
	assertNonEmptyEconomicData(t, call1LegSnapshot)

	// 1. Idle lifecycle phase: 2 minutes elapse (within 10m TTL)
	advanceNow(2 * time.Minute)
	recordsAfterIdle := snapshotTerminalUsage(sink)
	if len(recordsAfterIdle.calls) != 1 || len(recordsAfterIdle.legs) != 1 {
		t.Fatalf("idle time created unexpected records: calls=%d legs=%d", len(recordsAfterIdle.calls), len(recordsAfterIdle.legs))
	}
	assertCallUsageRecordEqual(t, recordsAfterIdle.calls[0], call1ClosureSnapshot, "call 1 closure altered during idle")
	assertCallLegUsageRecordEqual(t, recordsAfterIdle.legs[0], call1LegSnapshot, "call 1 leg altered during idle")

	// 2. Disconnect / Context cancellation between calls (idle phase)
	idleCtx, idleCancel := context.WithCancel(context.Background())
	idleCancel()
	if _, err := store.FetchALeg(idleCtx, firstALegID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on FetchALeg, got %v", err)
	}
	// Also verify that an invocation attempt with a canceled context fails without mutating records
	canceledCall := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
		ContinuityKey:          firstCall.Session.ContinuityKey,
		ResumeToken:            firstCall.Session.ResumeToken,
		ALegID:                 firstALegID,
	}, "ok:model", "canceled attempt between turns")
	if _, err := ex.Execute(idleCtx, canceledCall); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on Execute, got %v", err)
	}
	recordsAfterDisconnect := snapshotTerminalUsage(sink)
	if len(recordsAfterDisconnect.calls) != 1 || len(recordsAfterDisconnect.legs) != 1 {
		t.Fatalf("disconnect altered records: calls=%d legs=%d", len(recordsAfterDisconnect.calls), len(recordsAfterDisconnect.legs))
	}
	assertCallUsageRecordEqual(t, recordsAfterDisconnect.calls[0], call1ClosureSnapshot, "call 1 closure altered after disconnect")
	assertCallLegUsageRecordEqual(t, recordsAfterDisconnect.legs[0], call1LegSnapshot, "call 1 leg altered after disconnect")

	// 3. Resumed Call 2 on same A-leg during idle (before TTL expiry)
	// Requirement 3.3, 3.5: Same A-leg resumes seamlessly during idle window.
	secondCall := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
		ContinuityKey:          firstCall.Session.ContinuityKey,
		ResumeToken:            firstCall.Session.ResumeToken,
		ALegID:                 firstALegID,
	}, "ok:model", "call two during idle before retirement")
	secondStream, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	secondID, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("missing BillingCallID for second call")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatalf("call 2 reused call 1 ID %q", firstID)
	}
	recordsAfterCall2 := snapshotTerminalUsage(sink)
	if len(recordsAfterCall2.calls) != 2 || len(recordsAfterCall2.legs) != 2 {
		t.Fatalf("records after call 2 = calls:%d legs:%d, want 2 each", len(recordsAfterCall2.calls), len(recordsAfterCall2.legs))
	}
	var call2ClosureSnapshot usageevidence.CallUsageRecord
	var call2LegSnapshot usageevidence.CallLegUsageRecord
	for _, c := range recordsAfterCall2.calls {
		if c.CallID == secondID {
			call2ClosureSnapshot = cloneRefinement51CallRecord(c)
		}
	}
	for _, l := range recordsAfterCall2.legs {
		if l.CallID == secondID {
			call2LegSnapshot = cloneRefinement51CallLegRecord(l)
		}
	}
	if call2ClosureSnapshot.CallID == "" || call2LegSnapshot.CallID == "" {
		t.Fatal("call 2 snapshots missing")
	}
	assertNonEmptyEconomicData(t, call2LegSnapshot)
	// Disjointness check
	if call2LegSnapshot.BLegID == call1LegSnapshot.BLegID {
		t.Fatalf("call 2 B-leg %q reuses call 1 B-leg %q", call2LegSnapshot.BLegID, call1LegSnapshot.BLegID)
	}

	// 4. Retirement lifecycle phase: advance time past 10m TTL (15m total)
	advanceNow(15 * time.Minute)

	// Trigger lazy sweep / eviction via LoadAttempts on the A-leg
	_, err = store.LoadAttempts(context.Background(), firstALegID)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("expected ErrALegNotFound on expired session, got %v", err)
	}

	// Verify retirement observer fired synchronously for firstALegID
	select {
	case retiredID := <-retiredALegs:
		if retiredID != firstALegID {
			t.Fatalf("retired A-leg = %q, want %q", retiredID, firstALegID)
		}
	default:
		t.Fatal("retirement observer did not fire synchronously on B2BUA LoadAttempts eviction")
	}

	// Requirement 3.1 & 3.6: Retirement is purely storage/continuity.
	// Assert NO new economic records were emitted to the sink, and completed
	// call economics existed before retirement and remain intact for both calls.
	recordsAfterRetirement := snapshotTerminalUsage(sink)
	if len(recordsAfterRetirement.calls) != 2 || len(recordsAfterRetirement.legs) != 2 {
		t.Fatalf("retirement created economic records: calls=%d legs=%d, want 2 each", len(recordsAfterRetirement.calls), len(recordsAfterRetirement.legs))
	}
	for _, c := range recordsAfterRetirement.calls {
		switch c.CallID {
		case firstID:
			assertCallUsageRecordEqual(t, c, call1ClosureSnapshot, "call 1 closure altered by retirement")
		case secondID:
			assertCallUsageRecordEqual(t, c, call2ClosureSnapshot, "call 2 closure altered by retirement")
		default:
			t.Fatalf("unexpected call ID in sink: %q", c.CallID)
		}
	}
	for _, l := range recordsAfterRetirement.legs {
		switch l.CallID {
		case firstID:
			assertCallLegUsageRecordEqual(t, l, call1LegSnapshot, "call 1 leg altered by retirement")
		case secondID:
			assertCallLegUsageRecordEqual(t, l, call2LegSnapshot, "call 2 leg altered by retirement")
		default:
			t.Fatalf("unexpected leg call ID in sink: %q", l.CallID)
		}
	}
}

// TestRefinement51ContextCancelMidFlightDoesNotCreatePhantomUsage proves that
// when an active invocation is canceled by the client, the attempt and call close
// with canceled outcomes, but no phantom output tokens or subsequent usage are billed.
func TestRefinement51ContextCancelMidFlightDoesNotCreatePhantomUsage(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, store)
	sink := &refinement51Sink{}
	ex := refinement51Executor(t, store, sink, mgr)

	started := make(chan struct{})
	ex.Backends["blocking"] = execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &cancellableTestStream{started: started}, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := refinement51Call(aipapi.SessionRef{
		AuthoritativeSessionID: "refinement5-1-cancel-session",
		ContinuityKey:          "refinement5-1-cancel-session",
	}, "blocking:model", "cancel mid flight")

	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	callID, ok := billingCallIDFromStream(stream)
	if !ok {
		t.Fatal("missing BillingCallID")
	}

	// Wait for stream to start in Collect, then cancel context. The Collect
	// goroutine is owned by this test and joined by both the success path and
	// cleanup so a startup/timeout failure cannot orphan it.
	collectDone := make(chan struct{})
	var collectErr error
	go func() {
		_, collectErr = aipapi.Collect(ctx, stream)
		close(collectDone)
	}()
	t.Cleanup(func() {
		refinement51JoinCollect(t, collectDone, cancel)
	})

	startupCtx, cancelStartup := context.WithTimeout(ctx, 5*time.Second)
	defer cancelStartup()
	select {
	case <-started:
		cancel()
	case <-collectDone:
		t.Fatalf("Collect returned before backend stream started: %v", collectErr)
	case <-startupCtx.Done():
		t.Fatalf("timed out waiting for backend stream to start: %v", startupCtx.Err())
	}

	joinCtx, cancelJoin := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelJoin()
	select {
	case <-collectDone:
	case <-joinCtx.Done():
		t.Fatalf("timed out joining Collect after cancel: %v", joinCtx.Err())
	}
	err = collectErr
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect error = %v, want context.Canceled", err)
	}

	records := snapshotTerminalUsage(sink)
	if len(records.calls) != 1 {
		t.Fatalf("call closures after cancel = %d, want 1", len(records.calls))
	}
	if len(records.legs) != 1 {
		t.Fatalf("legs after cancel = %d, want 1", len(records.legs))
	}
	callClosure := records.calls[0]
	leg := records.legs[0]

	if callClosure.CallID != callID {
		t.Fatalf("closure CallID = %q, want %q", callClosure.CallID, callID)
	}
	if callClosure.Outcome != usageevidence.TurnOutcomeCanceled {
		t.Fatalf("call closure outcome = %q, want %q", callClosure.Outcome, usageevidence.TurnOutcomeCanceled)
	}
	if leg.CallID != callID {
		t.Fatalf("leg CallID = %q, want %q", leg.CallID, callID)
	}
	if leg.Outcome != usageevidence.LegOutcomeCanceled {
		t.Fatalf("leg outcome = %q, want %q", leg.Outcome, usageevidence.LegOutcomeCanceled)
	}
	assertRefinement51NoPhantomTokenEconomics(t, leg)

	attempts, err := store.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts after cancel = %d, want 1", len(attempts))
	}
	att := attempts[0]
	if att.Outcome != aipapi.AttemptCancelled {
		t.Fatalf("attempt outcome = %q, want %q", att.Outcome, aipapi.AttemptCancelled)
	}
	if att.Reason != "context canceled" {
		t.Fatalf("attempt reason = %q, want 'context canceled'", att.Reason)
	}
	if att.BLegID != leg.BLegID {
		t.Fatalf("attempt BLegID = %q, want %q", att.BLegID, leg.BLegID)
	}
	if att.ALegID != leg.ALegID {
		t.Fatalf("attempt ALegID = %q, want %q", att.ALegID, leg.ALegID)
	}
}

func refinement51JoinCollect(t *testing.T, done <-chan struct{}, cancel context.CancelFunc) {
	t.Helper()
	if cancel != nil {
		cancel()
	}
	joinCtx, joinCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer joinCancel()
	select {
	case <-done:
	case <-joinCtx.Done():
		t.Errorf("Collect goroutine did not terminate after cancellation: %v", joinCtx.Err())
	}
}

func assertRefinement51NoPhantomTokenEconomics(t *testing.T, leg usageevidence.CallLegUsageRecord) {
	t.Helper()
	quantities := []struct {
		name  string
		value billingport.Quantity
	}{
		{"input tokens", leg.Evidence.InputTokens},
		{"output tokens", leg.Evidence.OutputTokens},
		{"reasoning tokens", leg.Evidence.ReasoningTokens},
		{"cache-read tokens", leg.Evidence.CacheReadTokens},
		{"cache-write tokens", leg.Evidence.CacheWriteTokens},
		{"total tokens", leg.Evidence.TotalTokens},
	}
	for _, quantity := range quantities {
		if quantity.value.Value != 0 {
			t.Fatalf("phantom %s billed: value=%d present=%t", quantity.name, quantity.value.Value, quantity.value.Present)
		}
	}
	if leg.Evidence.Cost.NanoUnits != 0 {
		t.Fatalf("phantom provider cost billed: nano=%d present=%t currency=%q", leg.Evidence.Cost.NanoUnits, leg.Evidence.Cost.Present, leg.Evidence.Cost.Currency)
	}
	if len(leg.ObservationRefs) != 0 || len(leg.EvidenceConflicts) != 0 || len(leg.EconomicDispositions) != 0 {
		t.Fatalf("phantom economic carriers: observation_refs=%d conflicts=%d dispositions=%d", len(leg.ObservationRefs), len(leg.EvidenceConflicts), len(leg.EconomicDispositions))
	}
	for _, observation := range leg.Observations {
		if len(observation.Charges) != 0 || len(observation.Evidence) != 0 {
			t.Fatalf("phantom observation economics: boundary=%q charges=%d evidence=%d", observation.Boundary, len(observation.Charges), len(observation.Evidence))
		}
		for _, measure := range observation.Measures {
			if !refinement51CanonicalProviderTokenComponent(measure.Key.Component) || measure.Value == nil {
				continue
			}
			normalized, err := measure.Value.Normalize()
			if err != nil {
				t.Fatalf("invalid token measure %q: %v", measure.Key.Component, err)
			}
			if normalized.Coefficient != "0" {
				t.Fatalf("phantom token measure: key=%+v value=%s", measure.Key, normalized.CanonicalString())
			}
		}
	}
}

func refinement51CanonicalProviderTokenComponent(component string) bool {
	switch component {
	case metering.ComponentInputToken,
		metering.ComponentInputTokenUncached,
		metering.ComponentInputTokenTotal,
		metering.ComponentCacheReadInputToken,
		metering.ComponentCacheWriteInputToken,
		metering.ComponentOutputToken,
		metering.ComponentReasoningOutputToken,
		metering.ComponentTotalToken:
		return true
	default:
		// Customer-boundary text_token measures from the local tokenizer are
		// service observations, not provider inference economics.
		return false
	}
}

type cancellableTestStream struct {
	started chan struct{}
	once    sync.Once
}

func (s *cancellableTestStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *cancellableTestStream) Close() error { return nil }

func (s *cancellableTestStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

type refinement51Sink struct {
	capturingTerminalUsageSink
}

func (s *refinement51Sink) AppendLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
	// Assert sink does not mutate received record
	before := record.Clone()
	captured := record.Clone()
	err := s.capturingTerminalUsageSink.AppendLeg(ctx, captured)
	if !reflect.DeepEqual(record, before) {
		panic("refinement51Sink mutated received CallLegUsageRecord")
	}
	return err
}

func (s *refinement51Sink) AppendCall(ctx context.Context, record usageevidence.CallUsageRecord) error {
	// Assert sink does not mutate received record
	before := cloneRefinement51CallRecord(record)
	captured := cloneRefinement51CallRecord(record)
	err := s.capturingTerminalUsageSink.AppendCall(ctx, captured)
	if !reflect.DeepEqual(record, before) {
		panic("refinement51Sink mutated received CallUsageRecord")
	}
	return err
}

func TestRefinement51RecordingSinkDoesNotMutateInputOrNestedFields(t *testing.T) {
	t.Parallel()
	sink := &refinement51Sink{}
	ctx := context.Background()

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	measureVal := metering.Decimal{Coefficient: "42", Scale: 0}
	chargeAmt := metering.Decimal{Coefficient: "250", Scale: 2}
	leg := usageevidence.CallLegUsageRecord{
		CallID:             callID,
		ALegID:             "aleg-sink-test",
		BLegID:             "bleg-sink-test",
		AttemptSeq:         1,
		BackendID:          "ok",
		ProviderID:         "provider",
		ModelID:            "model",
		StartedAt:          now,
		FinishedAt:         now.Add(2 * time.Second),
		Outcome:            usageevidence.LegOutcomeWinner,
		Surfaced:           usageevidence.SurfacedYes,
		EvidenceVersion:    usageevidence.EvidenceFormatVersionV2,
		EvidenceProjection: usageevidence.EvidenceProjectionV1,
		Observations: []metering.Observation{{
			Version:        metering.ObservationVersionV2,
			ID:             "obs-sink-test",
			SourceEventKey: "src-sink-test",
			Revision:       1,
			StreamID:       "stream-sink-test",
			Sequence:       1,
			Origin:         metering.OriginProvider,
			Acquisition:    metering.AcquisitionProviderResponse,
			Authority:      metering.AuthorityObservedClaim,
			Perspective:    metering.PerspectiveOperator,
			Boundary:       metering.BoundaryBackendEgress,
			Lifecycle:      metering.LifecycleBackendAttempt,
			Subject: metering.SubjectRef{
				Kind:    metering.SubjectBLeg,
				StoreID: "store-test",
				ALegID:  "aleg-sink-test",
				BLegID:  "bleg-sink-test",
			},
			Correlation: metering.CorrelationV2{
				StoreID:           "store-test",
				ALegID:            "aleg-sink-test",
				BLegID:            "bleg-sink-test",
				ProviderRequestID: "req-sink-test",
			},
			Semantics:  metering.SemanticsReplacement,
			ObservedAt: now,
			ReceivedAt: now,
			MappingRef: "provider.test.v1",
			Scope: scope.PrincipalScopeView{
				Roles:        []string{"role-operator", "role-billing"},
				SafeClaims:   map[string]string{"tier": "enterprise"},
				PolicyLabels: map[string]string{"env": "prod"},
			},
			Measures: []metering.Measure{{
				Key: metering.ComponentKey{
					Direction:  metering.DirectionInput,
					Component:  metering.ComponentInputToken,
					Unit:       metering.UnitToken,
					SchemaID:   metering.DefaultInclusionSchemaID,
					Dimensions: []metering.Dimension{{Name: "tier", Value: "std"}},
				},
				Value:   &measureVal,
				Quality: metering.QualityObserved,
			}},
			Charges: []metering.ReportedCharge{{
				ChargeItemID: "item-sink-test",
				Component: &metering.ComponentKey{
					Direction:  metering.DirectionOutput,
					Component:  metering.ComponentOutputToken,
					Unit:       metering.UnitToken,
					SchemaID:   "provider.tokens.v2",
					Dimensions: []metering.Dimension{{Name: "class", Value: "standard"}},
				},
				Amount:   &chargeAmt,
				Currency: "USD",
				Kind:     metering.ChargeKindComponent,
				Covers: []metering.ChargeCoverageRef{{
					Ref: metering.ChargeRef{
						StoreID:       "store-test",
						ObservationID: "obs-cov-sink",
						Revision:      1,
						ChargeItemID:  "item-cov-sink",
					},
					Relation: metering.CoverageInclusive,
				}},
			}},
			Evidence: []metering.SafeEvidenceField{{
				Path:        "x-request-id",
				Lexeme:      "req-12345",
				Present:     true,
				Acquisition: metering.AcquisitionProviderResponse,
			}},
			Supersedes: []metering.ObservationRef{{
				StoreID:       "store-test",
				ObservationID: "obs-prev-sink",
				Revision:      1,
				PayloadHash:   "hash-prev-sink",
			}},
		}},
		ObservationRefs: nil, // Production billingLegRecord does NOT expose ObservationRefs!
		EvidenceConflicts: []usageevidence.EvidenceConflict{{
			Identity:               "ident-sink-test",
			ExistingHash:           "hash-a",
			IncomingHash:           "hash-b",
			ExistingCoverage:       usageevidence.EconomicEvidenceCoverageComplete,
			ExistingCoverageReason: "orig",
			IncomingCoverage:       usageevidence.EconomicEvidenceCoveragePartial,
			IncomingCoverageReason: "disp",
		}},
		EconomicEvidenceVersion: usageevidence.EconomicEvidenceDispositionVersionV1,
	}
	disp, err := usageevidence.NewEconomicEvidenceDisposition(leg.Observations[0], usageevidence.EconomicEvidenceCoverageComplete, "canonical report")
	if err != nil {
		t.Fatal(err)
	}
	leg.EconomicDispositions = []usageevidence.EconomicEvidenceDisposition{disp}
	origLegSnapshot := leg.Clone()

	if err := sink.AppendLeg(ctx, leg); err != nil {
		t.Fatalf("AppendLeg: %v", err)
	}

	assertCallLegUsageRecordEqual(t, leg, origLegSnapshot, "sink mutated input CallLegUsageRecord")
	if len(leg.ObservationRefs) != 0 {
		t.Fatalf("sink manufactured ObservationRefs on input leg: got %d, want 0", len(leg.ObservationRefs))
	}

	sink.mu.Lock()
	if len(sink.legs) != 1 {
		sink.mu.Unlock()
		t.Fatalf("sink.legs length = %d, want 1", len(sink.legs))
	}
	capturedLeg := &sink.legs[0]
	if len(capturedLeg.ObservationRefs) != 0 {
		sink.mu.Unlock()
		t.Fatalf("sink captured manufactured ObservationRefs: got %d, want 0", len(capturedLeg.ObservationRefs))
	}
	capturedLeg.Observations[0].Scope.Roles[0] = "MUTATED_ROLE"
	capturedLeg.Observations[0].Scope.SafeClaims["tier"] = "MUTATED_CLAIM"
	capturedLeg.Observations[0].Scope.PolicyLabels["env"] = "MUTATED_LABEL"
	capturedLeg.Observations[0].Measures[0].Key.Dimensions[0].Value = "MUTATED_DIM"
	*capturedLeg.Observations[0].Measures[0].Value = metering.Decimal{Coefficient: "9999", Scale: 0}
	capturedLeg.Observations[0].Charges[0].Component.Dimensions[0].Value = "MUTATED_CHG_DIM"
	*capturedLeg.Observations[0].Charges[0].Amount = metering.Decimal{Coefficient: "9999", Scale: 0}
	capturedLeg.Observations[0].Charges[0].Covers[0].Ref.ObservationID = "MUTATED_COV"
	capturedLeg.Observations[0].Evidence[0].Lexeme = "MUTATED_LEXEME"
	capturedLeg.Observations[0].Supersedes[0].PayloadHash = "MUTATED_HASH"
	capturedLeg.EvidenceConflicts[0].Identity = "MUTATED_CONFLICT"
	capturedLeg.EconomicDispositions[0].CoverageReason = "MUTATED_DISP"
	sink.mu.Unlock()

	assertCallLegUsageRecordEqual(t, leg, origLegSnapshot, "input CallLegUsageRecord aliased by sink captured copy")

	callRec := usageevidence.CallUsageRecord{
		CallID:          callID,
		AccountID:       "acct-test",
		ALegID:          "aleg-sink-test",
		ExpectedBLegIDs: []string{"bleg-sink-test"},
	}
	origCallSnapshot := cloneRefinement51CallRecord(callRec)
	if err := sink.AppendCall(ctx, callRec); err != nil {
		t.Fatalf("AppendCall: %v", err)
	}
	assertCallUsageRecordEqual(t, callRec, origCallSnapshot, "sink mutated input CallUsageRecord")

	sink.mu.Lock()
	if len(sink.calls) != 1 {
		sink.mu.Unlock()
		t.Fatalf("sink.calls length = %d, want 1", len(sink.calls))
	}
	sink.calls[0].ExpectedBLegIDs[0] = "MUTATED_BLEG"
	sink.mu.Unlock()

	assertCallUsageRecordEqual(t, callRec, origCallSnapshot, "input CallUsageRecord aliased by sink captured copy")
}

func refinement51Executor(t *testing.T, store b2bua.Store, sink *refinement51Sink, secureSession *app.Manager) *Executor {
	t.Helper()
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingIdentity.StoreID = func(context.Context) string { return "store-test" }
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{
			AccountID:       "acct",
			CallID:          in.CallID,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billingport.ExposureOpen,
		}, nil
	})
	ex.TerminalUsageSink = sink
	ex.SecureSession = secureSession
	ex.SyntheticLocalPrincipal = true
	ex.Backends = map[string]execbackend.Backend{
		"bad1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			},
		},
		"bad2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return ParallelPreWinFailStream{}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
			FinalizeBillingV2: func(ctx context.Context, in execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
				return nonNilEconomicEvidenceFor(in), nil
			},
		},
	}
	return ex
}

func nonNilEconomicEvidenceFor(in execbackend.BillingFinalizationInput) execbackend.BillingFinalizationResult {
	now := time.Unix(1_700_000_000, 0).UTC()
	measureVal := metering.Decimal{Coefficient: "25", Scale: 0}
	chargeAmt := metering.Decimal{Coefficient: "150", Scale: 2}
	obs := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-" + in.BLegID,
		SourceEventKey: "src-" + in.BLegID,
		Revision:       1,
		StreamID:       "stream-" + in.BLegID,
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:    metering.SubjectBLeg,
			StoreID: "store-test",
			ALegID:  in.ALegID,
			BLegID:  in.BLegID,
		},
		Correlation: metering.CorrelationV2{
			StoreID:           "store-test",
			ALegID:            in.ALegID,
			BLegID:            in.BLegID,
			ProviderRequestID: "req-" + in.BLegID,
		},
		Semantics:  metering.SemanticsReplacement,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "provider.test.v1",
		Scope: scope.PrincipalScopeView{
			Roles:        []string{"role-operator", "role-billing"},
			SafeClaims:   map[string]string{"tier": "enterprise", "plan": "growth"},
			PolicyLabels: map[string]string{"env": "staging"},
		},
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction:  metering.DirectionInput,
				Component:  metering.ComponentInputToken,
				Unit:       metering.UnitToken,
				SchemaID:   metering.DefaultInclusionSchemaID,
				Dimensions: []metering.Dimension{{Name: "tier", Value: "std"}},
			},
			Value:   &measureVal,
			Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "item-" + in.BLegID,
			Component: &metering.ComponentKey{
				Direction:  metering.DirectionOutput,
				Component:  metering.ComponentOutputToken,
				Unit:       metering.UnitToken,
				SchemaID:   "provider.tokens.v2",
				Dimensions: []metering.Dimension{{Name: "class", Value: "standard"}},
			},
			Amount:   &chargeAmt,
			Currency: "USD",
			Kind:     metering.ChargeKindComponent,
			Covers: []metering.ChargeCoverageRef{{
				Ref: metering.ChargeRef{
					StoreID:       "store-test",
					ObservationID: "obs-cov-" + in.BLegID,
					Revision:      1,
					ChargeItemID:  "item-cov-" + in.BLegID,
				},
				Relation: metering.CoverageInclusive,
			}},
		}},
		Evidence: []metering.SafeEvidenceField{{
			Path:        "x-request-id",
			Lexeme:      "req-12345",
			Present:     true,
			Acquisition: metering.AcquisitionProviderResponse,
		}},
		Supersedes: []metering.ObservationRef{{
			StoreID:       "store-test",
			ObservationID: "obs-prev-" + in.BLegID,
			Revision:      1,
			PayloadHash:   "hash-prev-" + in.BLegID,
		}},
	}
	return execbackend.BillingFinalizationResult{
		Usage: aipapi.Event{Kind: aipapi.EventUsageDelta},
		EconomicEvidence: []execbackend.EconomicEvidence{
			{
				Observation:    obs,
				Coverage:       usageevidence.EconomicEvidenceCoverageComplete,
				CoverageReason: "canonical report",
			},
			{
				Observation:    obs,
				Coverage:       usageevidence.EconomicEvidenceCoveragePartial,
				CoverageReason: "conflicting partial report",
			},
		},
	}
}

func assertNonEmptyEconomicData(t *testing.T, leg usageevidence.CallLegUsageRecord) {
	t.Helper()
	if leg.EvidenceVersion < usageevidence.EvidenceFormatVersionV2 {
		t.Fatalf("leg %s evidence version = %v, want >= %v", leg.BLegID, leg.EvidenceVersion, usageevidence.EvidenceFormatVersionV2)
	}
	if len(leg.Observations) == 0 {
		t.Fatalf("leg %s has empty Observations", leg.BLegID)
	}
	var foundEconomicObs bool
	for _, obs := range leg.Observations {
		if obs.Origin != metering.OriginProvider || len(obs.Charges) == 0 {
			continue
		}
		foundEconomicObs = true
		if len(obs.Scope.Roles) == 0 {
			t.Fatalf("economic obs has empty Scope.Roles: %+v", obs)
		}
		if len(obs.Scope.SafeClaims) == 0 {
			t.Fatalf("economic obs has empty Scope.SafeClaims: %+v", obs)
		}
		if len(obs.Scope.PolicyLabels) == 0 {
			t.Fatalf("economic obs has empty Scope.PolicyLabels: %+v", obs)
		}
		if len(obs.Measures) == 0 {
			t.Fatalf("economic obs has empty Measures: %+v", obs)
		}
		for j, m := range obs.Measures {
			if len(m.Key.Dimensions) == 0 {
				t.Fatalf("economic obs measure %d has empty Dimensions", j)
			}
			if m.Value == nil {
				t.Fatalf("economic obs measure %d has nil Value", j)
			}
		}
		for j, ch := range obs.Charges {
			if ch.Component == nil || len(ch.Component.Dimensions) == 0 {
				t.Fatalf("economic obs charge %d has empty Component Dimensions", j)
			}
			if ch.Amount == nil {
				t.Fatalf("economic obs charge %d has nil Amount", j)
			}
			if len(ch.Covers) == 0 {
				t.Fatalf("economic obs charge %d has empty Covers", j)
			}
		}
		if len(obs.Evidence) == 0 {
			t.Fatalf("economic obs has empty Evidence: %+v", obs)
		}
		if len(obs.Supersedes) == 0 {
			t.Fatalf("economic obs has empty Supersedes: %+v", obs)
		}
	}
	if !foundEconomicObs {
		t.Fatalf("leg %s has no provider economic observation", leg.BLegID)
	}
	// Note: ObservationRefs are populated during post-turn billing/valuation in the
	// authoritative store/rating pipeline; runtime execution handoff (Executor.Execute)
	// emits raw Observations, EvidenceConflicts, and EconomicDispositions.
	if len(leg.EvidenceConflicts) == 0 {
		t.Fatalf("leg %s has empty EvidenceConflicts", leg.BLegID)
	}
	if len(leg.EconomicDispositions) == 0 {
		t.Fatalf("leg %s has empty EconomicDispositions", leg.BLegID)
	}
}

func refinement51Call(session aipapi.SessionRef, selector, text string) *aipapi.Call {
	return &aipapi.Call{
		Session: session,
		Route:   aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		}},
	}
}

func cloneRefinement51CallRecord(record usageevidence.CallUsageRecord) usageevidence.CallUsageRecord {
	record.ExpectedBLegIDs = append([]string(nil), record.ExpectedBLegIDs...)
	return record
}

func cloneRefinement51CallLegRecord(leg usageevidence.CallLegUsageRecord) usageevidence.CallLegUsageRecord {
	return leg.Clone()
}

func assertCallLegUsageRecordEqual(t *testing.T, got, want usageevidence.CallLegUsageRecord, msg string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got:  %#v\n want: %#v", msg, got, want)
	}
}

func assertCallUsageRecordEqual(t *testing.T, got, want usageevidence.CallUsageRecord, msg string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got:  %#v\n want: %#v", msg, got, want)
	}
}

type terminalUsageSnapshot struct {
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
}

func snapshotTerminalUsage(sink *refinement51Sink) terminalUsageSnapshot {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	calls := make([]usageevidence.CallUsageRecord, len(sink.calls))
	for i, c := range sink.calls {
		calls[i] = cloneRefinement51CallRecord(c)
	}
	legs := make([]usageevidence.CallLegUsageRecord, len(sink.legs))
	for i, l := range sink.legs {
		legs[i] = cloneRefinement51CallLegRecord(l)
	}
	return terminalUsageSnapshot{
		calls: calls,
		legs:  legs,
	}
}

func TestRefinement51CallLegUsageRecordCloneDeepImmutabilityAndMutationSensitivity(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	measureVal := metering.Decimal{Coefficient: "25", Scale: 0}
	chargeAmt := metering.Decimal{Coefficient: "150", Scale: 2}

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	obs := metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-deep-immutability",
		SourceEventKey: "src-deep-immutability",
		Revision:       1,
		StreamID:       "stream-deep",
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendEgress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       "store-test",
			ALegID:        "aleg-deep",
			BLegID:        "bleg-deep",
			BillingCallID: callID.String(),
			AttemptSeq:    1,
		},
		Correlation: metering.CorrelationV2{
			StoreID:           "store-test",
			ALegID:            "aleg-deep",
			BLegID:            "bleg-deep",
			BillingCallID:     callID.String(),
			AttemptSeq:        1,
			ProviderRequestID: "req-deep",
		},
		Semantics:  metering.SemanticsReplacement,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "provider.test.v1",
		Scope: scope.PrincipalScopeView{
			Roles:        []string{"role-operator", "role-billing"},
			SafeClaims:   map[string]string{"tier": "enterprise", "plan": "growth"},
			PolicyLabels: map[string]string{"env": "staging"},
		},
		Measures: []metering.Measure{{
			Key: metering.ComponentKey{
				Direction:  metering.DirectionInput,
				Component:  metering.ComponentInputToken,
				Unit:       metering.UnitToken,
				SchemaID:   metering.DefaultInclusionSchemaID,
				Dimensions: []metering.Dimension{{Name: "tier", Value: "std"}},
			},
			Value:   &measureVal,
			Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "item-deep",
			Component: &metering.ComponentKey{
				Direction:  metering.DirectionOutput,
				Component:  metering.ComponentOutputToken,
				Unit:       metering.UnitToken,
				SchemaID:   "provider.tokens.v2",
				Dimensions: []metering.Dimension{{Name: "class", Value: "standard"}},
			},
			Amount:   &chargeAmt,
			Currency: "USD",
			Kind:     metering.ChargeKindComponent,
			Covers: []metering.ChargeCoverageRef{{
				Ref: metering.ChargeRef{
					StoreID:       "store-test",
					ObservationID: "obs-cov-deep",
					Revision:      1,
					ChargeItemID:  "item-cov-deep",
				},
				Relation: metering.CoverageInclusive,
			}},
		}},
		Evidence: []metering.SafeEvidenceField{{
			Path:        "x-request-id",
			Lexeme:      "req-12345",
			Present:     true,
			Acquisition: metering.AcquisitionProviderResponse,
		}},
		Supersedes: []metering.ObservationRef{{
			StoreID:       "store-test",
			ObservationID: "obs-prev-deep",
			Revision:      1,
			PayloadHash:   "hash-prev-deep",
		}},
	}
	disp, err := usageevidence.NewEconomicEvidenceDisposition(obs, usageevidence.EconomicEvidenceCoverageComplete, "canonical report")
	if err != nil {
		t.Fatalf("construct disposition: %v", err)
	}

	orig := usageevidence.CallLegUsageRecord{
		CallID:                  callID,
		ALegID:                  "aleg-deep",
		BLegID:                  "bleg-deep",
		AttemptSeq:              1,
		BackendID:               "ok",
		ProviderID:              "provider",
		ModelID:                 "model",
		StartedAt:               now,
		FinishedAt:              now.Add(time.Second),
		Outcome:                 usageevidence.LegOutcomeWinner,
		Surfaced:                usageevidence.SurfacedYes,
		EvidenceVersion:         usageevidence.EvidenceFormatVersionV2,
		EvidenceProjection:      usageevidence.EvidenceProjectionV1,
		Observations:            []metering.Observation{obs},
		ObservationRefs:         []metering.ObservationRef{{StoreID: "store-test", ObservationID: "ref-deep", Revision: 1, PayloadHash: "hash-deep"}},
		EvidenceConflicts:       []usageevidence.EvidenceConflict{{Identity: "ident-deep", ExistingHash: "hash-a", IncomingHash: "hash-b", ExistingCoverage: usageevidence.EconomicEvidenceCoverageComplete, ExistingCoverageReason: "orig", IncomingCoverage: usageevidence.EconomicEvidenceCoveragePartial, IncomingCoverageReason: "disp"}},
		EconomicEvidenceVersion: usageevidence.EconomicEvidenceDispositionVersionV1,
		EconomicDispositions:    []usageevidence.EconomicEvidenceDisposition{disp},
	}

	// Verify assertNonEmptyEconomicData passes on original
	assertNonEmptyEconomicData(t, orig)

	// Clone using production leg.Clone()
	cloned := orig.Clone()
	assertCallLegUsageRecordEqual(t, cloned, orig, "initial clone must be deep equal to original")

	// Mutate every mutable slice, map, and pointer in cloned:
	cloned.Observations[0].Scope.Roles[0] = "MUTATED_ROLE"
	cloned.Observations[0].Scope.Roles = append(cloned.Observations[0].Scope.Roles, "NEW_ROLE")
	cloned.Observations[0].Scope.SafeClaims["tier"] = "MUTATED_TIER"
	cloned.Observations[0].Scope.SafeClaims["new_key"] = "new_val"
	cloned.Observations[0].Scope.PolicyLabels["env"] = "MUTATED_ENV"
	cloned.Observations[0].Scope.PolicyLabels["extra"] = "label"

	cloned.Observations[0].Measures[0].Key.Dimensions[0].Value = "MUTATED_DIM"
	cloned.Observations[0].Measures[0].Key.Dimensions = append(cloned.Observations[0].Measures[0].Key.Dimensions, metering.Dimension{Name: "new", Value: "dim"})
	*cloned.Observations[0].Measures[0].Value = metering.Decimal{Coefficient: "99999", Scale: 3}

	cloned.Observations[0].Charges[0].Component.Dimensions[0].Value = "MUTATED_COMP_DIM"
	cloned.Observations[0].Charges[0].Component.Dimensions = append(cloned.Observations[0].Charges[0].Component.Dimensions, metering.Dimension{Name: "cnew", Value: "cdim"})
	*cloned.Observations[0].Charges[0].Amount = metering.Decimal{Coefficient: "99999", Scale: 3}
	cloned.Observations[0].Charges[0].Covers[0].Ref.ObservationID = "MUTATED_COV"
	cloned.Observations[0].Charges[0].Covers = append(cloned.Observations[0].Charges[0].Covers, metering.ChargeCoverageRef{Relation: metering.CoverageAdditive})

	cloned.Observations[0].Evidence[0].Lexeme = "MUTATED_LEXEME"
	cloned.Observations[0].Evidence = append(cloned.Observations[0].Evidence, metering.SafeEvidenceField{Path: "x-new"})

	cloned.Observations[0].Supersedes[0].PayloadHash = "MUTATED_HASH"
	cloned.Observations[0].Supersedes = append(cloned.Observations[0].Supersedes, metering.ObservationRef{ObservationID: "new-sup"})

	cloned.ObservationRefs[0].PayloadHash = "MUTATED_REF_HASH"
	cloned.ObservationRefs = append(cloned.ObservationRefs, metering.ObservationRef{ObservationID: "new-ref"})

	cloned.EvidenceConflicts[0].Identity = "MUTATED_CONFLICT"
	cloned.EvidenceConflicts = append(cloned.EvidenceConflicts, usageevidence.EvidenceConflict{Identity: "new-conflict"})

	cloned.EconomicDispositions[0].CoverageReason = "MUTATED_DISP"
	cloned.EconomicDispositions = append(cloned.EconomicDispositions, disp)

	cloned.Observations = append(cloned.Observations, obs)

	// Verify original was completely untouched by ANY of the clone mutations:
	if orig.Observations[0].Scope.Roles[0] != "role-operator" || len(orig.Observations[0].Scope.Roles) != 2 {
		t.Fatalf("original Scope.Roles mutated: %v", orig.Observations[0].Scope.Roles)
	}
	if orig.Observations[0].Scope.SafeClaims["tier"] != "enterprise" || len(orig.Observations[0].Scope.SafeClaims) != 2 {
		t.Fatalf("original Scope.SafeClaims mutated: %v", orig.Observations[0].Scope.SafeClaims)
	}
	if orig.Observations[0].Scope.PolicyLabels["env"] != "staging" || len(orig.Observations[0].Scope.PolicyLabels) != 1 {
		t.Fatalf("original Scope.PolicyLabels mutated: %v", orig.Observations[0].Scope.PolicyLabels)
	}
	if orig.Observations[0].Measures[0].Key.Dimensions[0].Value != "std" || len(orig.Observations[0].Measures[0].Key.Dimensions) != 1 {
		t.Fatalf("original Measure.Key.Dimensions mutated: %v", orig.Observations[0].Measures[0].Key.Dimensions)
	}
	if orig.Observations[0].Measures[0].Value.Coefficient != "25" || orig.Observations[0].Measures[0].Value.Scale != 0 {
		t.Fatalf("original Measure.Value pointer mutated: %+v", orig.Observations[0].Measures[0].Value)
	}
	if orig.Observations[0].Charges[0].Component.Dimensions[0].Value != "standard" || len(orig.Observations[0].Charges[0].Component.Dimensions) != 1 {
		t.Fatalf("original Charge.Component.Dimensions mutated: %v", orig.Observations[0].Charges[0].Component.Dimensions)
	}
	if orig.Observations[0].Charges[0].Amount.Coefficient != "150" || orig.Observations[0].Charges[0].Amount.Scale != 2 {
		t.Fatalf("original Charge.Amount pointer mutated: %+v", orig.Observations[0].Charges[0].Amount)
	}
	if orig.Observations[0].Charges[0].Covers[0].Ref.ObservationID != "obs-cov-deep" || len(orig.Observations[0].Charges[0].Covers) != 1 {
		t.Fatalf("original Charge.Covers mutated: %v", orig.Observations[0].Charges[0].Covers)
	}
	if orig.Observations[0].Evidence[0].Lexeme != "req-12345" || len(orig.Observations[0].Evidence) != 1 {
		t.Fatalf("original Evidence mutated: %v", orig.Observations[0].Evidence)
	}
	if orig.Observations[0].Supersedes[0].PayloadHash != "hash-prev-deep" || len(orig.Observations[0].Supersedes) != 1 {
		t.Fatalf("original Supersedes mutated: %v", orig.Observations[0].Supersedes)
	}
	if orig.ObservationRefs[0].PayloadHash != "hash-deep" || len(orig.ObservationRefs) != 1 {
		t.Fatalf("original ObservationRefs mutated: %v", orig.ObservationRefs)
	}
	if orig.EvidenceConflicts[0].Identity != "ident-deep" || len(orig.EvidenceConflicts) != 1 {
		t.Fatalf("original EvidenceConflicts mutated: %v", orig.EvidenceConflicts)
	}
	if orig.EconomicDispositions[0].CoverageReason != "canonical report" || len(orig.EconomicDispositions) != 1 {
		t.Fatalf("original EconomicDispositions mutated: %v", orig.EconomicDispositions)
	}
	if len(orig.Observations) != 1 {
		t.Fatalf("original Observations slice length mutated: %d", len(orig.Observations))
	}
}
