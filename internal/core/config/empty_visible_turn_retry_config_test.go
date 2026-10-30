package config_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/stretchr/testify/require"
)

// Gate-section claims (spec empty-visible-turn-retry, requirements 1.1 and
// 1.3; design "Configuration"): an omitted or disabled section resolves to
// disabled on the shared effective-load path, an enabled one resolves to
// enabled, and an unknown key inside the section is rejected at load.
func TestEmptyVisibleTurnRetryConfig_EffectiveResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "omitted_section", raw: "server:\n  address: \"127.0.0.1:0\"\n", want: false},
		{name: "explicitly_disabled", raw: "empty_visible_turn_retry:\n  enabled: false\n", want: false},
		{name: "explicitly_enabled", raw: "empty_visible_turn_retry:\n  enabled: true\n", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eff, err := config.LoadEffective(context.Background(), []byte(tc.raw), config.LoadEffectiveOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.want, config.EffectiveEmptyVisibleTurnRetry(eff.Config).Enabled, "effective gate")
		})
	}
	require.False(t, config.EffectiveEmptyVisibleTurnRetry(nil).Enabled, "nil config must resolve to the disabled default")
}

func TestEmptyVisibleTurnRetryConfig_UnknownKeyRejected(t *testing.T) {
	t.Parallel()
	_, category, err := config.StrictDecode([]byte("empty_visible_turn_retry:\n  enabled: true\n  max_withheld_events: 4\n"))
	require.Error(t, err, "unknown key inside the gate section must be rejected at decode")
	require.Equal(t, config.CategoryUnknownCoreField, category)
	require.ErrorContains(t, err, "max_withheld_events")
}
