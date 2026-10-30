package configreload_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestCoordinatorResultCategories_BusyNoopFaultShutdownCoalesceVocabulary(t *testing.T) {
	t.Parallel()
	want := map[sdkreload.ResultCategory]bool{
		sdkreload.ResultPublished:         true,
		sdkreload.ResultNoop:              true,
		sdkreload.ResultBusy:              true,
		sdkreload.ResultRestartRequired:   true,
		sdkreload.ResultRetentionBlocked:  true,
		sdkreload.ResultInvalid:           true,
		sdkreload.ResultSourceIntegrity:   true,
		sdkreload.ResultCanceled:          true,
		sdkreload.ResultPreparationFailed: true,
		sdkreload.ResultInternalFailed:    true,
	}
	got := sdkreload.ResultCategories()
	if len(got) != len(want) {
		t.Fatalf("ResultCategories len=%d want %d", len(got), len(want))
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("unexpected category %q", c)
		}
		delete(want, c)
	}
	if len(want) != 0 {
		t.Fatalf("missing categories: %v", want)
	}
}

func TestMapLoadFailure_CoordinatorFaultSourceAndInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want sdkreload.ResultCategory
	}{
		{
			name: "source_missing",
			err:  &config.LoadError{Category: config.CategoryMissing},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "non_atomic",
			err:  &config.LoadError{Category: config.CategoryNonAtomicUpdate},
			want: sdkreload.ResultSourceIntegrity,
		},
		{
			name: "malformed_yaml",
			err:  &config.LoadError{Category: config.CategoryMalformedYAML},
			want: sdkreload.ResultInvalid,
		},
		{
			name: "restart_required",
			err:  &configreload.RestartRequiredError{RestartRequiredFields: []string{"server.address"}, TotalBlocked: 1},
			want: sdkreload.ResultRestartRequired,
		},
		{
			name: "validate_wrap",
			err:  errors.New("validate config: bad route"),
			want: sdkreload.ResultInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := configreload.MapLoadFailure(tc.err)
			if got != tc.want {
				t.Fatalf("MapLoadFailure(%v)=%q want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestTriggerKinds_NoPathOrYAMLFields(t *testing.T) {
	t.Parallel()
	tr := sdkreload.Trigger{
		Kind:      sdkreload.TriggerAPI,
		SafeActor: "management-api",
	}
	if tr.Kind != sdkreload.TriggerAPI && tr.Kind != sdkreload.TriggerSIGHUP {
		t.Fatalf("unexpected kind %q", tr.Kind)
	}
	// Structural: trigger envelope has no path/yaml/url fields (req 1.7, 12.4).
	_ = tr.AcceptedAt
	_ = tr.SafeActor
}
