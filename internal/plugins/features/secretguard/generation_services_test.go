package secretguard

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGenerationServices_BuildsIndependentImmutableScanner(t *testing.T) {
	t.Parallel()

	build := func(confidence string, decodeDepth, workers int) *GenerationServices {
		services, err := BuildGenerationServices(DetectorPolicy{
			BetterLeaks: BetterLeaksPolicy{
				Enabled:           true,
				MinimumConfidence: confidence,
				MaxDecodeDepth:    decodeDepth,
				Workers:           workers,
				MaxFindings:       DefaultBetterLeaksMaxFindings,
			},
		}, engine.NewDisabledSource())
		require.NoError(t, err)
		require.NotNil(t, services)
		return services
	}

	first := build("medium", 1, 1)
	second := build("high", 2, 2)

	// Each generation owns the effective policy it was composed with: building
	// another generation must never republish the first generation's facts.
	firstFacts := first.DetectorFacts()
	secondFacts := second.DetectorFacts()
	assert.Equal(t, "medium", firstFacts.MinimumConfidence)
	assert.Equal(t, 1, firstFacts.MaxDecodeDepth)
	assert.Equal(t, 1, firstFacts.Workers)
	assert.Equal(t, "high", secondFacts.MinimumConfidence)
	assert.Equal(t, 2, secondFacts.MaxDecodeDepth)
	assert.Equal(t, 2, secondFacts.Workers)

	assert.Equal(t, betterLeaksPinnedVersion, firstFacts.Version)
	assert.NotEmpty(t, firstFacts.ConfigHash)
	assert.NotEmpty(t, firstFacts.RuleInventoryHash)
	assert.Greater(t, firstFacts.ActiveRuleCount, 0)
	assert.NotEmpty(t, firstFacts.RuleIDs)

	firstFacts.RuleIDs[0] = "mutated"
	assert.NotEqual(t, "mutated", first.DetectorFacts().RuleIDs[0], "policy facts must be defensively copied")
}

func TestBuildGenerationServices_DisabledSkipsScannerConstruction(t *testing.T) {
	t.Parallel()

	// Tuning values that an enabled detector policy rejects. Disabling the
	// detector must not validate or construct a scanner, so stale tuning values
	// can never fail startup.
	disabled := DetectorPolicy{BetterLeaks: BetterLeaksPolicy{
		Enabled:           false,
		MinimumConfidence: "unsupported",
		MaxDecodeDepth:    MaxBetterLeaksDecodeDepth + 1,
		Workers:           MaxBetterLeaksWorkers + 1,
		DisableRules:      []string{"no-such-betterleaks-rule"},
		MaxFindings:       DefaultBetterLeaksMaxFindings,
	}}
	services, err := BuildGenerationServices(disabled, engine.NewDisabledSource())
	require.NoError(t, err)
	require.NotNil(t, services)
	assert.Zero(t, services.DetectorFacts())

	// Positive control: the same tuning must fail composition once enabled.
	enabled := disabled
	enabled.BetterLeaks.Enabled = true
	_, err = BuildGenerationServices(enabled, engine.NewDisabledSource())
	require.Error(t, err, "enabled detector policy with invalid tuning must fail generation composition")
}
