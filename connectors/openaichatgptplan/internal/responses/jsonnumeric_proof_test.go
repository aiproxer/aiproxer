package responses_test

// Focused proofs for the numeric equivalence rule of the wire-document
// comparison.
//
// The proofs are self-contained on purpose: they use their own documents and
// their own literals, so they keep proving what they prove even when the
// contract test file around them is replaced.
//
// The bar for every proof here is the same as the bar for a fixture: a proof
// that cannot fail for the reason it names is not evidence.
//
// The numeric proofs use math/big.Rat as an INDEPENDENT oracle for the exact
// value of a literal. The oracle computes the value from the literal's own
// digits and exponent, without any of the shifting the rule under test
// performs, so the expectation is not a mirror of the implementation. An
// expected value derived from the constant under test would prove nothing.
import (
	"encoding/json"
	"math/big"
	"testing"
)

// TestCanonicalJSON_KeepsLargeIntegersDistinct proves the contract's
// comparison does not alias two large integers that differ.
//
// A decoder into float64 rounds 9007199254740993 into 9007199254740992, so
// a comparison built on it would call two different documents equal. The
// proof exercises the contract's own comparison entry point, because the
// contract is what the rule is for: a rule that is exact but unused would
// not keep the contract exact.
func TestCanonicalJSON_KeepsLargeIntegersDistinct(t *testing.T) {
	t.Parallel()
	larger := canonicalJSON(t, "larger", []byte(`{"maximum":9007199254740993}`))
	smaller := canonicalJSON(t, "smaller", []byte(`{"maximum":9007199254740992}`))
	if larger == smaller {
		t.Errorf("two different large integers must not compare equal, both rendered as %s", larger)
	}
}

// TestCanonicalJSON_KeepsSchemaNumbersComparable proves the numbers a real
// tool schema carries still compare equal.
//
// The rule is numeric, so a schema that writes "1" and a projection that
// writes "1.0" are the same value, and a rule that rejected a legitimate
// golden would be as bad as one that aliased two values.
func TestCanonicalJSON_KeepsSchemaNumbersComparable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		left     string
		right    string
		wantSame bool
	}{
		{name: "the same integer", left: `{"maximum":42}`, right: `{"maximum":42}`, wantSame: true},
		{name: "an integer and its decimal spelling", left: `{"maximum":1}`, right: `{"maximum":1.0}`, wantSame: true},
		{name: "an integer and its exponent spelling", left: `{"maximum":1}`, right: `{"maximum":1e0}`, wantSame: true},
		{name: "zero and its signed spelling", left: `{"minimum":0}`, right: `{"minimum":-0}`, wantSame: true},
		{name: "two different integers", left: `{"maximum":41}`, right: `{"maximum":42}`, wantSame: false},
		{name: "two different large integers", left: `{"maximum":9007199254740992}`, right: `{"maximum":9007199254740993}`, wantSame: false},
		{name: "two different fractions", left: `{"multipleOf":0.1}`, right: `{"multipleOf":0.2}`, wantSame: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			left := canonicalJSON(t, "left", []byte(tc.left))
			right := canonicalJSON(t, "right", []byte(tc.right))
			if same := left == right; same != tc.wantSame {
				t.Errorf("the two documents compare %v, want %v: %s and %s", same, tc.wantSame, left, right)
			}
		})
	}
}

// TestCanonicalNumberLiteral_KeepsTheExactValue proves the rule itself
// keeps the exact value of a literal, including the ones that a float64
// cannot hold.
//
// Every normalised literal must also be a JSON number: the comparison
// renders the document the literal belongs to, so a literal that cannot be
// marshalled breaks the comparison itself instead of comparing it.
func TestCanonicalNumberLiteral_KeepsTheExactValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		literal string
		want    string
	}{
		{literal: "1", want: "1"},
		{literal: "1.0", want: "1"},
		{literal: "1e0", want: "1"},
		{literal: "1e2", want: "100"},
		{literal: "1.5e1", want: "15"},
		{literal: "1e-3", want: "0.001"},
		{literal: "0.10", want: "0.1"},
		{literal: "0", want: "0"},
		{literal: "-0", want: "0"},
		{literal: "-1.50", want: "-1.5"},
		{literal: "9007199254740993", want: "9007199254740993"},
		// A zero integer part with a positive exponent moves the decimal
		// point past the leading zero of the integer part. The value is
		// right, but the leading zero must not survive: "050" is not a
		// valid JSON number, so the comparison could not render the
		// document it belongs to.
		{literal: "0.5e2", want: "50"},
		{literal: "0.1e2", want: "10"},
		{literal: "0e5", want: "0"},
		{literal: "-0e5", want: "0"},
		{literal: "0.12345e2", want: "12.345"},
		{literal: "0.001e1", want: "0.01"},
	}
	for _, tc := range cases {
		got, ok := canonicalNumberLiteral(tc.literal)
		if !ok {
			t.Errorf("the literal %q must be normalised, it was refused", tc.literal)
			continue
		}
		if got != tc.want {
			t.Errorf("the literal %q normalises to %q, want %q", tc.literal, got, tc.want)
		}
		if _, err := json.Marshal(json.Number(got)); err != nil {
			t.Errorf("the normalised literal %q (from %q) is not a valid JSON number: %v", got, tc.literal, err)
		}
	}
}

// TestCanonicalJSON_ComparesEquivalentExponentSpellings proves the
// contract's own comparison accepts two documents that spell the same
// number differently.
//
// The comparison renders both documents, so a normalised literal that is
// not a valid JSON number does not only compare wrongly - it makes the
// comparison unable to render the document at all. The proof goes through
// the contract's entry point because the contract is what the rule is for:
// a rule that is exact but unused would not keep the contract exact.
func TestCanonicalJSON_ComparesEquivalentExponentSpellings(t *testing.T) {
	t.Parallel()
	left := canonicalJSON(t, "left", []byte(`{"maximum":50}`))
	right := canonicalJSON(t, "right", []byte(`{"maximum":0.5e2}`))
	if left != right {
		t.Errorf("the two documents must compare equal, they denote the same value:\n%s\n%s", left, right)
	}
}

// numericCorpus is a bounded corpus of the number literals the rule must
// handle exactly.
//
// It is hand-picked and small on purpose: it covers the spellings a tool
// schema carries, the spellings a decimal shifting can break, the values
// around the float64 boundary and the large integers a float64 cannot
// hold. It is not a fuzz corpus and it does not grow into one.
var numericCorpus = []string{
	"0",
	"-0",
	"0.0",
	"-0.0",
	"1",
	"-1",
	"1.0",
	"1.00",
	"10",
	"100",
	"0.1",
	"0.01",
	"0.001",
	"0.0001",
	"1.5",
	"2.25",
	"12.345",
	"1e0",
	"1e1",
	"1e2",
	"1e3",
	"1e-1",
	"1e-2",
	"1e-3",
	"1.5e1",
	"1.5e2",
	"1.5e-1",
	"0.5e2",
	"0.1e2",
	"0e5",
	"-0e5",
	"0.12345e2",
	"0.001e1",
	"0.05e1",
	"0.05e-1",
	"9007199254740992",
	"9007199254740993",
	"9007199254740993.0",
	"123456789012345678901234567890",
	"1e-10",
	"1e10",
}

// equivalentLiteralGroups are the spellings of ONE value.
//
// Every member of a group denotes the same exact value, and every member
// must normalise to the SAME literal: a rule that renders two spellings of
// one value differently would compare two equal documents as different.
var equivalentLiteralGroups = [][]string{
	{"50", "0.5e2", "50.0", "5e1", "500e-1", "0.500e2"},
	{"10", "0.1e2", "10.0", "1e1", "100e-1"},
	{"0", "-0", "0.0", "-0.0", "0e5", "-0e5", "0e-5"},
	{"12.345", "0.12345e2", "12345e-3", "12.3450"},
	{"0.01", "0.001e1", "1e-2", "0.010"},
	{"0.1", "0.10", "1e-1", "0.01e1"},
	{"1", "1.0", "1e0", "0.1e1", "10e-1", "1.00"},
	{"0.001", "1e-3", "0.1e-2", "0.0010"},
	{"9007199254740993", "9007199254740993.0", "9007199254740993e0"},
}

// TestCanonicalNumberLiteral_MatchesAnIndependentExactOracle proves the
// normalised literal is a valid JSON number that denotes the SAME exact
// value as the literal it was made from.
//
// The oracle is math/big.Rat: it parses the literal into an exact rational
// on its own, so the expectation does not come from the shifting the rule
// under test performs. The corpus is bounded and hand-picked.
func TestCanonicalNumberLiteral_MatchesAnIndependentExactOracle(t *testing.T) {
	t.Parallel()
	for _, literal := range numericCorpus {
		got, ok := canonicalNumberLiteral(literal)
		if !ok {
			t.Errorf("the literal %q must be normalised, it was refused", literal)
			continue
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("the normalised literal %q (from %q) is not a valid JSON number", got, literal)
		}
		if _, err := json.Marshal(json.Number(got)); err != nil {
			t.Errorf("the normalised literal %q (from %q) cannot be marshalled: %v", got, literal, err)
		}
		want := exactValue(t, literal)
		have := exactValue(t, got)
		if have.Cmp(want) != 0 {
			t.Errorf("the normalised literal %q (from %q) denotes %s, want %s", got, literal, have.RatString(), want.RatString())
		}
	}
}

// TestCanonicalNumberLiteral_NormalisesEqualValuesIdentically proves two
// spellings of the same value normalise to the same literal.
//
// The oracle checks the premise of every group first: its members must
// denote one value. A group whose members denote different values would
// make the assertion meaningless.
func TestCanonicalNumberLiteral_NormalisesEqualValuesIdentically(t *testing.T) {
	t.Parallel()
	for _, group := range equivalentLiteralGroups {
		want := exactValue(t, group[0])
		for _, literal := range group[1:] {
			if got := exactValue(t, literal); got.Cmp(want) != 0 {
				t.Fatalf("the corpus group %v does not denote one value: %s and %s differ", group, want.RatString(), got.RatString())
			}
		}
		normalised := ""
		for _, literal := range group {
			got, ok := canonicalNumberLiteral(literal)
			if !ok {
				t.Fatalf("the literal %q must be normalised, it was refused", literal)
			}
			if normalised == "" {
				normalised = got
				continue
			}
			if got != normalised {
				t.Errorf("the spellings %q and %q denote one value but normalise to %q and %q", group[0], literal, normalised, got)
			}
		}
	}
}

// exactValue parses a decimal literal into an exact rational.
//
// math/big.Rat is the independent oracle of the numeric proofs: it
// computes the value from the literal's own digits and exponent.
func exactValue(t *testing.T, literal string) *big.Rat {
	t.Helper()
	value, ok := new(big.Rat).SetString(literal)
	if !ok {
		t.Fatalf("the literal %q is not a decimal number the oracle can parse", literal)
	}
	return value
}
