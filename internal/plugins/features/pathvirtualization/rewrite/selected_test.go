package rewrite_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// Spec: b-leg-path-virtualization Task 8.1. design.md section 7 steps 4/5/9 and
// requirements.md 4.8 ("preserve JSON validity and all non-selected argument
// fields") rest on the byte-splice engine Task 4.1 shipped for the OUTBOUND
// direction. Task 8.1 needs the very same engine for the INBOUND direction, and
// duplicating it would put requirement 4.8's byte-for-byte property at the mercy
// of a second implementation.
//
// These tests pin the engine as a reusable primitive BEFORE it has one: which
// outcomes it reports, that a refusal publishes nothing, that eligibility and
// byte totals are decoded-value lengths, and that every non-selected byte
// survives.

// selectedPointers compiles a selector set the way a profile would.
func selectedPointers(t *testing.T, pointers ...string) pathvirtualization.SelectorSet {
	t.Helper()
	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{"probe_tool"},
		ArgPointers: pointers,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("compile %v: reject %v", pointers, reject)
	}
	if len(compiled) != 1 {
		t.Fatalf("compiled profiles=%d want 1", len(compiled))
	}
	return compiled[0].ArgPointers
}

// prefixDecider replaces every eligible value with a fixed prefix and reports
// eligibility for every value, so the engine's own accounting is observable.
func prefixDecider() rewrite.ValueDecider {
	return func(value string) rewrite.ValueDecision {
		return rewrite.ValueDecision{Replacement: "X:" + value, Eligible: true}
	}
}

// TestApplySelectedValuesReportsTheWholeOutcomeVocabulary pins every bounded
// outcome the engine can report. Each state must be DISTINGUISHABLE, because both
// directions project it onto their own closed vocabularies: the outbound
// rewriter records it as a skip, and Task 8.1's expansion finalizer refuses
// closed on the invalid-document state (requirements.md 4.4).
func TestApplySelectedValuesReportsTheWholeOutcomeVocabulary(t *testing.T) {
	t.Parallel()

	pointers := selectedPointers(t, "/path")
	for _, tc := range []struct {
		name     string
		pointers pathvirtualization.SelectorSet
		payload  string
		want     rewrite.PayloadOutcome
	}{
		{name: "no_selectors", pointers: nil, payload: `{"path":"/a"}`, want: rewrite.PayloadOutcomeNoSelectors},
		{name: "absent", pointers: pointers, payload: ``, want: rewrite.PayloadOutcomeAbsent},
		{name: "null", pointers: pointers, payload: `null`, want: rewrite.PayloadOutcomeAbsent},
		{name: "invalid", pointers: pointers, payload: `{"path":"/a`, want: rewrite.PayloadOutcomeInvalid},
		{name: "trailing_garbage", pointers: pointers, payload: `{"path":"/a"}x`, want: rewrite.PayloadOutcomeInvalid},
		{name: "two_documents", pointers: pointers, payload: `{"path":"/a"}{"path":"/b"}`, want: rewrite.PayloadOutcomeInvalid},
		{name: "array_root", pointers: pointers, payload: `["/a"]`, want: rewrite.PayloadOutcomeNotObject},
		{name: "scalar_root", pointers: pointers, payload: `"/a"`, want: rewrite.PayloadOutcomeNotObject},
		{name: "unresolved_pointer", pointers: pointers, payload: `{"other":"/a"}`, want: rewrite.PayloadOutcomeNoLeaves},
		{name: "replaced", pointers: pointers, payload: `{"path":"/a"}`, want: rewrite.PayloadOutcomeReplaced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			published, pass, err := rewrite.ApplySelectedValues([]byte(tc.payload), tc.pointers, prefixDecider())
			if err != nil {
				t.Fatalf("ApplySelectedValues: %v", err)
			}
			if pass.Outcome != tc.want {
				t.Fatalf("outcome=%v want %v", pass.Outcome, tc.want)
			}
			if tc.want != rewrite.PayloadOutcomeReplaced {
				if published != nil {
					t.Fatalf("outcome %v must publish nothing, got %q", tc.want, published)
				}
				if pass.Changed {
					t.Fatalf("outcome %v must not report a change", tc.want)
				}
			}
		})
	}
}

// TestApplySelectedValuesPreservesEveryUnselectedByte is the byte-splice
// property requirement 4.8 states for the inbound direction. A re-encode would
// normalize member order, number spelling, duplicate keys, escapes, whitespace,
// and empty-versus-null presence, so every one of those is asserted here.
func TestApplySelectedValuesPreservesEveryUnselectedByte(t *testing.T) {
	t.Parallel()

	payload := `{ "z" : 1e400 ,` +
		`"a":{"nested":"kept"},` +
		`"path":"/original",` +
		`"dup":"/kept","dup":"/also-kept",` +
		`"big":12345678901234567890,` +
		`"neg":-0,"frac":0.0,` +
		`"esc":"tab\there é",` +
		`"empty":"","null":null,` +
		`"content":"/original inside prose",` +
		`"list":["/original","/untouched"] }`

	published, pass, err := rewrite.ApplySelectedValues(
		[]byte(payload), selectedPointers(t, "/path"), prefixDecider())
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Outcome != rewrite.PayloadOutcomeReplaced {
		t.Fatalf("outcome=%v want replaced", pass.Outcome)
	}
	if !pass.Changed {
		t.Fatal("a replaced leaf must report a change")
	}
	if !json.Valid(published) {
		t.Fatalf("published document is not valid JSON: %q", published)
	}

	// Only the selected literal changed; every other byte is byte-identical.
	want := strings.Replace(payload, `"path":"/original"`, `"path":"X:/original"`, 1)
	if !bytes.Equal(published, []byte(want)) {
		t.Fatalf("published document is not a byte splice of the input:\n got %q\nwant %q", published, want)
	}
	// The unselected siblings keep their own bytes, including the duplicate key
	// and the content field, which is exactly requirement 4.9's surface.
	for _, kept := range []string{
		`"content":"/original inside prose"`,
		`"dup":"/kept","dup":"/also-kept"`,
		`"empty":"","null":null`,
		`"big":12345678901234567890`,
		`"neg":-0,"frac":0.0`,
		`"esc":"tab\there é"`,
		`"list":["/original","/untouched"]`,
	} {
		if !strings.Contains(string(published), kept) {
			t.Fatalf("published document lost unselected bytes %q", kept)
		}
	}
}

// TestApplySelectedValuesVisitsSelectedLeavesOnly proves the engine never
// reaches a value no selector named, even when that value is the only one the
// decision function would accept.
func TestApplySelectedValuesVisitsSelectedLeavesOnly(t *testing.T) {
	t.Parallel()

	payload := `{"content":"/selected-would-match","other":"/selected-would-match","path":"/selected-would-match"}`
	var visited []string
	published, pass, err := rewrite.ApplySelectedValues(
		[]byte(payload), selectedPointers(t, "/path"),
		func(value string) rewrite.ValueDecision {
			visited = append(visited, value)
			return rewrite.ValueDecision{Replacement: "X:" + value, Eligible: true}
		})
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Outcome != rewrite.PayloadOutcomeReplaced {
		t.Fatalf("outcome=%v want replaced", pass.Outcome)
	}
	if len(visited) != 1 || visited[0] != "/selected-would-match" {
		t.Fatalf("visited %v want exactly the selected leaf", visited)
	}
	if strings.Contains(string(published), `"content":"X:`) || strings.Contains(string(published), `"other":"X:`) {
		t.Fatalf("an unselected field was rewritten: %q", published)
	}
}

// TestApplySelectedValuesRefusalPublishesNothing is the engine half of Task
// 8.1's fail-closed obligation. A refusal must end the whole document: no
// partial expansion may be published, because a partially expanded argument
// document would reach the client with one real path and one live alias.
func TestApplySelectedValuesRefusalPublishesNothing(t *testing.T) {
	t.Parallel()

	pointers := selectedPointers(t, "/path", "/other")
	payload := `{"path":"/first","other":"/second"}`

	t.Run("refusal_on_the_first_leaf", func(t *testing.T) {
		t.Parallel()
		published, pass, err := rewrite.ApplySelectedValues([]byte(payload), pointers,
			func(string) rewrite.ValueDecision {
				return rewrite.ValueDecision{Refused: true}
			})
		if err != nil {
			t.Fatalf("ApplySelectedValues: %v", err)
		}
		if !pass.Refused {
			t.Fatal("a refused decision must set Refused")
		}
		if published != nil {
			t.Fatalf("a refusal must publish nothing, got %q", published)
		}
		if pass.Changed {
			t.Fatal("a refusal must not report a change")
		}
	})

	t.Run("refusal_after_an_earlier_replacement", func(t *testing.T) {
		t.Parallel()
		seen := 0
		published, pass, err := rewrite.ApplySelectedValues([]byte(payload), pointers,
			func(value string) rewrite.ValueDecision {
				seen++
				if seen == 1 {
					return rewrite.ValueDecision{Replacement: "X:" + value, Eligible: true}
				}
				return rewrite.ValueDecision{Refused: true}
			})
		if err != nil {
			t.Fatalf("ApplySelectedValues: %v", err)
		}
		if !pass.Refused {
			t.Fatal("a refused decision must set Refused")
		}
		if published != nil {
			t.Fatalf("a later refusal must discard the earlier replacement, got %q", published)
		}
	})
}

// TestApplySelectedValuesAccountsDecodedValueLengths pins the measurement basis
// requirement 9.5 asks for: byte totals are DECODED-VALUE lengths, so an
// escape-spelled path reports the length the model would see rather than the wire
// length. The outbound rewriter already measures this way; the inbound direction
// must not introduce a second basis.
func TestApplySelectedValuesAccountsDecodedValueLengths(t *testing.T) {
	t.Parallel()

	const value = "/some/path"
	escaped := `{"path":"/some` + `\u002f` + `path"}`
	published, pass, err := rewrite.ApplySelectedValues(
		[]byte(escaped), selectedPointers(t, "/path"), prefixDecider())
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Outcome != rewrite.PayloadOutcomeReplaced {
		t.Fatalf("outcome=%v want replaced", pass.Outcome)
	}
	if pass.Eligible != 1 || pass.Replaced != 1 {
		t.Fatalf("eligible=%d replaced=%d want 1/1", pass.Eligible, pass.Replaced)
	}
	if pass.BytesBefore != len(value) {
		t.Fatalf("BytesBefore=%d want the decoded length %d", pass.BytesBefore, len(value))
	}
	if pass.BytesAfter != len("X:"+value) {
		t.Fatalf("BytesAfter=%d want %d", pass.BytesAfter, len("X:"+value))
	}
	// The escaped literal is re-spelled canonically, so the wire bytes and the
	// decoded-value bytes are deliberately different numbers; the accounting must
	// report the latter.
	if strings.Contains(string(published), `\u002f`) {
		t.Fatalf("the replaced literal must be re-spelled canonically: %q", published)
	}
	if want := `{"path":"X:/some/path"}`; string(published) != want {
		t.Fatalf("published %q want %q", published, want)
	}
}

// TestApplySelectedValuesIneligibleLeafKeepsItsBytes proves a leaf the decision
// function refuses as not-a-location is left exactly as it arrived, so a selected
// array can hold a mixture of expanded and untouched values.
func TestApplySelectedValuesIneligibleLeafKeepsItsBytes(t *testing.T) {
	t.Parallel()

	payload := `{"path":"relative/not-absolute"}`
	published, pass, err := rewrite.ApplySelectedValues([]byte(payload), selectedPointers(t, "/path"),
		func(string) rewrite.ValueDecision { return rewrite.ValueDecision{} })
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Outcome != rewrite.PayloadOutcomeReplaced {
		t.Fatalf("outcome=%v want replaced", pass.Outcome)
	}
	if pass.Eligible != 0 || pass.Replaced != 0 {
		t.Fatalf("an ineligible leaf must contribute no counters: eligible=%d replaced=%d", pass.Eligible, pass.Replaced)
	}
	if pass.Changed || published != nil {
		t.Fatalf("an ineligible leaf must publish nothing: changed=%t published=%q", pass.Changed, published)
	}
}

// TestApplySelectedValuesReportsSelectorSkips proves the canonical selector
// refusals are forwarded verbatim, so both directions account for an unresolved
// location by the SAME bounded reason instead of inventing their own.
func TestApplySelectedValuesReportsSelectorSkips(t *testing.T) {
	t.Parallel()

	pointers := selectedPointers(t, "/absent", "/number", "/object", "/mixed")
	_, pass, err := rewrite.ApplySelectedValues(
		[]byte(`{"number":7,"object":{"a":1},"mixed":["/a",7]}`), pointers, prefixDecider())
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Outcome != rewrite.PayloadOutcomeNoLeaves {
		t.Fatalf("outcome=%v want no-leaves", pass.Outcome)
	}
	got := make([]pathvirtualization.SelectorSkip, 0, len(pass.Selection.Skipped))
	for _, skipped := range pass.Selection.Skipped {
		got = append(got, skipped.Reason)
	}
	want := []pathvirtualization.SelectorSkip{
		pathvirtualization.SelectorSkipUnresolved,
		pathvirtualization.SelectorSkipNotString,
		pathvirtualization.SelectorSkipObject,
		pathvirtualization.SelectorSkipNotStringArray,
	}
	if len(got) != len(want) {
		t.Fatalf("selector skips=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selector skips=%v want %v", got, want)
		}
	}
}

// TestApplySelectedValuesLimitedRefusesOnlyOverBoundDocuments pins the output bound
// [rewrite.ApplySelectedValuesLimited] documents: the bound is on the bytes the pass
// would publish, so a document whose published size is exactly the bound is still
// published, one byte past the bound is refused with [rewrite.ErrOutputOverLimit]
// and no document, a non-positive bound means no bound, and a pass already provably
// over the bound stops deciding the remaining selected leaves.
//
// The bound has exactly one live caller, the inbound expansion finalizer, which maps
// the error to its own bounded refusal reason and must never pay for the document it
// refuses (design.md "Path Expansion Finalizer" step 9; the caller-side cases are in
// expansion/finalizer_test.go). A bound that fired on a document it fits would reject
// valid expansions, and one that fired only after building the refused document would
// reopen the resource defect the bound exists to close, so both edges are pinned here
// against hard-coded bytes rather than against the pass's own arithmetic.
func TestApplySelectedValuesLimitedRefusesOnlyOverBoundDocuments(t *testing.T) {
	t.Parallel()

	// The decoded value is /a, the decider adds two bytes, and the published document
	// adds the literal's two quotes on top of the decoded length: the exact result is
	// 15 bytes, asserted literally below.
	const (
		payload   = `{"path":"/a"}`
		published = `{"path":"X:/a"}`
	)
	pointers := selectedPointers(t, "/path")

	t.Run("exactly_at_the_bound_is_published", func(t *testing.T) {
		t.Parallel()
		out, pass, err := rewrite.ApplySelectedValuesLimited([]byte(payload), pointers, prefixDecider(), len(published))
		if err != nil {
			t.Fatalf("ApplySelectedValuesLimited at the exact bound: %v", err)
		}
		if string(out) != published {
			t.Fatalf("published %q want %q", out, published)
		}
		if !pass.Changed || pass.Replaced != 1 {
			t.Fatalf("a published replacement must report the change: changed=%t replaced=%d",
				pass.Changed, pass.Replaced)
		}
	})

	t.Run("one_byte_past_the_bound_is_refused_without_a_document", func(t *testing.T) {
		t.Parallel()
		out, _, err := rewrite.ApplySelectedValuesLimited([]byte(payload), pointers, prefixDecider(), len(published)-1)
		if !errors.Is(err, rewrite.ErrOutputOverLimit) {
			t.Fatalf("err=%v want %v", err, rewrite.ErrOutputOverLimit)
		}
		if out != nil {
			t.Fatalf("a refused document must not be published, got %q", out)
		}
	})

	t.Run("a_non_positive_bound_means_no_bound", func(t *testing.T) {
		t.Parallel()
		out, _, err := rewrite.ApplySelectedValuesLimited([]byte(payload), pointers, prefixDecider(), 0)
		if err != nil {
			t.Fatalf("ApplySelectedValuesLimited with no bound: %v", err)
		}
		if string(out) != published {
			t.Fatalf("published %q want %q", out, published)
		}
	})

	t.Run("an_over_limit_pass_stops_deciding_remaining_leaves", func(t *testing.T) {
		t.Parallel()
		// The first selected leaf alone puts the pass's exact lower bound at
		// len(`{"first":`)+len("X:/a") = 13 bytes, so a bound of five is provably
		// exceeded after exactly one decision.
		decided := 0
		count := func(value string) rewrite.ValueDecision {
			decided++
			return rewrite.ValueDecision{Replacement: "X:" + value, Eligible: true}
		}
		out, _, err := rewrite.ApplySelectedValuesLimited(
			[]byte(`{"first":"/a","second":"/b"}`), selectedPointers(t, "/first", "/second"), count, 5)
		if !errors.Is(err, rewrite.ErrOutputOverLimit) {
			t.Fatalf("err=%v want %v", err, rewrite.ErrOutputOverLimit)
		}
		if out != nil {
			t.Fatalf("a refused document must not be published, got %q", out)
		}
		if decided != 1 {
			t.Fatalf("the pass decided %d leaves before refusing a document already over the bound, want 1", decided)
		}
	})
}

// TestApplySelectedValuesCountsArrayElementsSeparately pins per-element
// accounting for an array-of-string selector, which is what makes a mixed array
// report one eligible occurrence rather than a single opaque one.
func TestApplySelectedValuesCountsArrayElementsSeparately(t *testing.T) {
	t.Parallel()

	published, pass, err := rewrite.ApplySelectedValues(
		[]byte(`{"paths":["/a","/b","relative"]}`), selectedPointers(t, "/paths"),
		func(value string) rewrite.ValueDecision {
			if !strings.HasPrefix(value, "/") {
				return rewrite.ValueDecision{}
			}
			return rewrite.ValueDecision{Replacement: "X:" + value, Eligible: true}
		})
	if err != nil {
		t.Fatalf("ApplySelectedValues: %v", err)
	}
	if pass.Eligible != 2 || pass.Replaced != 2 {
		t.Fatalf("eligible=%d replaced=%d want 2/2", pass.Eligible, pass.Replaced)
	}
	want := `{"paths":["X:/a","X:/b","relative"]}`
	if string(published) != want {
		t.Fatalf("published %q want %q", published, want)
	}
}
