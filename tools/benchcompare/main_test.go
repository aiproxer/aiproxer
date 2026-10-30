package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoundOrder_AlternatesWhichRevisionRunsFirst(t *testing.T) {
	t.Parallel()
	for round, want := range [][]string{{"baseline", "candidate"}, {"candidate", "baseline"}, {"baseline", "candidate"}} {
		if got := roundOrder(round); !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d = %v want %v", round, got, want)
		}
	}
}

func TestFixtures_MismatchRefusesComparison(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	for _, root := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(root, "fixture.json"), []byte(`{"size":12}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixtureIdentity(a, b, []string{"fixture.json"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "fixture.json"), []byte(`{"size":13}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureIdentity(a, b, []string{"fixture.json"}); err == nil {
		t.Fatal("compared different workloads")
	}
	if _, err := fixtureIdentity(a, b, []string{"../fixture.json"}); err == nil {
		t.Fatal("fixture escaped revision roots")
	}
}

func TestSamples_NoBenchmarkOrInsufficientRowsCannotSupportTimingClaim(t *testing.T) {
	t.Parallel()
	if err := validateSample([]byte("PASS\nok package 0.001s\n")); err == nil {
		t.Fatal("empty benchmark selection accepted")
	}
	if err := validateSample([]byte("BenchmarkEncode-2  12  35.0 ns/op  0 B/op  0 allocs/op\n")); err != nil {
		t.Fatal(err)
	}
	if sufficientSamples(2) || !sufficientSamples(10) {
		t.Fatal("insufficient repetitions presented as timing evidence")
	}
}
