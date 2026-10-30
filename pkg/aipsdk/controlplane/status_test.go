package controlplane_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestCapabilityStateConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.CapabilityState]string{
		controlplane.CapabilityDisabled:    "disabled",
		controlplane.CapabilityReady:       "ready",
		controlplane.CapabilityDegraded:    "degraded",
		controlplane.CapabilityUnavailable: "unavailable",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("capability state drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("capability state %q must be known", got)
		}
	}
}

func TestRecordingPolicyConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.RecordingPolicy]string{
		controlplane.RecordingDisabled:        "disabled",
		controlplane.RecordingBestEffort:      "best_effort",
		controlplane.RecordingRequiredPreWork: "required_pre_work",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("recording policy drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("recording policy %q must be known", got)
		}
	}
}

func TestErrorCodeConstantsAreStable(t *testing.T) {
	t.Parallel()
	want := map[controlplane.ErrorCode]string{
		controlplane.ErrCodeDisabled:          "disabled",
		controlplane.ErrCodeUnavailable:       "unavailable",
		controlplane.ErrCodeDegraded:          "degraded",
		controlplane.ErrCodeInvalidQuery:      "invalid_query",
		controlplane.ErrCodeTooBroad:          "too_broad",
		controlplane.ErrCodeUnsupportedFilter: "unsupported_filter",
		controlplane.ErrCodeUnsafeEvidence:    "unsafe_evidence",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("error code drift: got %q want %q", got, w)
		}
	}
}

func TestCapabilityStatusJSONRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := controlplane.CapabilityStatus{
		State:           controlplane.CapabilityDegraded,
		Reason:          controlplane.ReasonRecordingFailure,
		LastFailureAt:   now,
		RecordingPolicy: controlplane.RecordingBestEffort,
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back controlplane.CapabilityStatus
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.State != controlplane.CapabilityDegraded {
		t.Fatalf("state lost: %q", back.State)
	}
	if back.Reason != controlplane.ReasonRecordingFailure {
		t.Fatalf("reason lost: %q", back.Reason)
	}
	if !back.LastFailureAt.Equal(now) {
		t.Fatalf("last failure time lost: %v vs %v", back.LastFailureAt, now)
	}
}

func TestReasonCodeConstantsAreStable(t *testing.T) {
	t.Parallel()
	cases := map[controlplane.ReasonCode]string{
		controlplane.ReasonRecordingFailure:   "recording_failure",
		controlplane.ReasonQueryFailure:       "query_failure",
		controlplane.ReasonRetentionFailure:   "retention_failure",
		controlplane.ReasonRedactionFailure:   "redaction_failure",
		controlplane.ReasonBackingUnavailable: "backing_unavailable",
		controlplane.ReasonStoreNotReady:      "store_not_ready",
		controlplane.ReasonDisabled:           "disabled",
		controlplane.ReasonUnsupported:        "unsupported",
	}
	for got, w := range cases {
		if string(got) != w {
			t.Fatalf("reason code drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("reason code %q must be known", got)
		}
	}
}

func TestServiceInterfacesCompile(t *testing.T) {
	t.Parallel()
	_ = controlplane.Recorder(nil)
	_ = controlplane.Queries(nil)
}
