package main

import (
	"strings"
	"testing"
)

func TestUniqueGoPathCountIgnoresNonGoBlanksAndDuplicates(t *testing.T) {
	t.Parallel()
	names := []string{"a.go", " a.go ", "", "b.go", "a.go", "notes.md", "README"}
	if got := uniqueGoPathCount(names); got != 2 {
		t.Fatalf("uniqueGoPathCount=%d, want 2", got)
	}
}

func TestAllowedAtLimitAndOverride(t *testing.T) {
	t.Parallel()
	if !allowed(100, DefaultLimit, false) {
		t.Fatal("100 files must pass the default limit")
	}
	if allowed(101, DefaultLimit, false) {
		t.Fatal("101 files must fail without override")
	}
	if !allowed(101, DefaultLimit, true) {
		t.Fatal("101 files must pass when override is set")
	}
}

func TestTruthyOverrideValues(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"1", "true", "TRUE", "yes", "On", " 1 "} {
		if !truthy(v) {
			t.Fatalf("truthy(%q)=false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		if truthy(v) {
			t.Fatalf("truthy(%q)=true, want false", v)
		}
	}
}

func TestSplitGitNamesNullSeparated(t *testing.T) {
	t.Parallel()
	raw := []byte("a.go\x00b with space.md\x00c.go\x00")
	got := splitGitNames(raw)
	want := []string{"a.go", "b with space.md", "c.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("splitGitNames=%q, want %q", got, want)
	}
	if uniqueGoPathCount(got) != 2 {
		t.Fatalf("count=%d, want 2", uniqueGoPathCount(got))
	}
}
