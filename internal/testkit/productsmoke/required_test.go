//go:build productsmoke

package productsmoke

import (
	"slices"
	"testing"
)

// TestRequiredForOSExcludesPOSIXOnlyJourneys proves the required-journey list
// declares its platform limits: the shutdown journey is POSIX-only, the Windows
// filtered list has no such entry, and the portable reload journeys stay
// required on both hosts. This is a unit test of the filter; it is not Windows
// runtime evidence.
func TestRequiredForOSExcludesPOSIXOnlyJourneys(t *testing.T) {
	t.Parallel()

	if len(Required) != 22 {
		t.Fatalf("Required has %d journeys, want 22", len(Required))
	}
	seen := make(map[string]bool, len(Required))
	for _, journey := range Required {
		if journey.ID == "" {
			t.Fatal("Required carries an empty journey ID")
		}
		if seen[journey.ID] {
			t.Fatalf("Required lists %q more than once", journey.ID)
		}
		seen[journey.ID] = true
	}

	linux := RequiredForOS("linux")
	if len(linux) != len(Required) {
		t.Fatalf("the Linux required list has %d journeys, want all %d", len(linux), len(Required))
	}
	windows := RequiredForOS("windows")
	if len(windows) != len(Required)-1 {
		t.Fatalf("the Windows required list has %d journeys, want %d (one POSIX-only journey excluded)", len(windows), len(Required)-1)
	}

	posixOnly := make([]string, 0, 1)
	for _, journey := range Required {
		if !journey.POSIXOnly {
			continue
		}
		posixOnly = append(posixOnly, journey.ID)
		if !slices.ContainsFunc(linux, func(candidate Journey) bool { return candidate.ID == journey.ID }) {
			t.Errorf("POSIX-only journey %q is missing from the Linux required list", journey.ID)
		}
		if slices.ContainsFunc(windows, func(candidate Journey) bool { return candidate.ID == journey.ID }) {
			t.Errorf("POSIX-only journey %q reached the Windows required list", journey.ID)
		}
	}
	if want := []string{"TestProduct_Shutdown/graceful"}; !slices.Equal(posixOnly, want) {
		t.Fatalf("POSIX-only journeys = %v, want only the shutdown journey %v", posixOnly, want)
	}
	for _, id := range []string{
		"TestProduct_Lifecycle/reload_valid",
		"TestProduct_Lifecycle/reload_invalid",
	} {
		if !slices.ContainsFunc(linux, func(candidate Journey) bool { return candidate.ID == id }) {
			t.Errorf("portable journey %q is missing from the Linux required list", id)
		}
		if !slices.ContainsFunc(windows, func(candidate Journey) bool { return candidate.ID == id }) {
			t.Errorf("portable journey %q is missing from the Windows required list", id)
		}
	}
	t.Logf("required journeys: all=%d linux=%d windows=%d posix_only=%v",
		len(Required), len(linux), len(windows), posixOnly)
}
