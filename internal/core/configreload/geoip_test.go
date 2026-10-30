package configreload

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestClassifyGeoIPPolicyFieldsReloadable(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	candidate.Access.GeoIP.Enabled = true
	candidate.Access.GeoIP.Order = "deny_allow"
	candidate.Access.GeoIP.Deny.Countries = []string{"RU"}
	candidate.Access.GeoIP.ClientIP.Source = "direct"

	changes, err := Classify(active, candidate)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := map[string]bool{
		"access.geoip.enabled":          false,
		"access.geoip.order":            false,
		"access.geoip.deny.countries":   false,
		"access.geoip.client_ip.source": false,
	}
	for _, change := range changes {
		if change.Disposition != ChangeReloadable {
			t.Fatalf("change %q disposition = %q, want reloadable", change.Path, change.Disposition)
		}
		if _, ok := want[change.Path]; ok {
			want[change.Path] = true
		}
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("missing reloadable change %q (changes=%+v)", path, changes)
		}
	}
}

func TestClassifyGeoIPDatabaseFieldsRestartRequired(t *testing.T) {
	t.Parallel()

	active := &config.Config{}
	candidate := &config.Config{}
	candidate.Access.GeoIP.Database.Source = "managed"
	candidate.Access.GeoIP.Database.Directory = "/var/lib/aip/geoip"
	candidate.Access.GeoIP.Database.Update.Interval = "24h"

	_, err := Classify(active, candidate)
	if err == nil {
		t.Fatal("Classify succeeded, want restart-required error")
	}
	restart, ok := err.(*RestartRequiredError)
	if !ok {
		t.Fatalf("error type = %T, want *RestartRequiredError", err)
	}
	for _, path := range []string{
		"access.geoip.database.source",
		"access.geoip.database.directory",
		"access.geoip.database.update.interval",
	} {
		if !slices.Contains(restart.RestartRequiredFields, path) {
			t.Errorf("restart fields %v missing %q", restart.RestartRequiredFields, path)
		}
	}
}
