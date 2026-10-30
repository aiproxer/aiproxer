package routing

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestParsePrimaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		backend  string
		model    string
		wantErr  bool
		queryKey string
		queryVal string
	}{
		{"gpt-4o", "", "gpt-4o", false, "", ""},
		{"openai:gpt-4o", "openai", "gpt-4o", false, "", ""},
		{"openai.azure:gpt-4o", "openai.azure", "gpt-4o", false, "", ""},
		{"openai:gpt-4o?temperature=0.2", "openai", "gpt-4o", false, "temperature", "0.2"},
		{"", "", "", true, "", ""},
		{"openai:", "", "", true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(sel.Alternatives) != 1 || sel.Alternatives[0].Primary == nil {
				t.Fatalf("expected single primary, got %#v", sel)
			}
			p := sel.Alternatives[0].Primary
			if p.Backend != tc.backend || p.Model != tc.model {
				t.Fatalf("backend/model: got %q %q want %q %q", p.Backend, p.Model, tc.backend, tc.model)
			}
			if tc.queryKey != "" {
				if p.Params.Get(tc.queryKey) != tc.queryVal {
					t.Fatalf("query: %v", p.Params)
				}
			}
		})
	}
}

func TestParseFailoverOrder(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a|b:c|anthropic:opus")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 3 {
		t.Fatalf("got %d alts", len(sel.Alternatives))
	}
	if sel.Alternatives[0].Primary.Model != "a" {
		t.Fatalf("alt0 model")
	}
	if sel.Alternatives[1].Primary.Backend != "b" || sel.Alternatives[1].Primary.Model != "c" {
		t.Fatalf("alt1")
	}
}

func TestParseWeightedAndFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=3]openai:gpt-4^[weight=1]anthropic:opus")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 1 || sel.Alternatives[0].Weighted == nil {
		t.Fatalf("expected weighted")
	}
	w := sel.Alternatives[0].Weighted
	if len(w.Branches) != 2 || w.Branches[0].Weight != 3 || w.Branches[1].Weight != 1 {
		t.Fatalf("branches: %#v", w.Branches)
	}
}

func TestParseInvalidTwoFirst(t *testing.T) {
	t.Parallel()
	_, err := Parse("[first]a:b^[first]c:d")
	if err == nil {
		t.Fatal("expected error for two [first]")
	}
}

func TestParseFirstSingleArm(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Alternatives[0].Weighted == nil || len(sel.Alternatives[0].Weighted.Branches) != 1 {
		t.Fatalf("expected one weighted branch")
	}
	if !sel.Alternatives[0].Weighted.Branches[0].IsFirst {
		t.Fatalf("expected IsFirst")
	}
}

// Task 14.5: parity with composite routing examples (failover |, weighted ^, [first], [weight=], per-leg query).
func TestParseCompositeSelectorFailoverWeightsAndLegQuery(t *testing.T) {
	t.Parallel()
	s := "[first]openai-codex:gpt-5.3-codex-spark?reasoning_effort=high^[weight=4]openai-codex:gpt-5.3-codex-spark?reasoning_effort=low|[weight=2]openai-codex:gpt-5.3-codex-spark?reasoning_effort=medium"
	sel, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Alternatives) != 2 {
		t.Fatalf("want 2 failover arms, got %d", len(sel.Alternatives))
	}
	// Arm 1: weighted first + second branch with weight 4
	w0 := sel.Alternatives[0].Weighted
	if w0 == nil || len(w0.Branches) != 2 {
		t.Fatalf("arm1 weighted branches: %#v", w0)
	}
	b0 := w0.Branches[0]
	if !b0.IsFirst || b0.Weight != 1 {
		t.Fatalf("branch0: IsFirst=%v Weight=%d", b0.IsFirst, b0.Weight)
	}
	if b0.Target.Backend != "openai-codex" || b0.Target.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("branch0 target: %#v", b0.Target)
	}
	if b0.Target.Params.Get("reasoning_effort") != "high" {
		t.Fatalf("branch0 params: %v", b0.Target.Params)
	}
	b1 := w0.Branches[1]
	if b1.IsFirst || b1.Weight != 4 {
		t.Fatalf("branch1: IsFirst=%v Weight=%d", b1.IsFirst, b1.Weight)
	}
	if b1.Target.Params.Get("reasoning_effort") != "low" {
		t.Fatalf("branch1 params: %v", b1.Target.Params)
	}
	// Arm 2: single weighted branch [weight=2]...
	w1 := sel.Alternatives[1].Weighted
	if w1 == nil || len(w1.Branches) != 1 {
		t.Fatalf("arm2 weighted: %#v", w1)
	}
	if w1.Branches[0].Weight != 2 || w1.Branches[0].IsFirst {
		t.Fatalf("arm2 branch0: %#v", w1.Branches[0])
	}
	if w1.Branches[0].Target.Params.Get("reasoning_effort") != "medium" {
		t.Fatalf("arm2 params: %v", w1.Branches[0].Target.Params)
	}
}

func TestParseRequestSizeAnnotations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantMin int64
		wantMax int64
	}{
		{name: "max context primary", in: "[max_context=4096]openai:gpt-4o-mini", wantMax: 4096},
		{name: "min context primary", in: "[min_context=1024]anthropic:claude", wantMin: 1024},
		{name: "combined context block", in: "[min_context=1024,max_context=8192]openai:gpt", wantMin: 1024, wantMax: 8192},
		{name: "max context suffix primary", in: "[max_context=200K]openai:gpt-4o-mini", wantMax: 200000},
		{name: "min context suffix primary", in: "[min_context=1M]anthropic:claude", wantMin: 1_000_000},
		{name: "combined suffix context block", in: "[min_context=200K,max_context=250K]openai:gpt", wantMin: 200000, wantMax: 250000},
		{name: "query params preserved", in: "[max_context=4096]openai:gpt?temperature=0.2", wantMax: 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if len(sel.Alternatives) != 1 || sel.Alternatives[0].Primary == nil {
				t.Fatalf("expected primary selector, got %#v", sel)
			}
			size := sel.Alternatives[0].Primary.Size
			if tc.wantMin == 0 {
				if size.MinContextTokens != nil {
					t.Fatalf("unexpected min context: %d", *size.MinContextTokens)
				}
			} else if size.MinContextTokens == nil || *size.MinContextTokens != tc.wantMin {
				t.Fatalf("min context: got %v want %d", size.MinContextTokens, tc.wantMin)
			}
			if tc.wantMax == 0 {
				if size.MaxContextTokens != nil {
					t.Fatalf("unexpected max context: %d", *size.MaxContextTokens)
				}
			} else if size.MaxContextTokens == nil || *size.MaxContextTokens != tc.wantMax {
				t.Fatalf("max context: got %v want %d", size.MaxContextTokens, tc.wantMax)
			}
			if sel.Alternatives[0].Primary.Params.Get("temperature") != "" && sel.Alternatives[0].Primary.Params.Get("temperature") != "0.2" {
				t.Fatalf("unexpected params: %v", sel.Alternatives[0].Primary.Params)
			}
		})
	}
}

func TestParseRequestSizeAnnotationsOnWeightedBranches(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=2][max_context=4096]a:m^[weight=1][min_context=4096]b:m")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	if w == nil || len(w.Branches) != 2 {
		t.Fatalf("weighted branches: %#v", sel.Alternatives[0])
	}
	if w.Branches[0].Target.Size.MaxContextTokens == nil || *w.Branches[0].Target.Size.MaxContextTokens != 4096 {
		t.Fatalf("branch0 max: %#v", w.Branches[0].Target.Size)
	}
	if w.Branches[1].Target.Size.MinContextTokens == nil || *w.Branches[1].Target.Size.MinContextTokens != 4096 {
		t.Fatalf("branch1 min: %#v", w.Branches[1].Target.Size)
	}
}

func TestParseRequestSizeAnnotationsInvalid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"[max_context=0]a:b",
		"[min_context=-1]a:b",
		"[max_context=abc]a:b",
		"[max_context]a:b",
		"[max_context=10][max_context=20]a:b",
		"[min_context=10,max_context=10]a:b",
		"[unknown=1]a:b",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseTTFTTimeoutAnnotations(t *testing.T) {
	t.Parallel()
	sel, err := Parse("{ttft_timeout=60}[ttft_timeout=30]openai:gpt-5.5^[weight=2,ttft_timeout=20]gemini:gemini-3")
	if err != nil {
		t.Fatal(err)
	}
	if sel.GlobalTTFTTimeout == nil || *sel.GlobalTTFTTimeout != 60*time.Second {
		t.Fatalf("global ttft timeout: %#v", sel.GlobalTTFTTimeout)
	}
	w := sel.Alternatives[0].Weighted
	if w == nil || len(w.Branches) != 2 {
		t.Fatalf("weighted branches: %#v", sel.Alternatives[0])
	}
	if w.Branches[0].Target.TTFTTimeout == nil || *w.Branches[0].Target.TTFTTimeout != 30*time.Second {
		t.Fatalf("branch0 ttft timeout: %#v", w.Branches[0].Target.TTFTTimeout)
	}
	if w.Branches[1].Weight != 2 {
		t.Fatalf("branch1 weight: %d", w.Branches[1].Weight)
	}
	if w.Branches[1].Target.TTFTTimeout == nil || *w.Branches[1].Target.TTFTTimeout != 20*time.Second {
		t.Fatalf("branch1 ttft timeout: %#v", w.Branches[1].Target.TTFTTimeout)
	}
}

func TestParseGlobalAffinity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want AffinityMode
	}{
		{name: "session alias", raw: "{session_sticky}a:m", want: AffinitySession},
		{name: "client alias", raw: "{client_sticky}a:m", want: AffinityClient},
		{name: "session explicit", raw: "{affinity=session}a:m", want: AffinitySession},
		{name: "client explicit", raw: "{affinity=client}a:m", want: AffinityClient},
		{name: "combined with ttft", raw: "{ttft_timeout=60,affinity=session}a:m", want: AffinitySession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if sel.Affinity != tc.want {
				t.Fatalf("affinity: got %q want %q", sel.Affinity, tc.want)
			}
		})
	}
}

func TestParseGlobalAffinityRejectsAmbiguousOrLeafScope(t *testing.T) {
	t.Parallel()
	cases := []string{
		"{session_sticky,client_sticky}a:m",
		"{affinity=session,affinity=client}a:m",
		"{affinity=tenant}a:m",
		"{affinity}a:m",
		"{session_sticky=true}a:m",
		"{session_sticky=}a:m",
		"{client_sticky=}a:m",
		"[session_sticky]a:m",
		"[client_sticky]a:m",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("got %v want ErrInvalidSelector", err)
			}
		})
	}
}

func TestParseTTFTTimeoutAnnotationsInvalid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"{ttft_timeout=0}a:b",
		"{ttft_timeout=-1}a:b",
		"{ttft_timeout=abc}a:b",
		"{ttft_timeout}a:b",
		"{ttft_timeout=1}{ttft_timeout=2}a:b",
		"a:b{ttft_timeout=1}",
		"a:b{ ttft_timeout=1}",
		"a:b{TTFT_TIMEOUT=1}",
		"{}a:b",
		"[ttft_timeout=0]a:b",
		"[ttft_timeout=-1]a:b",
		"[ttft_timeout=abc]a:b",
		"[ttft_timeout]a:b",
		"[ttft_timeout=1][ttft_timeout=2]a:b",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(in)
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("expected ErrInvalidSelector, got %v", err)
			}
		})
	}
}

func TestParseInvalidQueryWrapsParseQueryError(t *testing.T) {
	t.Parallel()
	_, err := Parse("openai:gpt-4?x=%zz")
	if err == nil {
		t.Fatal("expected error for invalid query escape")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("expected ErrInvalidSelector in chain: %v", err)
	}
	if _, ok := errors.AsType[url.EscapeError](err); !ok {
		t.Fatalf("expected url.EscapeError in chain, got %T: %v", err, err)
	}
}
