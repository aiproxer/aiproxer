package routing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseParallelBasic(t *testing.T) {
	t.Parallel()
	sel, err := Parse("nvidia:kimi-k2!nvidia:minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 1 {
		t.Fatalf("expected 1 failover alt (parallel group), got %d", len(sel.Alternatives))
	}
	alt := sel.Alternatives[0]
	if alt.Parallel == nil {
		t.Fatalf("expected parallel group, got %#v", alt)
	}
	p := alt.Parallel
	if len(p.Branches) != 2 {
		t.Fatalf("branches: got %d want 2", len(p.Branches))
	}
	if p.Branches[0].Target.Backend != "nvidia" || p.Branches[0].Target.Model != "kimi-k2" {
		t.Fatalf("branch0: %#v", p.Branches[0].Target)
	}
	if p.Branches[1].Target.Backend != "nvidia" || p.Branches[1].Target.Model != "minimax-m3" {
		t.Fatalf("branch1: %#v", p.Branches[1].Target)
	}
}

func TestParseParallelHandicap(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[handicap=10]nvidia:kimi-k2![handicap=5]nvidia:minimax-m3!nvidia:flash")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 1 {
		t.Fatalf("alts: %d", len(sel.Alternatives))
	}
	p := sel.Alternatives[0].Parallel
	if p == nil {
		t.Fatal("expected parallel group")
		return
	}
	if len(p.Branches) != 3 {
		t.Fatalf("branches: %d", len(p.Branches))
	}
	if p.Branches[0].Handicap != 10*time.Second {
		t.Fatalf("branch0 handicap: %v", p.Branches[0].Handicap)
	}
	if p.Branches[1].Handicap != 5*time.Second {
		t.Fatalf("branch1 handicap: %v", p.Branches[1].Handicap)
	}
	if p.Branches[2].Handicap != 0 {
		t.Fatalf("branch2 handicap: %v want 0", p.Branches[2].Handicap)
	}
}

func TestParseParallelTTFTTimeout(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[handicap=10,ttft_timeout=10]a:m1![ttft_timeout=5]b:m2!c:m3")
	if err != nil {
		t.Fatal(err)
	}
	p := sel.Alternatives[0].Parallel
	if p == nil {
		t.Fatal("expected parallel")
		return
	}
	if p.Branches[0].Target.TTFTTimeout == nil || *p.Branches[0].Target.TTFTTimeout != 10*time.Second {
		t.Fatalf("branch0 ttft: %v", p.Branches[0].Target.TTFTTimeout)
	}
	if p.Branches[0].Handicap != 10*time.Second {
		t.Fatalf("branch0 handicap: %v", p.Branches[0].Handicap)
	}
	if p.Branches[1].Target.TTFTTimeout == nil || *p.Branches[1].Target.TTFTTimeout != 5*time.Second {
		t.Fatalf("branch1 ttft: %v", p.Branches[1].Target.TTFTTimeout)
	}
	if p.Branches[2].Target.TTFTTimeout != nil {
		t.Fatalf("branch2 ttft: want nil got %v", p.Branches[2].Target.TTFTTimeout)
	}
}

func TestParseParallelUserExample(t *testing.T) {
	t.Parallel()
	s := "[handicap=10,ttft_timeout=10]nvidia:moonshotai/kimi-k2.6![handicap=5,ttft_timeout=5]nvidia:minimaxai/minimax-m3![handicap=2]nvidia:nvidia/nemotron-3-ultra-550b-a55b!nvidia:stepfun-ai/step-3.7-flash!nvidia:mistralai/mistral-large-3-675b-instruct-2512"
	sel, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 1 {
		t.Fatalf("alts: %d", len(sel.Alternatives))
	}
	p := sel.Alternatives[0].Parallel
	if p == nil {
		t.Fatal("expected parallel group")
		return
	}
	if len(p.Branches) != 5 {
		t.Fatalf("branches: %d want 5", len(p.Branches))
	}
	if p.Branches[0].Handicap != 10*time.Second {
		t.Fatalf("branch0 handicap: %v", p.Branches[0].Handicap)
	}
	if p.Branches[0].Target.Backend != "nvidia" || p.Branches[0].Target.Model != "moonshotai/kimi-k2.6" {
		t.Fatalf("branch0: %#v", p.Branches[0].Target)
	}
	if p.Branches[1].Handicap != 5*time.Second {
		t.Fatalf("branch1 handicap: %v", p.Branches[1].Handicap)
	}
	if p.Branches[3].Handicap != 0 || p.Branches[4].Handicap != 0 {
		t.Fatalf("branches 3/4 handicap: %v %v", p.Branches[3].Handicap, p.Branches[4].Handicap)
	}
	if p.Branches[4].Target.Model != "mistralai/mistral-large-3-675b-instruct-2512" {
		t.Fatalf("branch4 model: %s", p.Branches[4].Target.Model)
	}
}

func TestParseParallelFailoverOfParallelGroups(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1!b:m2|c:m3!d:m4")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 2 {
		t.Fatalf("alts: %d want 2", len(sel.Alternatives))
	}
	if sel.Alternatives[0].Parallel == nil || len(sel.Alternatives[0].Parallel.Branches) != 2 {
		t.Fatalf("alt0: expected parallel with 2 branches")
	}
	if sel.Alternatives[1].Parallel == nil || len(sel.Alternatives[1].Parallel.Branches) != 2 {
		t.Fatalf("alt1: expected parallel with 2 branches")
	}
}

func TestParseParallelRejectsMixedWithWeighted(t *testing.T) {
	t.Parallel()
	cases := []string{
		"a:m^b:m!c:m",
		"a:m!b:m^c:m",
		"[weight=2]a:m!b:m",
		"[first]a:m!b:m",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected error for mixed parallel+weighted")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseParallelHandicapOnlyInParallel(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[handicap=1]a:b|c:d",
		"[handicap=1]a:b^c:d",
		"[handicap=1]a:b",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected error: handicap outside parallel group")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseParallelInvalidHandicapValues(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[handicap=-1]a:b!c:d",
		"[handicap=abc]a:b!c:d",
		"[handicap]a:b!c:d",
		"[handicap=0]a:b!c:d",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected error for invalid handicap")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseParallelEmptyBranch(t *testing.T) {
	t.Parallel()
	_, err := Parse("a:b!!c:d")
	if err == nil {
		t.Fatal("expected error for empty parallel branch")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("expected ErrInvalidSelector, got %v", err)
	}
}

func TestParseParallelQueryBangPreserved(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m?note=hello%21world!b:m")
	if err != nil {
		t.Fatal(err)
	}
	p := sel.Alternatives[0].Parallel
	if p == nil {
		t.Fatal("expected parallel group")
		return
	}
	if len(p.Branches) != 2 {
		t.Fatalf("branches: %d want 2", len(p.Branches))
	}
	if p.Branches[0].Target.Params.Get("note") != "hello!world" {
		t.Fatalf("branch0 query note: %q", p.Branches[0].Target.Params.Get("note"))
	}
}

func TestParseParallelPreservesQueryParams(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[handicap=5]nvidia:kimi?temperature=0.3!nvidia:flash?reasoning_effort=high")
	if err != nil {
		t.Fatal(err)
	}
	p := sel.Alternatives[0].Parallel
	if p == nil {
		t.Fatal("expected parallel")
		return
	}
	if p.Branches[0].Target.Params.Get("temperature") != "0.3" {
		t.Fatalf("branch0 temp: %v", p.Branches[0].Target.Params)
	}
	if p.Branches[1].Target.Params.Get("reasoning_effort") != "high" {
		t.Fatalf("branch1 reasoning_effort: %v", p.Branches[1].Target.Params)
	}
}

func TestParseParallelRejectsTooManyBranches(t *testing.T) {
	t.Parallel()
	selector := "a:m" + strings.Repeat("!a:m", maxParallelBranches)
	_, err := Parse(selector)
	if err == nil {
		t.Fatal("expected error for excessive parallel branch count")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("expected ErrInvalidSelector, got %v", err)
	}
}
