package engine

import (
	"slices"
	"testing"
)

func TestAhoNoHitAllocatesNoMatchBuffer(t *testing.T) {
	ac := buildAhoCorasick([]catalogEntry{{value: []byte("synthetic-credential")}})
	for _, raw := range []string{"123456789012345678901234567890", "true", "false", "null", "ordinary text"} {
		input := []byte(raw)
		if allocations := testing.AllocsPerRun(10, func() {
			if len(ac.findAll(input)) != 0 {
				t.Fatal("no-hit fixture matched")
			}
		}); allocations != 0 {
			t.Errorf("no-hit scan reserved unused match storage: allocations=%.0f", allocations)
		}
	}
}

func TestAhoLazyMatchBufferPreservesRepeatedAndOverlappingHits(t *testing.T) {
	t.Parallel()
	ac := buildAhoCorasick([]catalogEntry{{value: []byte("aba")}, {value: []byte("ba")}})
	want := []matchHit{
		{start: 0, length: 3, entryIdx: 0},
		{start: 1, length: 2, entryIdx: 1},
		{start: 2, length: 3, entryIdx: 0},
		{start: 3, length: 2, entryIdx: 1},
	}
	if got := ac.findAll([]byte("ababa")); !slices.Equal(got, want) {
		t.Error("lazy allocation changed hit order, overlap or cardinality")
	}
	for _, empty := range []*ahoCorasick{nil, {}, ac} {
		if got := empty.findAll(nil); got != nil {
			t.Error("empty input changed nil hit behavior")
		}
	}
}
