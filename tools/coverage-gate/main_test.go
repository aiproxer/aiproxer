package main

import (
	"math/big"
	"testing"
)

func TestParseTotal(t *testing.T) {
	t.Parallel()
	got, err := parseTotal("github.com/example/foo.Func 1 2 3\n total: (statements) 84.2%\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(mustRat("421/5")) != 0 {
		t.Fatalf("got %s, want 84.2", got)
	}
}

func TestParseTotalRejectsDuplicateTotalLines(t *testing.T) {
	t.Parallel()
	if _, err := parseTotal("total: (statements) 89.9%\ntotal: (statements) 90.0%\n"); err == nil {
		t.Fatal("expected duplicate total error")
	}
}

func TestParseTotalRejectsSpoofedTotal(t *testing.T) {
	t.Parallel()
	if _, err := parseTotal("total: (statements) 90.0%\ntotal: spoofed 99.9%\n"); err == nil {
		t.Fatal("expected malformed total error")
	}
}

func TestParseTotalRejectsMalformedTotal(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"total: (statements) 90%\n",
		"total: (functions) 90.0%\n",
		"total: (statements) 100.00%\n",
	} {
		if _, err := parseTotal(input); err == nil {
			t.Fatalf("expected malformed total error for %q", input)
		}
	}
}

func TestParseTotalRejectsMissingTotal(t *testing.T) {
	t.Parallel()
	if _, err := parseTotal("ok\n"); err == nil {
		t.Fatal("expected missing total error")
	}
}

func TestThresholdComparisonDoesNotUseFloatingPoint(t *testing.T) {
	t.Parallel()
	actual := mustRat("8999999999999999/100000000000000")
	threshold := mustRat("90")
	if actual.Cmp(threshold) >= 0 {
		t.Fatal("value below threshold was accepted")
	}
}

func mustRat(value string) *big.Rat {
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		panic(value)
	}
	return result
}
