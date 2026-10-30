package diag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
)

// genericPortRow builds one finished compiled-metadata row the way the
// featurehost inventory authority would: diagnostics only consumes it.
func genericPortRow(instanceID string, attemptHandlers, streamHandlers []string) InventoryFeatureMetadata {
	row := InventoryFeatureMetadata{
		InstanceID:     instanceID,
		StageOccupancy: []InventoryStageOccupancy{},
	}
	if len(attemptHandlers) > 0 {
		row.StageOccupancy = append(row.StageOccupancy, InventoryStageOccupancy{
			StageID:    extensions.StageCandidateAttemptTransform,
			HandlerIDs: attemptHandlers,
			Count:      len(attemptHandlers),
		})
	}
	if len(streamHandlers) > 0 {
		row.StageOccupancy = append(row.StageOccupancy, InventoryStageOccupancy{
			StageID:    extensions.StageFinalStreamObservation,
			HandlerIDs: streamHandlers,
			Count:      len(streamHandlers),
		})
	}
	return row
}

func TestBuildInventoryExtensions_genericPortAggregatePosture(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feat-a", Enabled: true},
				{ID: "feat-b", Enabled: true},
			},
		},
	}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{
			genericPortRow("feat-a", []string{"attempt_transform:at-a"}, []string{"stream_observer:so-a", "stream_observer:so-b"}),
			genericPortRow("feat-b", []string{"attempt_transform:at-b"}, []string{"stream_observer:so-c", "stream_observer:so-d"}),
		},
	})
	if !ext.GenericPorts.AttemptTransformOccupied || ext.GenericPorts.AttemptTransformHandlers != 2 {
		t.Fatalf("attempt posture=%#v", ext.GenericPorts)
	}
	if !ext.GenericPorts.FinalStreamObservationOccupied || ext.GenericPorts.FinalStreamObservationHandlers != 4 {
		t.Fatalf("observer posture=%#v", ext.GenericPorts)
	}
	var atOcc, soOcc bool
	for _, f := range ext.Features {
		for _, occ := range f.StageOccupancy {
			switch occ.StageID {
			case extensions.StageCandidateAttemptTransform:
				atOcc = true
			case extensions.StageFinalStreamObservation:
				soOcc = true
			}
		}
	}
	if !atOcc || !soOcc {
		t.Fatalf("per-feature occupancy missing at=%v so=%v", atOcc, soOcc)
	}
}

func TestBuildInventoryExtensions_absentPortsZeroPosture(t *testing.T) {
	t.Parallel()
	ext := buildInventoryExtensions(context.Background(), &config.Config{}, nil)
	if ext.GenericPorts.AttemptTransformOccupied || ext.GenericPorts.AttemptTransformHandlers != 0 {
		t.Fatalf("absent attempt posture=%#v", ext.GenericPorts)
	}
	if ext.GenericPorts.FinalStreamObservationOccupied || ext.GenericPorts.FinalStreamObservationHandlers != 0 {
		t.Fatalf("absent observer posture=%#v", ext.GenericPorts)
	}
}

func TestBuildInventoryExtensions_genericPortPosturePrivacyNoSensitiveFields(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "feat-priv", Enabled: true}},
		},
	}
	ext := buildInventoryExtensions(context.Background(), cfg, &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{
			genericPortRow("feat-priv", []string{"attempt_transform:keep"}, []string{"stream_observer:keep"}),
		},
	})
	raw, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		"reasoning_text",
		"session_partition",
		"prompt_excerpt",
		`"anchor"`,
		`"payload"`,
		`"opaque"`,
		`"signature"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("inventory JSON must not contain %q; body=%s", forbidden, body)
		}
	}
}
