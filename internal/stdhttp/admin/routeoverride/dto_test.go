package routeoverride_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	adminov "github.com/aiproxer/aiproxer/internal/stdhttp/admin/routeoverride"
)

func TestStateDTO_omitsSelectorWhenInactive(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0).UTC()
	cases := []struct {
		name          string
		state         routeoverride.State
		wantSelector  bool
		wantUpdatedAt bool
	}{
		{
			name:          "revision0",
			state:         routeoverride.Inactive("a_1"),
			wantSelector:  false,
			wantUpdatedAt: false,
		},
		{
			name: "clearedTombstone",
			state: routeoverride.State{
				ALegID:    "a_1",
				Active:    false,
				Revision:  3,
				UpdatedAt: now,
			},
			wantSelector:  false,
			wantUpdatedAt: true,
		},
		{
			name: "active",
			state: routeoverride.State{
				ALegID:    "a_1",
				Active:    true,
				Selector:  "openai:gpt-4",
				Revision:  2,
				UpdatedAt: now,
			},
			wantSelector:  true,
			wantUpdatedAt: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(adminov.StateToDTO(tc.state))
			if err != nil {
				t.Fatal(err)
			}
			s := string(raw)
			if tc.wantSelector != strings.Contains(s, `"selector"`) {
				t.Fatalf("selector presence=%v in %s", tc.wantSelector, s)
			}
			if tc.wantUpdatedAt != strings.Contains(s, `"updated_at"`) {
				t.Fatalf("updated_at presence=%v in %s", tc.wantUpdatedAt, s)
			}
			if !tc.wantSelector && strings.Contains(s, "openai:gpt-4") {
				t.Fatalf("inactive DTO leaked selector: %s", s)
			}
		})
	}
}
