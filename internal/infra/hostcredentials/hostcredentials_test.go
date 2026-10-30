// Package hostcredentials owns the single policy-driven numbered
// environment-key resolver for static API-key credentials.
//
// Both the standard-distribution host capture (internal/standardplugins) and
// the compatible-family helpers (internal/plugins/backends/compatmode)
// resolve through here, so sparse numbering, slot de-duplication, root_1
// rejection, value-size budgets and secret-free errors cannot diverge between
// the two historical scanners this package replaces.
package hostcredentials

import (
	"strings"
	"testing"
)

type mapEnv map[string]string

func (m mapEnv) Lookup(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

func valuesOf(t *testing.T, slots []Slot) []string {
	t.Helper()
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.Value)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResolveSparseAcrossGaps(t *testing.T) {
	t.Parallel()
	env := mapEnv{"TEST_ROOT_API_KEY": "s1", "TEST_ROOT_API_KEY_3": "s3", "TEST_ROOT_API_KEY_5": "s5"}
	slots, err := Resolve("TEST_ROOT_API_KEY", env, 20)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := valuesOf(t, slots), []string{"s1", "s3", "s5"}; !equalStrings(got, want) {
		t.Fatalf("values = %#v want %#v (must scan through gaps)", got, want)
	}
	if slots[0].Index != 1 || slots[1].Index != 3 || slots[2].Index != 5 {
		t.Fatalf("slot identity lost: %+v", slots)
	}
}

func TestResolveSkipsBlankAndTrims(t *testing.T) {
	t.Parallel()
	env := mapEnv{"TEST_ROOT_API_KEY": "  s1  ", "TEST_ROOT_API_KEY_2": "   ", "TEST_ROOT_API_KEY_3": "\ts3\n"}
	slots, err := Resolve("TEST_ROOT_API_KEY", env, 20)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := valuesOf(t, slots), []string{"s1", "s3"}; !equalStrings(got, want) {
		t.Fatalf("values = %#v want %#v", got, want)
	}
}

func TestResolveDeduplicatesKeepingFirstPriority(t *testing.T) {
	t.Parallel()
	env := mapEnv{"TEST_ROOT_API_KEY": "same", "TEST_ROOT_API_KEY_2": "same", "TEST_ROOT_API_KEY_3": "other", "TEST_ROOT_API_KEY_4": "other"}
	slots, err := Resolve("TEST_ROOT_API_KEY", env, 20)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := valuesOf(t, slots), []string{"same", "other"}; !equalStrings(got, want) {
		t.Fatalf("values = %#v want %#v", got, want)
	}
	if slots[0].Index != 1 || slots[1].Index != 3 {
		t.Fatalf("dedup must keep first-occurrence slot identity: %+v", slots)
	}
}

func TestResolveRejectsRoot1Suffix(t *testing.T) {
	t.Parallel()
	env := mapEnv{"TEST_ROOT_API_KEY": "s1", "TEST_ROOT_API_KEY_1": "misconfigured"}
	_, err := Resolve("TEST_ROOT_API_KEY", env, 20)
	if err == nil {
		t.Fatal("expected root_1 rejection")
	}
	if !strings.Contains(err.Error(), "TEST_ROOT_API_KEY_1") {
		t.Fatalf("error %q must name the misconfigured variable", err.Error())
	}
	if strings.Contains(err.Error(), "misconfigured") && strings.Contains(err.Error(), "s1") {
		t.Fatalf("error leaked secret-adjacent test value: %q", err.Error())
	}
}

func TestResolveRejectsOversizedAndNUL(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("k", MaxValueBytes+1)
	if _, err := Resolve("TEST_ROOT_API_KEY", mapEnv{"TEST_ROOT_API_KEY": big}, 20); err == nil {
		t.Fatal("expected oversized value rejection")
	} else if strings.Contains(err.Error(), big) {
		t.Fatalf("error leaked secret value")
	}
	if _, err := Resolve("TEST_ROOT_API_KEY", mapEnv{"TEST_ROOT_API_KEY_2": "a\x00b"}, 20); err == nil {
		t.Fatal("expected NUL rejection")
	} else if strings.Contains(err.Error(), "a\x00b") {
		t.Fatalf("error leaked secret value")
	}
}

func TestResolveEnforcesMaxIndexBounds(t *testing.T) {
	t.Parallel()
	for _, bad := range []int{0, -1, 257, 1000} {
		if _, err := Resolve("TEST_ROOT_API_KEY", mapEnv{}, bad); err == nil {
			t.Fatalf("maxIndex %d: expected bounds rejection", bad)
		}
	}
	env := mapEnv{"TEST_ROOT_API_KEY_256": "edge"}
	slots, err := Resolve("TEST_ROOT_API_KEY", env, 256)
	if err != nil {
		t.Fatalf("Resolve at upper bound: %v", err)
	}
	if len(slots) != 1 || slots[0].Index != 256 {
		t.Fatalf("upper-bound slot not resolved: %+v", slots)
	}
	if slots, err := Resolve("TEST_ROOT_API_KEY", env, 20); err != nil || len(slots) != 0 {
		t.Fatalf("bounded scan must stop at N: %+v, %v", slots, err)
	}
}

func TestResolveAllRejectsDuplicateRoots(t *testing.T) {
	t.Parallel()
	env := mapEnv{"TEST_ROOT_API_KEY": "s1"}
	_, err := ResolveAll([]string{"TEST_ROOT_API_KEY", "OTHER_API_KEY", "TEST_ROOT_API_KEY"}, env, 20)
	if err == nil {
		t.Fatal("expected duplicate-root collision rejection")
	}
	if !strings.Contains(err.Error(), "TEST_ROOT_API_KEY") {
		t.Fatalf("error %q must name the colliding root", err.Error())
	}
}

func TestResolveErrorsNeverCarryValues(t *testing.T) {
	t.Parallel()
	secret := "sentinel-secret-value"
	cases := []struct {
		name string
		env  mapEnv
		root string
		max  int
	}{
		{name: "root1", env: mapEnv{"R_API_KEY_1": secret}, root: "R_API_KEY", max: 20},
		{name: "nul", env: mapEnv{"R_API_KEY": "a\x00" + secret}, root: "R_API_KEY", max: 20},
		{name: "oversize", env: mapEnv{"R_API_KEY": strings.Repeat("x", MaxValueBytes+1)}, root: "R_API_KEY", max: 20},
		{name: "bad root", env: mapEnv{}, root: "lower_api_key", max: 20},
		{name: "bad max", env: mapEnv{}, root: "R_API_KEY", max: 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Resolve(tc.root, tc.env, tc.max)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret value: %q", err.Error())
			}
		})
	}
}
