package usageevidence

import (
	"errors"
	"strings"
	"testing"
)

func TestJoinCompleteCallIndependentOfAppendOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		steps []string
	}{
		{name: "legs_first", steps: []string{"leg:b-fail", "leg:b-win", "closure"}},
		{name: "closure_first", steps: []string{"closure", "leg:b-fail", "leg:b-win"}},
		{name: "interleaved", steps: []string{"leg:b-fail", "closure", "leg:b-win"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			callID := mustBillingCallID(t)
			src := testCallUsageRecord(callID)
			src.ExpectedBLegIDs = []string{"b-win", "b-fail"}
			var closure *CallUsageRecord
			var legs []CallLegUsageRecord
			for i, step := range tc.steps {
				switch {
				case step == "closure":
					sealed, err := src.Seal()
					if err != nil {
						t.Fatalf("seal closure: %v", err)
					}
					closure = &sealed
				case strings.HasPrefix(step, "leg:"):
					sealed, err := testCallLegUsageRecord(callID, strings.TrimPrefix(step, "leg:")).Seal()
					if err != nil {
						t.Fatalf("seal %s: %v", step, err)
					}
					legs = append(legs, sealed)
				default:
					t.Fatalf("unknown step %q", step)
				}
				if closure == nil {
					continue
				}
				got, err := JoinCompleteCall(*closure, legs)
				last := i == len(tc.steps)-1
				if !last {
					if !errors.Is(err, ErrCallIncomplete) {
						t.Fatalf("step %d (%s) join = %v, want ErrCallIncomplete", i, step, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("complete join after %s: %v", tc.name, err)
				}
				if got.Closure.Key != callID.String() {
					t.Fatalf("complete call key = %q, want BillingCallID", got.Closure.Key)
				}
				if len(got.Legs) != 2 {
					t.Fatalf("complete legs = %d, want 2", len(got.Legs))
				}
				seen := map[string]struct{}{}
				for _, leg := range got.Legs {
					seen[leg.BLegID] = struct{}{}
					if leg.CallID != callID {
						t.Fatal("joined legs must share the call BillingCallID")
					}
				}
				if _, ok := seen["b-fail"]; !ok {
					t.Fatal("missing b-fail")
				}
				if _, ok := seen["b-win"]; !ok {
					t.Fatal("missing b-win")
				}
			}
		})
	}
}

func TestJoinCompleteCallIgnoresUnexpectedLegsAndRejectsInvalidRows(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	src := testCallUsageRecord(callID)
	src.ExpectedBLegIDs = []string{"b-1"}
	closure, err := src.Seal()
	if err != nil {
		t.Fatal(err)
	}
	want, err := testCallLegUsageRecord(callID, "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	extra, err := testCallLegUsageRecord(callID, "b-extra").Seal()
	if err != nil {
		t.Fatal(err)
	}
	otherCall, err := testCallLegUsageRecord(mustBillingCallID(t), "b-1").Seal()
	if err != nil {
		t.Fatal(err)
	}
	invalid := testCallLegUsageRecord(callID, "b-1")
	invalid.FinishedAt = invalid.StartedAt.Add(-1)
	got, err := JoinCompleteCall(closure, []CallLegUsageRecord{extra, invalid, otherCall, want})
	if err != nil {
		t.Fatalf("unexpected extra/invalid rows must not block a complete call: %v", err)
	}
	if len(got.Legs) != 1 || got.Legs[0].BLegID != "b-1" {
		t.Fatalf("joined legs = %+v", got.Legs)
	}
}

func TestJoinCompleteCallEmptyExpectedSetIsComplete(t *testing.T) {
	t.Parallel()
	src := testCallUsageRecord(mustBillingCallID(t))
	src.ExpectedBLegIDs = nil
	closure, err := src.Seal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := JoinCompleteCall(closure, nil)
	if err != nil {
		t.Fatalf("empty expected set: %v", err)
	}
	if len(got.Legs) != 0 {
		t.Fatalf("legs = %d, want 0", len(got.Legs))
	}
}

func TestJoinCompleteCallDoesNotCrossSubmissionIdentity(t *testing.T) {
	t.Parallel()
	callID := mustBillingCallID(t)
	closureSource := testCallUsageRecord(callID)
	closureSource.SubmissionID = "submission-1"
	closureSource.ExpectedBLegIDs = []string{"b-1"}
	closure, err := closureSource.Seal()
	if err != nil {
		t.Fatal(err)
	}
	foreign := testCallLegUsageRecord(callID, "b-1")
	foreign.SubmissionID = "submission-2"
	sealedForeign, err := foreign.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := JoinCompleteCall(closure, []CallLegUsageRecord{sealedForeign}); !errors.Is(err, ErrCallIncomplete) {
		t.Fatalf("foreign submission join error = %v, want ErrCallIncomplete", err)
	}
}
