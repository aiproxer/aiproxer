package protocolstate

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func fingerprintInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:     terminaldecision.CandidateCauseNormal,
			Reference: "cand-ref",
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "req-1",
			TraceID:   "trace-1",
			ALegID:    "aleg-1",
			BLegID:    "bleg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "rev-1",
			MaxContinuationAttempts: 3,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "traj-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "ship the release",
			RecentText:    "recent transcript",
			CandidateText: "candidate answer",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{
				{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, Name: "read_file"},
				{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusInProgress, Name: "write_file"},
			},
			ActionCount:                2,
			ExplicitCompletion:         false,
			ExplicitCompletionExpected: true,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "lineage-traj",
				ParentRef:     "lineage-parent",
				ProgressRef:   "lineage-progress",
				Attempt:       2,
			},
		},
		Deadline: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

func TestFingerprintIsCanonicalDigest(t *testing.T) {
	t.Parallel()

	got := Fingerprint(fingerprintInput())
	if len(got) != len("sha256:")+64 {
		t.Fatalf("fingerprint %q has unexpected length", got)
	}
	for i := len("sha256:"); i < len(got); i++ {
		c := got[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("fingerprint %q is not lowercase hex at %d", got, i)
		}
	}
	if again := Fingerprint(fingerprintInput()); again != got {
		t.Fatalf("fingerprint is not deterministic: %q then %q", got, again)
	}
}

func TestFingerprintIncludedDimensionsChangeDigest(t *testing.T) {
	t.Parallel()

	base := Fingerprint(fingerprintInput())
	cases := map[string]func(*terminaldecision.Input){
		"cause":             func(in *terminaldecision.Input) { in.Candidate.Cause = terminaldecision.CandidateCauseTransport },
		"explicit_expected": func(in *terminaldecision.Input) { in.Evidence.ExplicitCompletionExpected = false },
		"explicit_observed": func(in *terminaldecision.Input) { in.Evidence.ExplicitCompletion = true },
		"objective":         func(in *terminaldecision.Input) { in.Evidence.Objective = "ship the hotfix" },
		"candidate":         func(in *terminaldecision.Input) { in.Evidence.CandidateText = "different answer" },
		"recent":            func(in *terminaldecision.Input) { in.Evidence.RecentText = "other transcript" },
		"action_kind":       func(in *terminaldecision.Input) { in.Evidence.Actions[0].Kind = aipapi.ItemKindToolResult },
		"action_status":     func(in *terminaldecision.Input) { in.Evidence.Actions[1].Status = aipapi.ItemStatusCompleted },
		"action_name":       func(in *terminaldecision.Input) { in.Evidence.Actions[0].Name = "write_file" },
		"action_count":      func(in *terminaldecision.Input) { in.Evidence.ActionCount = 1 },
		"active_action_order": func(in *terminaldecision.Input) {
			in.Evidence.Actions[0], in.Evidence.Actions[1] = in.Evidence.Actions[1], in.Evidence.Actions[0]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := fingerprintInput()
			mutate(&in)
			if got := Fingerprint(in); got == base {
				t.Fatalf("fingerprint ignored included dimension %q", name)
			}
		})
	}
}

func TestFingerprintNormalizesEquivalentText(t *testing.T) {
	t.Parallel()

	base := Fingerprint(fingerprintInput())
	in := fingerprintInput()
	in.Evidence.Objective = "\r\n  ship the release \t\r"
	in.Evidence.RecentText = "recent transcript\r"
	in.Evidence.CandidateText = "\r\ncandidate answer\n"
	in.Evidence.Actions[0].Name = "  read_file\r\n"
	if got := Fingerprint(in); got != base {
		t.Fatalf("normalization changed fingerprint: %q != %q", got, base)
	}
}

func TestFingerprintExcludesVolatileDimensions(t *testing.T) {
	t.Parallel()

	base := Fingerprint(fingerprintInput())
	cases := map[string]func(*terminaldecision.Input){
		"candidate_reference":  func(in *terminaldecision.Input) { in.Candidate.Reference = "other-cand" },
		"output_committed":     func(in *terminaldecision.Input) { in.Candidate.OutputCommitted = true },
		"request_id":           func(in *terminaldecision.Input) { in.Request.RequestID = "req-2" },
		"trace_id":             func(in *terminaldecision.Input) { in.Request.TraceID = "trace-2" },
		"aleg_id":              func(in *terminaldecision.Input) { in.Request.ALegID = "aleg-2" },
		"bleg_id":              func(in *terminaldecision.Input) { in.Request.BLegID = "bleg-2" },
		"policy_revision":      func(in *terminaldecision.Input) { in.Policy.Revision = "rev-2" },
		"platform_cap":         func(in *terminaldecision.Input) { in.Policy.MaxContinuationAttempts = 9 },
		"trajectory_ref":       func(in *terminaldecision.Input) { in.Continuation.TrajectoryRef = "traj-2" },
		"continuation_attempt": func(in *terminaldecision.Input) { in.Continuation.Attempt = 4 },
		"lineage":              func(in *terminaldecision.Input) { in.Evidence.Lineage = terminaldecision.EvidenceLineage{Attempt: 7} },
		"deadline":             func(in *terminaldecision.Input) { in.Deadline = in.Deadline.Add(time.Hour) },
		"inactive_action_fact": func(in *terminaldecision.Input) {
			in.Evidence.Actions[5] = terminaldecision.ActionFact{ItemID: "i", CallID: "c", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, Name: "unused"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			in := fingerprintInput()
			mutate(&in)
			if got := Fingerprint(in); got != base {
				t.Fatalf("fingerprint included volatile dimension %q", name)
			}
		})
	}
}

// TestFingerprintIgnoresUnusedActionCapacity exercises the pure helper in
// isolation. It does not claim that an Input with populated inactive facts is
// valid in production: terminaldecision.ValidateInput rejects that shape, and
// this package must not weaken it.
func TestFingerprintIgnoresUnusedActionCapacity(t *testing.T) {
	t.Parallel()

	in := fingerprintInput()
	in.Evidence.Actions[6] = terminaldecision.ActionFact{
		ItemID: "unused-item",
		CallID: "unused-call",
		Kind:   aipapi.ItemKindToolResult,
		Status: aipapi.ItemStatusCompleted,
		Name:   "unused_tool",
	}
	if got, want := Fingerprint(in), Fingerprint(fingerprintInput()); got != want {
		t.Fatalf("unused action capacity changed fingerprint: %q != %q", got, want)
	}
}

func TestFingerprintHandlesInvalidActionCountWithoutPanic(t *testing.T) {
	t.Parallel()

	in := fingerprintInput()
	in.Evidence.ActionCount = terminaldecision.MaxEvidenceActions + 50
	if got := Fingerprint(in); got == "" {
		t.Fatal("fingerprint returned empty digest for overflowing action count")
	}
}
