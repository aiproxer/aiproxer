package billsem

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ---------------------------------------------------------------------------
// Fixture helpers.
// ---------------------------------------------------------------------------

func key(name string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionNone,
		Component: name,
		Unit:      "unit",
		SchemaID:  "billsem.test",
	}
}

func keyDir(name string, dir metering.FlowDirection) metering.ComponentKey {
	k := key(name)
	k.Direction = dir
	return k
}

func keyUnit(name, unit string) metering.ComponentKey {
	k := key(name)
	k.Unit = unit
	return k
}

func subset(p, c string) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: metering.RelationshipSubset, Parent: key(p), Child: key(c)}
}

func completeRel(kind metering.RelationshipKind, p, c string, optional bool) metering.ComponentRelationship {
	return metering.ComponentRelationship{Kind: kind, Parent: key(p), Child: key(c), Optional: optional}
}

func part(p, c string) metering.ComponentRelationship {
	return completeRel(metering.RelationshipPartition, p, c, false)
}

func partOpt(p, c string) metering.ComponentRelationship {
	return completeRel(metering.RelationshipPartition, p, c, true)
}

func agg(p, c string) metering.ComponentRelationship {
	return completeRel(metering.RelationshipAggregate, p, c, false)
}

func schema(id string, rels ...metering.ComponentRelationship) metering.ComponentSchema {
	return metering.ComponentSchema{ID: id, Version: "v1", Relationships: rels}
}

func ensure(ev Evidence, scope string) {
	if ev.States[scope] == nil {
		ev.States[scope] = map[string]ObsState{}
	}
	if ev.Values[scope] == nil {
		ev.Values[scope] = map[string]*big.Rat{}
	}
}

func setExKey(ev Evidence, scope string, k metering.ComponentKey, v int64) {
	ensure(ev, scope)
	ev.States[scope][k.CanonicalKey()] = ObsExact
	ev.Values[scope][k.CanonicalKey()] = big.NewRat(v, 1)
}

func setUnKey(ev Evidence, scope string, k metering.ComponentKey) {
	ensure(ev, scope)
	ev.States[scope][k.CanonicalKey()] = ObsUnavailable
}

func setAbsentKey(ev Evidence, scope string, k metering.ComponentKey) {
	ensure(ev, scope)
	ev.States[scope][k.CanonicalKey()] = ObsAbsent
}

func ex(ev Evidence, scope, name string, v int64) { setExKey(ev, scope, key(name), v) }
func un(ev Evidence, scope, name string)          { setUnKey(ev, scope, key(name)) }
func absent(ev Evidence, scope, name string)      { setAbsentKey(ev, scope, key(name)) }

// ---------------------------------------------------------------------------
// Assertion helpers.
// ---------------------------------------------------------------------------

func solve(t *testing.T, schemas []metering.ComponentSchema, ev Evidence) []ScopeResult {
	t.Helper()
	res, err := Solve(schemas, ev)
	if err != nil {
		t.Fatalf("Solve returned error: %v", err)
	}
	return res
}

func scopeOf(t *testing.T, res []ScopeResult, scope string) ScopeResult {
	t.Helper()
	for _, sr := range res {
		if sr.Scope == scope {
			return sr
		}
	}
	t.Fatalf("scope %q missing from result", scope)
	return ScopeResult{}
}

func nodeOf(t *testing.T, sr ScopeResult, name string) NodeResult {
	t.Helper()
	return nodeOfKey(t, sr, key(name))
}

func nodeOfKey(t *testing.T, sr ScopeResult, k metering.ComponentKey) NodeResult {
	t.Helper()
	n, ok := sr.Nodes[k.CanonicalKey()]
	if !ok {
		t.Fatalf("node %q missing from scope %q", k.Component, sr.Scope)
	}
	return n
}

func hasKey(list []string, k metering.ComponentKey) bool {
	return slices.Contains(list, k.CanonicalKey())
}

func assertHasKey(t *testing.T, list []string, k metering.ComponentKey, what string) {
	t.Helper()
	if !hasKey(list, k) {
		t.Fatalf("%s: expected %q in %v", what, k.Component, list)
	}
}

func assertNoKey(t *testing.T, list []string, k metering.ComponentKey, what string) {
	t.Helper()
	if hasKey(list, k) {
		t.Fatalf("%s: did not expect %q in %v", what, k.Component, list)
	}
}

func assertRat(t *testing.T, got *big.Rat, want int64, what string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil rational, want %d", what, want)
	}
	if got.Cmp(big.NewRat(want, 1)) != 0 {
		t.Fatalf("%s: got %s, want %d", what, got.RatString(), want)
	}
}

func assertNoContradictions(t *testing.T, sr ScopeResult) {
	t.Helper()
	if len(sr.Contradicted) != 0 {
		t.Fatalf("scope %q: expected no contradictions, got %v", sr.Scope, sr.Contradicted)
	}
}

// canon serializes a result deterministically for order-independence checks.
func canon(res []ScopeResult) string {
	var b strings.Builder
	for _, sr := range res {
		fmt.Fprintf(&b, "scope=%s\n", sr.Scope)
		for _, k := range SortedKeys(sr.Nodes) {
			n := sr.Nodes[k]
			up := "+inf"
			if n.UpperFinite {
				up = n.Upper.RatString()
			}
			rep := "-"
			if n.Represented != nil {
				rep = n.Represented.RatString()
			}
			fmt.Fprintf(&b, "node=%s low=%s up=%s class=%s represented=%s absent=%v unavailable=%v\n",
				k, n.Lower.RatString(), up, n.Class.String(), rep, n.Absent, n.Unavailable)
		}
		fmt.Fprintf(&b, "contradicted=%s\n", strings.Join(sr.Contradicted, ","))
		fmt.Fprintf(&b, "incomplete=%s\n", strings.Join(sr.Incomplete, ","))
		fmt.Fprintf(&b, "ambiguous=%s\n", strings.Join(sr.Ambiguous, ","))
		pairs := make([]string, len(sr.UnknownIntersection))
		for i, p := range sr.UnknownIntersection {
			pairs[i] = p[0] + "|" + p[1]
		}
		fmt.Fprintf(&b, "unknown=%s\n", strings.Join(pairs, ","))
	}
	return b.String()
}

// canonEvidence serializes evidence without mutating it.
func canonEvidence(ev Evidence) string {
	var b strings.Builder
	for _, scope := range SortedKeys(ev.States) {
		for _, k := range SortedKeys(ev.States[scope]) {
			fmt.Fprintf(&b, "%s|%s|state=%d\n", scope, k, ev.States[scope][k])
		}
	}
	for _, scope := range SortedKeys(ev.Values) {
		for _, k := range SortedKeys(ev.Values[scope]) {
			v := ev.Values[scope][k]
			vs := "<nil>"
			if v != nil {
				vs = v.RatString()
			}
			fmt.Fprintf(&b, "%s|%s|value=%s\n", scope, k, vs)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Structural / quantity tests.
// ---------------------------------------------------------------------------

// TestDirectSubsetChildContradicted: A --subset--> B, A=10, B=20.
// Q(B) <= Q(A) requires 20 <= 10, false. B is contradicted.
func TestDirectSubsetChildContradicted(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "B", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("B"), "contradicted")
}

// TestSubsetChainTransitive: A --subset--> M --subset--> L, A=10, M absent, L=20.
// lower(A)=max(10,lower(M)=20)=20 and upper(L)=min(20,upper(M)=upper(A)=10)=10,
// so the leaf L (20 > 10) is contradicted transitively.
func TestSubsetChainTransitive(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "M"), subset("M", "L"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "L", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("L"), "contradicted")
}

// TestMixedSubsetPartitionTransitive: A --subset--> M --partition--> L,
// A=10, M absent, L=20. upper(L) <= upper(M) <= upper(A) = 10, so L contradicted.
func TestMixedSubsetPartitionTransitive(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "M"), part("M", "L"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "L", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("L"), "contradicted")
}

// TestKnownBaselineBlocker: A --subset--> B; B --partition--> {C,D}.
// A=30, B absent, C=20, D=20. The complete coverage forces B=40, but
// upper(B) <= upper(A) = 30, and upper(C) <= upper(B) - lower(D) = 30-20 = 10
// while lower(C) = 20. This is the case production historically passed as a
// false complete USD 40.
func TestKnownBaselineBlocker(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"), part("B", "C"), part("B", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 30)
	ex(ev, "s", "C", 20)
	ex(ev, "s", "D", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("C"), "contradicted")
	assertHasKey(t, sr.Contradicted, key("D"), "contradicted")
}

// TestKnownBaselineBlockerWithRequiredMissing: same as above plus a required
// member E absent. B is not exactly representable, yet the known lower bound
// 20+20+0 = 40 > 30 still contradicts the children.
func TestKnownBaselineBlockerWithRequiredMissing(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"), part("B", "C"), part("B", "D"), part("B", "E"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 30)
	ex(ev, "s", "C", 20)
	ex(ev, "s", "D", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("C"), "contradicted")
	assertHasKey(t, sr.Contradicted, key("D"), "contradicted")
}

// TestEqualBoundaryNotContradicted: A --subset--> B with A=B=10. 10 <= 10.
func TestEqualBoundaryNotContradicted(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "B", 10)
	assertNoContradictions(t, scopeOf(t, solve(t, schemas, ev), "s"))
}

// TestStrictContainmentNotContradicted: A --subset--> B with A=10, B=5.
func TestStrictContainmentNotContradicted(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "B", 5)
	assertNoContradictions(t, scopeOf(t, solve(t, schemas, ev), "s"))
}

// TestExactZeroIsNotAbsent: P --partition--> {C,D}, C=Exact(0), D=10, P=10.
// The exact zero is a real observed zero and contributes to the complete sum.
func TestExactZeroIsNotAbsent(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", part("P", "C"), part("P", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "P", 10)
	ex(ev, "s", "C", 0)
	ex(ev, "s", "D", 10)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	c := nodeOf(t, sr, "C")
	if c.Absent {
		t.Fatalf("Exact(0) must not be Absent")
	}
	assertRat(t, c.Lower, 0, "C.Lower")
	if !c.UpperFinite {
		t.Fatalf("C must have a finite upper bound")
	}
	assertRat(t, c.Upper, 0, "C.Upper")
	if c.Class != ClassRepresented {
		t.Fatalf("C class = %s, want represented", c.Class)
	}
	assertRat(t, c.Represented, 0, "C.Represented")
	p := nodeOf(t, sr, "P")
	if p.Class != ClassRepresented {
		t.Fatalf("P class = %s, want represented", p.Class)
	}
	assertRat(t, p.Represented, 10, "P.Represented")
}

// TestAbsentOptionalIsEdgeLocalZero: X is an optional complete child of P1 and
// a required complete child of P2, and X is absent. X contributes an edge-local
// zero to P1 (so P1 = 10 is exact) but stays missing for P2 (lower 0, upper
// +inf), so P2 is Incomplete. X itself never becomes a global zero.
func TestAbsentOptionalIsEdgeLocalZero(t *testing.T) {
	schemas := []metering.ComponentSchema{schema(
		"s",
		partOpt("P1", "X"), part("P1", "Y"),
		part("P2", "X"), part("P2", "Z"),
	)}
	ev := NewEvidence()
	ex(ev, "s", "P1", 10)
	ex(ev, "s", "Y", 10)
	ex(ev, "s", "P2", 5)
	ex(ev, "s", "Z", 5)
	sr := scopeOf(t, solve(t, schemas, ev), "s")

	x := nodeOf(t, sr, "X")
	if !x.Absent {
		t.Fatalf("X must remain absent")
	}
	assertRat(t, x.Lower, 0, "X.Lower")
	if x.UpperFinite {
		t.Fatalf("X must not become a global zero; upper = %s", x.Upper.RatString())
	}
	p1 := nodeOf(t, sr, "P1")
	if p1.Class != ClassRepresented {
		t.Fatalf("P1 class = %s, want represented", p1.Class)
	}
	assertRat(t, p1.Represented, 10, "P1.Represented")
	p2 := nodeOf(t, sr, "P2")
	if p2.Class != ClassIncomplete {
		t.Fatalf("P2 class = %s, want incomplete", p2.Class)
	}
}

// TestAbsentRequiredIncomplete: P --partition--> {C,D}, C=10, D required absent,
// P=10. The absent required member is unknown, not wrong: P is Incomplete and
// there is no contradiction.
func TestAbsentRequiredIncomplete(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", part("P", "C"), part("P", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "P", 10)
	ex(ev, "s", "C", 10)
	absent(ev, "s", "D")
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertNoContradictions(t, sr)
	p := nodeOf(t, sr, "P")
	if p.Class != ClassIncomplete {
		t.Fatalf("P class = %s, want incomplete", p.Class)
	}
}

// TestNestedCompleteSumExceedsAncestor: A --subset--> B --partition--> C
// --partition--> D, with A=10 and D=20. The nested sums force C=D=20, B=20 and
// upper(A)=10, so the chain is contradicted.
func TestNestedCompleteSumExceedsAncestor(t *testing.T) {
	schemas := []metering.ComponentSchema{schema(
		"s",
		subset("A", "B"), part("B", "C"), part("C", "D"),
	)}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "D", 20)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("D"), "contradicted")
	assertHasKey(t, sr.Contradicted, key("A"), "contradicted")
}

// TestUnavailableIsNeverZero: P --partition--> {C,D}, C=10, D unavailable, P=10.
// P is Incomplete; D keeps the [0, +inf) interval rather than collapsing to 0.
func TestUnavailableIsNeverZero(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", part("P", "C"), part("P", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "P", 10)
	ex(ev, "s", "C", 10)
	un(ev, "s", "D")
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertNoContradictions(t, sr)
	p := nodeOf(t, sr, "P")
	if p.Class != ClassIncomplete {
		t.Fatalf("P class = %s, want incomplete", p.Class)
	}
	d := nodeOf(t, sr, "D")
	if !d.Unavailable {
		t.Fatalf("D must be marked unavailable")
	}
	if d.UpperFinite {
		t.Fatalf("D must keep upper = +inf, got %s", d.Upper.RatString())
	}
	assertRat(t, d.Lower, 0, "D.Lower")
	if d.Class != ClassIncomplete {
		t.Fatalf("D class = %s, want incomplete", d.Class)
	}
}

// ---------------------------------------------------------------------------
// Negative quantity tests.
// ---------------------------------------------------------------------------

// TestNegativeExactContradiction: Exact(-1) with no correction marker is a
// structural contradiction. lower is clamped to 0 while upper stays -1.
func TestNegativeExactContradiction(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s")}
	ev := NewEvidence()
	ex(ev, "s", "A", -1)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("A"), "contradicted")
	a := nodeOf(t, sr, "A")
	if a.Class != ClassContradicted {
		t.Fatalf("A class = %s, want contradicted", a.Class)
	}
	if a.Absent {
		t.Fatalf("negative exact is reported, not absent")
	}
	if a.Represented != nil {
		t.Fatalf("contradicted node must not be represented")
	}
}

// TestNegativeInsideCompleteSum: P --partition--> {C,D}, C=-1, D=10, P=9.
// The negative child and the parent are both contradicted.
func TestNegativeInsideCompleteSum(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", part("P", "C"), part("P", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "P", 9)
	ex(ev, "s", "C", -1)
	ex(ev, "s", "D", 10)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertHasKey(t, sr.Contradicted, key("C"), "contradicted")
	assertHasKey(t, sr.Contradicted, key("P"), "contradicted")
}

// ---------------------------------------------------------------------------
// Non-containment / isolation tests.
// ---------------------------------------------------------------------------

// TestTransformIsNotContainment: A --transform--> B, A=10, B=20 is not a
// containment relation and must not be contradicted.
func TestTransformIsNotContainment(t *testing.T) {
	rel := metering.ComponentRelationship{Kind: metering.RelationshipTransform, Parent: key("A"), Child: key("B")}
	schemas := []metering.ComponentSchema{schema("s", rel)}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "B", 20)
	assertNoContradictions(t, scopeOf(t, solve(t, schemas, ev), "s"))
}

// TestCrossDirectionIsNotContainment: a subset edge between different economic
// directions is ignored entirely.
func TestCrossDirectionIsNotContainment(t *testing.T) {
	rel := metering.ComponentRelationship{
		Kind:   metering.RelationshipSubset,
		Parent: keyDir("A", metering.DirectionInput),
		Child:  keyDir("B", metering.DirectionOutput),
	}
	schemas := []metering.ComponentSchema{schema("s", rel)}
	ev := NewEvidence()
	setExKey(ev, "s", keyDir("A", metering.DirectionInput), 10)
	setExKey(ev, "s", keyDir("B", metering.DirectionOutput), 20)
	assertNoContradictions(t, scopeOf(t, solve(t, schemas, ev), "s"))
}

// TestCrossUnitIsNotContainment: a cross-unit edge can only be expressed as a
// transform (the public Validate rejects non-transform unequal units), and a
// transform is never traversed, so it is not containment.
func TestCrossUnitIsNotContainment(t *testing.T) {
	rel := metering.ComponentRelationship{
		Kind:   metering.RelationshipTransform,
		Parent: keyUnit("A", "unit"),
		Child:  keyUnit("B", "other"),
	}
	schemas := []metering.ComponentSchema{schema("s", rel)}
	ev := NewEvidence()
	setExKey(ev, "s", keyUnit("A", "unit"), 10)
	setExKey(ev, "s", keyUnit("B", "other"), 20)
	assertNoContradictions(t, scopeOf(t, solve(t, schemas, ev), "s"))
}

// TestScopesAreIndependent: identical graphs in scope1 and scope2. Evidence in
// one scope never leaks into the other.
func TestScopesAreIndependent(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"))}
	ev := NewEvidence()
	ex(ev, "scope1", "A", 10)
	ex(ev, "scope1", "B", 20)
	ex(ev, "scope2", "A", 10)
	ex(ev, "scope2", "B", 10)

	res := solve(t, schemas, ev)
	if len(res) != 2 {
		t.Fatalf("expected 2 scopes, got %d", len(res))
	}
	s1 := scopeOf(t, res, "scope1")
	assertHasKey(t, s1.Contradicted, key("B"), "scope1 contradicted")
	assertNoContradictions(t, scopeOf(t, res, "scope2"))
}

// ---------------------------------------------------------------------------
// Determinism / independence tests.
// ---------------------------------------------------------------------------

// TestDeclarationOrderIndependence: permuting the schema slice and the
// relationship slices must not change the result.
func TestDeclarationOrderIndependence(t *testing.T) {
	order1 := []metering.ComponentSchema{
		schema("s1", subset("A", "B"), part("B", "C"), part("B", "D")),
		schema("s2", subset("E", "C")),
	}
	order2 := []metering.ComponentSchema{
		schema("s2", subset("E", "C")),
		schema("s1", part("B", "D"), part("B", "C"), subset("A", "B")),
	}
	ev := NewEvidence()
	ex(ev, "s", "A", 100)
	ex(ev, "s", "C", 30)
	ex(ev, "s", "D", 30)
	ex(ev, "s", "E", 10)

	r1 := canon(solve(t, order1, ev))
	r2 := canon(solve(t, order2, ev))
	if r1 != r2 {
		t.Fatalf("declaration order changed result:\n--- order1 ---\n%s\n--- order2 ---\n%s", r1, r2)
	}
}

// TestCollapsedSubsetBoundsAreNotRepresented: P --subset--> C --subset--> D,
// P=10, C absent, D=10. Propagation collapses C to [10,10], but a collapsed
// subset interval is a validation device, not an observation, so C must not be
// RepresentedExact.
func TestCollapsedSubsetBoundsAreNotRepresented(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("P", "C"), subset("C", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "P", 10)
	ex(ev, "s", "D", 10)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	c := nodeOf(t, sr, "C")
	assertRat(t, c.Lower, 10, "C.Lower")
	if !c.UpperFinite {
		t.Fatalf("C.Upper should be finite")
	}
	assertRat(t, c.Upper, 10, "C.Upper")
	if c.Represented != nil {
		t.Fatalf("collapsed bounds must not be Represented, got %s", c.Represented.RatString())
	}
	if c.Class == ClassRepresented {
		t.Fatalf("collapsed bounds class = %s, want not represented", c.Class)
	}
}

// TestExactCompleteDerivation: (a) with all members exact, P is Represented=10
// even though P itself is absent; (b) making one member unavailable removes the
// represented value and yields Incomplete.
func TestExactCompleteDerivation(t *testing.T) {
	schemasA := []metering.ComponentSchema{schema("s", agg("P", "C"), agg("P", "D"))}
	evA := NewEvidence()
	ex(evA, "s", "C", 3)
	ex(evA, "s", "D", 7)
	pa := nodeOf(t, scopeOf(t, solve(t, schemasA, evA), "s"), "P")
	if pa.Class != ClassRepresented {
		t.Fatalf("(a) P class = %s, want represented", pa.Class)
	}
	assertRat(t, pa.Represented, 10, "(a) P.Represented")

	schemasB := []metering.ComponentSchema{schema("s", part("P", "C"), part("P", "D"))}
	evB := NewEvidence()
	ex(evB, "s", "C", 3)
	un(evB, "s", "D")
	pb := nodeOf(t, scopeOf(t, solve(t, schemasB, evB), "s"), "P")
	if pb.Represented != nil {
		t.Fatalf("(b) P must not be represented, got %s", pb.Represented.RatString())
	}
	if pb.Class != ClassIncomplete {
		t.Fatalf("(b) P class = %s, want incomplete", pb.Class)
	}
}

// TestAmbiguousOwnership: X has two complete-coverage parents. X is reported
// Ambiguous, not Contradicted.
func TestAmbiguousOwnership(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", part("P1", "X"), part("P2", "X"))}
	ev := NewEvidence()
	ex(ev, "s", "X", 5)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertNoContradictions(t, sr)
	assertHasKey(t, sr.Ambiguous, key("X"), "ambiguous")
	x := nodeOf(t, sr, "X")
	if x.Class != ClassAmbiguous {
		t.Fatalf("X class = %s, want ambiguous", x.Class)
	}
}

// TestUnknownIntersection: two sibling subsets sharing a parent are reported,
// but two subsets under different complete branches of one partition are not.
func TestUnknownIntersection(t *testing.T) {
	// Sibling subsets under A: reported.
	schemas := []metering.ComponentSchema{schema("s", subset("A", "S1"), subset("A", "S2"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 10)
	ex(ev, "s", "S1", 5)
	ex(ev, "s", "S2", 3)
	sr := scopeOf(t, solve(t, schemas, ev), "s")
	assertNoContradictions(t, sr)
	if len(sr.UnknownIntersection) != 1 {
		t.Fatalf("expected 1 unknown intersection, got %v", sr.UnknownIntersection)
	}
	want := [2]string{key("S1").CanonicalKey(), key("S2").CanonicalKey()}
	a, b := want[0], want[1]
	if a > b {
		a, b = b, a
	}
	if sr.UnknownIntersection[0] != [2]string{a, b} {
		t.Fatalf("unknown intersection = %v, want [%s %s]", sr.UnknownIntersection, a, b)
	}

	// Separated by a validated complete partition: not reported.
	schemas2 := []metering.ComponentSchema{schema(
		"s",
		part("P", "B1"), part("P", "B2"),
		subset("B1", "S1"), subset("B2", "S2"),
	)}
	ev2 := NewEvidence()
	ex(ev2, "s", "P", 14)
	ex(ev2, "s", "B1", 10)
	ex(ev2, "s", "S1", 5)
	ex(ev2, "s", "B2", 4)
	ex(ev2, "s", "S2", 2)
	sr2 := scopeOf(t, solve(t, schemas2, ev2), "s")
	assertNoContradictions(t, sr2)
	if len(sr2.UnknownIntersection) != 0 {
		t.Fatalf("expected no unknown intersection across branches, got %v", sr2.UnknownIntersection)
	}
}

// TestIdempotenceNoMutation: solving twice gives equal results and leaves the
// input evidence untouched.
func TestIdempotenceNoMutation(t *testing.T) {
	schemas := []metering.ComponentSchema{schema("s", subset("A", "B"), part("B", "C"), part("B", "D"))}
	ev := NewEvidence()
	ex(ev, "s", "A", 30)
	ex(ev, "s", "C", 20)
	ex(ev, "s", "D", 20)
	un(ev, "s", "E")

	schemasBefore := fmt.Sprintf("%v", schemas)
	evBefore := canonEvidence(ev)

	r1 := solve(t, schemas, ev)
	r2 := solve(t, schemas, ev)
	if canon(r1) != canon(r2) {
		t.Fatalf("Solve is not idempotent")
	}
	if canonEvidence(ev) != evBefore {
		t.Fatalf("Solve mutated the evidence")
	}
	if fmt.Sprintf("%v", schemas) != schemasBefore {
		t.Fatalf("Solve mutated the schemas")
	}
}
