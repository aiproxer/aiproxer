package configreload_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

func TestReloadabilityClassify_ModelAliasChange_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.ModelAliases = []config.ModelAliasConfig{{Pattern: `^cheap$`, Replacement: "newbe:m"}}

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("alias change must be reloadable: %v", err)
	}
	if !containsChange(changes, "model_aliases", configreload.ChangeReloadable) {
		t.Fatalf("want model_aliases reloadable, got %#v", changes)
	}
}

func TestReloadabilityClassify_OverrideAdminEnabled_Reloadable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		path string
		mut  func(*config.Config)
	}{
		{
			name: "enabled",
			path: "routing.override_admin.enabled",
			mut:  func(c *config.Config) { c.Routing.OverrideAdmin.Enabled = true },
		},
		{
			name: "pathPrefix",
			path: "routing.override_admin.path_prefix",
			mut:  func(c *config.Config) { c.Routing.OverrideAdmin.PathPrefix = "/admin/routing-overrides" },
		},
		{
			name: "maxBodyBytes",
			path: "routing.override_admin.max_body_bytes",
			mut:  func(c *config.Config) { c.Routing.OverrideAdmin.MaxBodyBytes = 4096 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			active := baseConfig()
			candidate := baseConfig()
			tc.mut(candidate)
			changes, err := configreload.Classify(active, candidate)
			if err != nil {
				t.Fatalf("override_admin %s must be reloadable: %v", tc.name, err)
			}
			if !containsChange(changes, tc.path, configreload.ChangeReloadable) {
				t.Fatalf("want %s reloadable, got %#v", tc.path, changes)
			}
		})
	}
}
