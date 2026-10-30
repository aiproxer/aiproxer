package configreload_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestStatus_NoFixedSourcePathField(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[sdkreload.Status]()
	if _, ok := rt.FieldByName("FixedSourcePath"); ok {
		t.Fatal("canonical Status must not expose FixedSourcePath")
	}
	st := sdkreload.Status{ActiveGeneration: 3, Busy: true}
	if st.ActiveGeneration != 3 || !st.Busy {
		t.Fatalf("status fields broken: %+v", st)
	}
}

func TestStatusHistory_SanitizesActorAndUsesCanonicalEntry(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(2)
	h.Append(sdkreload.HistoryEntry{
		AttemptID:  1,
		Trigger:    sdkreload.TriggerAPI,
		Category:   sdkreload.ResultPublished,
		SafeActor:  "password=super-secret-value",
		Stage:      configreload.StagePublish,
		RecordedAt: time.Unix(1, 0).UTC(),
	})
	snap := h.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].SafeActor == "password=super-secret-value" {
		t.Fatal("expected secret-looking actor to be redacted")
	}
	if snap[0].Trigger != sdkreload.TriggerAPI {
		t.Fatalf("trigger=%q", snap[0].Trigger)
	}
}

func TestStatusHistory_BoundedRing(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(3)
	for i := int64(1); i <= 5; i++ {
		h.Append(sdkreload.HistoryEntry{
			AttemptID:        i,
			Trigger:          sdkreload.TriggerAPI,
			Stage:            configreload.StagePublish,
			Category:         sdkreload.ResultPublished,
			ActiveGeneration: i,
			DurationMs:       i * 10,
			RecordedAt:       time.Unix(i, 0).UTC(),
		})
	}
	snap := h.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot len=%d want 3", len(snap))
	}
	if snap[0].AttemptID != 3 || snap[2].AttemptID != 5 {
		t.Fatalf("ring order=%v want attempts 3..5", []int64{snap[0].AttemptID, snap[1].AttemptID, snap[2].AttemptID})
	}
}

func TestStatusHistory_ZeroCapacityDefaults(t *testing.T) {
	t.Parallel()
	h := configreload.NewStatusHistory(0)
	h.Append(sdkreload.HistoryEntry{AttemptID: 1, Category: sdkreload.ResultNoop})
	if len(h.Snapshot()) != 1 {
		t.Fatalf("default capacity should accept at least one entry")
	}
}
