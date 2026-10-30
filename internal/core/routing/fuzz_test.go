package routing

import (
	"testing"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

func capFuzzBytes(b []byte, maxBytes int) []byte {
	if maxBytes <= 0 || len(b) <= maxBytes {
		return b
	}
	return b[:maxBytes]
}

func FuzzParseSelector(f *testing.F) {
	f.Add("openai:gpt-4")
	f.Add("a|b|c")
	f.Add("[weight=2]x:y^[first]p:q")
	f.Add("m?max_tokens=10")
	f.Add("{ttft_timeout=60}openai:gpt-4")
	f.Add("{ttft_timeout=60}[ttft_timeout=30]openai:gpt-4^[ttft_timeout=20]gemini:model")
	f.Add("[weight=2,ttft_timeout=30]openai:gpt-4")
	f.Add("a:m1!b:m2!c:m3")
	f.Add("[handicap=10]nvidia:kimi![handicap=5]nvidia:mini!nvidia:flash")
	f.Add("[handicap=10,ttft_timeout=10]a:m![ttft_timeout=5]b:m!c:m")
	f.Add("a:m!b:m|c:m!d:m")
	f.Add("[handicap=3]a:m![handicap=1]b:m!c:m?note=hi")
	// Task 2.4: thinker parse seeds exercise accepted and rejected annotation forms.
	f.Add("[thinker]a:m^b:m")
	f.Add("[thinker=true]a:m^b:m")
	f.Add("[thinker]a:m^b:m!c:m")
	f.Add("[thinker][weight=2]a:m^b:m")
	f.Add("[thinker=false]a:m^b:m")
	f.Add("[thinker]a:m![thinker]b:m")
	f.Add("[first][thinker]a:m^b:m")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4<<10 {
			return
		}
		for _, r := range s {
			if !unicode.IsPrint(r) && r != ' ' {
				return
			}
		}
		sel, err := Parse(s)
		if err != nil {
			return
		}
		if sel == nil {
			t.Fatal("nil without error")
		}
		_, _ = ExpandFailover(sel, PlanOptions{Rand: rng(1), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	})
}

// FuzzParseSelectorFromBytes exercises Parse with arbitrary UTF-8 (no printable-only filter).
func FuzzParseSelectorFromBytes(f *testing.F) {
	f.Add([]byte("openai:gpt-4"))
	f.Add([]byte{0xff, 0xfe, 0xfd})
	// Task 2.4: thinker parse seeds for the byte-oriented fuzz corpus.
	f.Add([]byte("[thinker]a:m^b:m"))
	f.Add([]byte("[thinker]a:m^b:m!c:m"))
	f.Fuzz(func(t *testing.T, b []byte) {
		b = capFuzzBytes(b, 64<<10)
		s := string(b)
		sel, err := Parse(s)
		if err != nil {
			return
		}
		if sel == nil {
			t.Fatal("nil without error")
		}
		_, _ = ExpandFailover(sel, PlanOptions{Rand: rng(1), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	})
}

// FuzzPickWeightedThinkerCycle exercises thinker-aware weighted cycle planning across
// cursor positions, suppression, and exclusion. Task 2.4 fuzz seed for the planner.
func FuzzPickWeightedThinkerCycle(f *testing.F) {
	f.Add(0, false, false, "")
	f.Add(1, false, false, "")
	f.Add(1, true, false, "")
	f.Add(0, false, true, "b:m")
	f.Add(2, true, true, "b:m")
	f.Fuzz(func(t *testing.T, nextIndex int, suppress, consumed bool, excludedKey string) {
		sel, err := Parse("[thinker]a:m^[weight=2]b:m")
		if err != nil {
			t.Fatalf("seed selector must parse: %v", err)
		}
		w := sel.Alternatives[0].Weighted
		if w == nil {
			t.Fatal("expected weighted selector")
		}
		state := interleavedstate.CycleState{
			SelectorKey: "a:m^b:m",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "b:m", Role: interleavedstate.RoleExecutor},
				{Key: "b:m", Role: interleavedstate.RoleExecutor},
				{Key: "a:m", Role: interleavedstate.RoleThinker},
			},
		}
		if nextIndex < 0 {
			nextIndex = 0
		}
		if nextIndex > len(state.Sequence) {
			nextIndex %= len(state.Sequence)
		}
		state.NextIndex = nextIndex
		opt := PlanOptions{
			Session:         &SessionRoutingState{FirstRequestConsumed: consumed},
			SuppressThinker: suppress,
			ThinkerCycle:    state,
		}
		if excludedKey != "" {
			opt.Excluded = map[string]struct{}{excludedKey: {}}
		}
		_, _, _, _ = pickWeighted(w, opt)
	})
}
