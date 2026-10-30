package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestQuarantineSkipsListedTests(t *testing.T) {
	t.Parallel()
	entries, err := parseQuarantine(strings.NewReader(`# Format: <name> <issue-url>

TestAlpha https://github.com/aiproxer/aiproxer-enterprise/issues/1
  TestBeta   https://github.com/aiproxer/aiproxer/issues/22
`))
	if err != nil {
		t.Fatal(err)
	}
	got := withQuarantine([]string{"go", "test", "-json", "./..."}, entries)
	want := []string{"go", "test", "-skip=^(TestAlpha|TestBeta)$", "-json", "./..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	build := []string{"go", "build", "./..."}
	if got := withQuarantine(build, entries); !reflect.DeepEqual(got, build) {
		t.Fatalf("non-test command changed: %q", got)
	}
	if got := withQuarantine(build[:2], nil); len(got) != 2 {
		t.Fatalf("empty quarantine changed command: %q", got)
	}
}

func TestQuarantineRejectsMalformedEntries(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"TestAlpha",
		"TestAlpha https://example.com/issues/1",
		"TestAlpha https://github.com/aiproxer/aiproxer-other/issues/1",
		"TestAlpha https://github.com/other/aiproxer/issues/1",
		"TestAlpha https://github.com/aiproxer/aiproxer/issues/not-a-number",
		"helperFunc https://github.com/aiproxer/aiproxer-enterprise/issues/1",
		"TestAlpha https://github.com/aiproxer/aiproxer-enterprise/issues/1 extra",
		"TestAlpha https://github.com/aiproxer/aiproxer-enterprise/issues/1\nTestAlpha https://github.com/aiproxer/aiproxer-enterprise/issues/2",
		"# TestAlpha is flaky",
	} {
		if _, err := parseQuarantine(strings.NewReader(line)); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}
