package routing

import (
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestExpandFailoverLeftToRightPrimaries(t *testing.T) {
	t.Parallel()
	sel, err := Parse("openai:gpt-4|anthropic:opus|bedrock:claude")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Key != "openai:gpt-4" || out[1].Key != "anthropic:opus" {
		t.Fatalf("order: %#v", out)
	}
}

func TestExpandFailoverSkipsExcludedPrimary(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:b|c:d")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"a:b": {}}
	out, err := ExpandFailover(sel, PlanOptions{Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "c:d" {
		t.Fatalf("got %#v", out)
	}
}

func TestExpandFailoverSkipsUnhealthyPrimary(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:b|c:d")
	if err != nil {
		t.Fatal(err)
	}
	uh := map[string]struct{}{"a:b": {}}
	out, err := ExpandFailover(sel, PlanOptions{Unhealthy: uh})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "c:d" {
		t.Fatalf("got %#v", out)
	}
}

func TestExpandFailoverRequestSizeConstraints(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		sel    string
		tokens int64
		want   string
	}{
		{name: "max excludes oversized primary", sel: "[max_context=10]a:b|c:d", tokens: 11, want: "c:d"},
		{name: "max allows exact limit", sel: "[max_context=10]a:b|c:d", tokens: 10, want: "a:b"},
		{name: "min excludes equal tokens", sel: "[min_context=10]a:b|c:d", tokens: 10, want: "c:d"},
		{name: "min allows greater tokens", sel: "[min_context=10]a:b|c:d", tokens: 11, want: "a:b"},
		{name: "max suffix excludes oversized primary", sel: "[max_context=200K]a:b|c:d", tokens: 200001, want: "c:d"},
		{name: "combined suffix range allows middle tokens", sel: "[min_context=200K,max_context=250K]a:b|c:d", tokens: 225000, want: "a:b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			out, err := ExpandFailover(sel, PlanOptions{RequestSize: RequestSizeEstimate{Available: true, Tokens: tc.tokens}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 || out[0].Key != tc.want {
				t.Fatalf("got %#v want first %q", out, tc.want)
			}
		})
	}
}

func TestWeightedRequestSizeFiltersBeforeRoll(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=100][max_context=10]small:m^[weight=1]large:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		Rand:        rng(0),
		Session:     &SessionRoutingState{FirstRequestConsumed: true},
		RequestSize: RequestSizeEstimate{Available: true, Tokens: 11},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "large:m" {
		t.Fatalf("got %#v", out)
	}
}

func TestExpandFailover_stickyBackendOverridesWeightedRollWhenEligible(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=100]a:m^[weight=1]b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{
		StickyBackendID: "b",
		Rand:            rng(0),
		Session:         &SessionRoutingState{FirstRequestConsumed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("got %#v want sticky b:m", out)
	}
}

func TestExpandFailover_stickyBackendIgnoredWhenUnhealthyOrSizeIneligible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sel  string
		opt  PlanOptions
		want string
	}{
		{
			name: "unhealthy",
			sel:  "a:m|b:m",
			opt:  PlanOptions{StickyBackendID: "b", Unhealthy: map[string]struct{}{"b:m": {}}},
			want: "a:m",
		},
		{
			name: "max context exceeded",
			sel:  "a:m|[max_context=10]b:m",
			opt:  PlanOptions{StickyBackendID: "b", RequestSize: RequestSizeEstimate{Available: true, Tokens: 11}},
			want: "a:m",
		},
		{
			name: "min context boundary excluded",
			sel:  "a:m|[min_context=10]b:m",
			opt:  PlanOptions{StickyBackendID: "b", RequestSize: RequestSizeEstimate{Available: true, Tokens: 10}},
			want: "a:m",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			out, err := ExpandFailover(sel, tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 || out[0].Key != tc.want {
				t.Fatalf("got %#v want first %q", out, tc.want)
			}
		})
	}
}

func TestExpandFailover_stickyBackendAbsentFallsBackToNormalPlanning(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m|b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{StickyBackendID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Key != "a:m" || out[1].Key != "b:m" {
		t.Fatalf("got %#v", out)
	}
}

func TestExpandFailover_stickyFirstBranchMarksFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]a:m^[weight=1]b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{StickyBackendID: "a", Session: &SessionRoutingState{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "a:m" || !out[0].MarkedFirst {
		t.Fatalf("got %#v want sticky first branch marked", out)
	}
}

func TestExpandFailoverPreservesTTFTTimeoutMetadata(t *testing.T) {
	t.Parallel()
	sel, err := Parse("{ttft_timeout=60}[ttft_timeout=30]a:b|[ttft_timeout=20]c:d")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if sel.GlobalTTFTTimeout == nil || *sel.GlobalTTFTTimeout != 60*time.Second {
		t.Fatalf("global ttft timeout: %#v", sel.GlobalTTFTTimeout)
	}
	if out[0].Primary.TTFTTimeout == nil || *out[0].Primary.TTFTTimeout != 30*time.Second {
		t.Fatalf("candidate0 ttft timeout: %#v", out[0].Primary.TTFTTimeout)
	}
	if out[1].Primary.TTFTTimeout == nil || *out[1].Primary.TTFTTimeout != 20*time.Second {
		t.Fatalf("candidate1 ttft timeout: %#v", out[1].Primary.TTFTTimeout)
	}
	if out[0].Key != "a:b" || out[1].Key != "c:d" {
		t.Fatalf("timeout annotations must not alter candidate keys: %#v", out)
	}
}

func TestFirstRequestSizeIneligibleDoesNotConsumeFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first][max_context=10]small:m^[weight=1]large:m")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{}
	out, err := ExpandFailover(sel, PlanOptions{
		Rand:        rng(0),
		Session:     sess,
		RequestSize: RequestSizeEstimate{Available: true, Tokens: 11},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "large:m" {
		t.Fatalf("got %#v", out)
	}
	if sess.FirstRequestConsumed {
		t.Fatal("size-ineligible [first] branch must not consume first-request state")
	}
}

func TestRequestSizeConstraintsFailOpenWhenEstimateUnavailable(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[max_context=10]a:b|c:d")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{RequestSize: RequestSizeEstimate{Available: false, Tokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 || out[0].Key != "a:b" {
		t.Fatalf("got %#v", out)
	}
}

func TestRequestSizeConstraintsAllIneligible(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[max_context=10]a:b|[min_context=20]c:d")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ExpandFailover(sel, PlanOptions{RequestSize: RequestSizeEstimate{Available: true, Tokens: 15}})
	if !errors.Is(err, ErrNoEligibleCandidate) {
		t.Fatalf("got %v want ErrNoEligibleCandidate", err)
	}
}

func rng(seed int64) Rng { return NewSeededRng(seed) }

func TestWeightedDeterministic(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:x^[weight=1]b:y")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("len %d", len(out))
	}
	// PCG seed 0: first Intn(2)==0 — picks first weighted branch (a:x).
	if out[0].Key != "a:x" {
		t.Fatalf("got %q", out[0].Key)
	}
}

func TestFirstRequestForcesBranch(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast^[weight=100]expensive:slow")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(99), Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "cheap:fast" {
		t.Fatalf("got %#v", out)
	}
	if !sess.FirstRequestConsumed {
		t.Fatal("session should mark first consumed")
	}
}

func TestFirstRequestIgnoredAfterConsumedUsesEligibleSet(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]a:a^[weight=1]b:b")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{FirstRequestConsumed: true}
	ex := map[string]struct{}{"a:a": {}}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Session: sess, Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:b" {
		t.Fatalf("got %#v", out)
	}
}

// Req 7.1 / 7.4 / 7.5: on retry path, RNG selection of the sole remaining [first] branch must not set MarkedFirst
// (first-request steering was not consumed via the forced [first] path).
func TestRetryPathRNGDoesNotMarkFirstWhenOnlyFirstBranchEligible(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:a^[first][weight=1]b:b")
	if err != nil {
		t.Fatal(err)
	}
	sess := &SessionRoutingState{FirstRequestConsumed: false}
	ex := map[string]struct{}{"a:a": {}}
	out, err := ExpandFailover(sel, PlanOptions{
		Rand:        rng(0),
		Session:     sess,
		Excluded:    ex,
		IsRetryPath: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:b" {
		t.Fatalf("got %#v", out)
	}
	if out[0].MarkedFirst {
		t.Fatal("MarkedFirst must be false on retry path when only [first] branch remains after exclusions")
	}
}

func TestReplanWeightedIgnoresFirst(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[first]cheap:fast^[weight=1]expensive:slow")
	if err != nil {
		t.Fatal(err)
	}
	w := sel.Alternatives[0].Weighted
	ex := map[string]struct{}{"cheap:fast": {}}
	opt := PlanOptions{
		Rand:        rng(0),
		Session:     &SessionRoutingState{FirstRequestConsumed: false},
		Excluded:    ex,
		IsRetryPath: false,
	}
	c, err := ReplanWeighted(w, opt)
	if err != nil {
		t.Fatal(err)
	}
	if c.Key != "expensive:slow" {
		t.Fatalf("got %q", c.Key)
	}
}

func TestExpandFailover_nil_selector_wrapsErrInvalidSelector(t *testing.T) {
	t.Parallel()
	_, err := ExpandFailover(nil, PlanOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("errors.Is ErrInvalidSelector: got %v", err)
	}
}

func TestWeightedSkipsToNextFailoverWhenAllExcluded(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[weight=1]a:b^[weight=1]c:d|fallback:model")
	if err != nil {
		t.Fatal(err)
	}
	ex := map[string]struct{}{"a:b": {}, "c:d": {}}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Excluded: ex})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "fallback:model" {
		t.Fatalf("got %#v", out)
	}
}

func TestPickWeighted_int64SumOverflow(t *testing.T) {
	t.Parallel()
	if strconv.IntSize != 64 {
		t.Skip("needs 64-bit int to hold math.MaxInt64-1 as branch weight")
	}
	w := &Weighted{Branches: []WeightedBranch{
		{Weight: math.MaxInt64 - 1, Target: Primary{Backend: "a", Model: "x"}},
		{Weight: 2, Target: Primary{Backend: "b", Model: "y"}},
	}}
	_, _, _, err := pickWeighted(w, PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if !errors.Is(err, ErrWeightedTotalTooLarge) {
		t.Fatalf("got %v want ErrWeightedTotalTooLarge", err)
	}
}

func TestExpandFailover_weightedSumVsMaxInt(t *testing.T) {
	t.Parallel()
	// int64(math.MaxInt32) + 2 exceeds [math.MaxInt] on 32-bit platforms; 64-bit int can represent it.
	sel := &Selector{
		Alternatives: []FailoverAlt{{
			Weighted: &Weighted{Branches: []WeightedBranch{
				{Weight: math.MaxInt32, Target: Primary{Backend: "a", Model: "x"}},
				{Weight: 2, Target: Primary{Backend: "b", Model: "y"}},
			}},
		}},
	}
	out, err := ExpandFailover(sel, PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if strconv.IntSize == 32 {
		if !errors.Is(err, ErrWeightedTotalTooLarge) {
			t.Fatalf("got %v want ErrWeightedTotalTooLarge", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("len %d", len(out))
	}
}
