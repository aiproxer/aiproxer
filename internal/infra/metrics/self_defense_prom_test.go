package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// The three finite self-defense series this feature owns. There is no fourth
// bounded self-defense metric and no label beyond "reason". The unprefixed name
// is what the collector declares; prometheus.CounterOpts.Namespace prepends the
// shared "aip_" namespace at registration time.
//
// These are the EXPECTED families, not the selector: the static guard below
// selects an in-scope file by the shared aip_self_defense_ metric-name prefix, so
// a new self-defense metric added by a later change is in scope for the guard
// without this table being updated first.
const (
	metricDenials     = "aip_self_defense_denials_total"
	metricTransitions = "aip_self_defense_quarantine_transitions_total"
	metricEntries     = "aip_self_defense_state_entries"
)

// selfDefenseMetricPrefix is the fully-qualified metric-name prefix that scopes
// the label-safety guard. Every collector whose fq name begins with it is part of
// the bounded self-defense exposition and must therefore be label-safe, whichever
// file declares it and whichever metric name it chooses. It is derived from the
// production namespace so a namespace change keeps the guard aligned.
const selfDefenseMetricPrefix = namespace + "_self_defense_"

// selfDefenseDeclaredMetricPrefix is the same prefix as it appears inside a
// prometheus opts literal, where the shared namespace is supplied separately as
// the Namespace field and joined with an underscore at registration time.
const selfDefenseDeclaredMetricPrefix = "self_defense_"

// selfDefenseLabelNameAllowList is the complete finite set of label names the
// self-defense series may declare. Requirement 9.4 forbids labelling by source
// IP, CIDR text, path, query, header, User-Agent, credential, principal,
// country, or any other attacker-controlled value, so "reason" is the only name
// a descriptor may carry.
var selfDefenseLabelNameAllowList = map[string]bool{"reason": true}

// selfDefenseClosedReasonValues is the complete finite set of reason label
// VALUES the owned counters may ever emit: the closed ingressdefense
// vocabulary plus the constant "unknown" bucket. Requirement 9.4 forbids an
// arbitrary attacker-controlled value, and a source address smuggled into the
// reason VALUE is the same violation as one smuggled into a label NAME.
var selfDefenseClosedReasonValues = func() map[string]bool {
	out := map[string]bool{unknownSelfDefenseReason: true}
	for _, r := range ingressdefense.AllReasons() {
		out[string(r)] = true
	}
	return out
}()

// selfDefenseForbiddenLabelTokens are the attacker-controlled label concepts
// requirement 9.4 names. They are matched case-insensitively against every
// string literal in a self-defense metrics file, so a forbidden label name
// cannot even be introduced as dead code.
var selfDefenseForbiddenLabelTokens = []string{
	"addr", "agent", "body", "cidr", "client_ip", "country", "credential",
	"header", "path", "prefix", "principal", "query", "remote_ip", "secret",
	"source_ip", "token", "user_agent",
}

// selfDefenseHostileValues is the attacker-controlled battery used to prove the
// emitted labels cannot carry request material. None of these values may appear
// in the exposition or in any label value.
var selfDefenseHostileValues = []string{
	"203.0.113.7",
	"2001:db8::dead:beef",
	"198.51.100.0/24",
	"/.env",
	"/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php",
	"?q=UNION+SELECT+password+FROM+users",
	"User-Agent: sqlmap/1.7",
	"Authorization: Bearer sk-live-SECRETVALUE",
	"principal=admin@example.test",
	"RU",
	"CN",
	"BY",
}

// selfDefenseEntrySource adapts a bounded adaptive state to the scrape-driven
// entry-count gauge source. The single PRODUCTION adapter is
// runtimebundle.ingressDefenseEntryCount, which owns the real process-owned
// state; this test-local copy exists only because the metrics package cannot
// import its own composition root, and it carries no production behaviour.
func selfDefenseEntrySource(state *ingressdefense.State) func() int {
	return func() int {
		if state == nil {
			return 0
		}
		return state.Len()
	}
}

// selfDefenseTestRegistry registers the collector on a private registry. Only
// the totality and vocabulary tests may use it: every label-safety assertion
// must measure the real process bundle registry, because a private registry
// cannot see a collector that production registers on the bundle.
func selfDefenseTestRegistry(t *testing.T) (*prometheus.Registry, *SelfDefenseProm) {
	t.Helper()
	reg := prometheus.NewRegistry()
	prom := RegisterSelfDefenseProm(reg)
	if prom == nil {
		t.Fatal("RegisterSelfDefenseProm returned nil")
	}
	return reg, prom
}

// selfDefenseBundle builds the real process metrics bundle. Every label-safety
// assertion is measured against b.Registry, never against a private registry.
func selfDefenseBundle(t *testing.T) *Bundle {
	t.Helper()
	b := NewBundle(nil, nil, selfDefenseEntrySource(nil))
	if b == nil || b.Registry == nil || b.SelfDefense == nil {
		t.Fatal("NewBundle must own a registry and the self-defense collector")
	}
	return b
}

// selfDefenseGatheredFamilies returns the self-defense families visible to
// Gather. Gather is the only surface that also sees an unchecked collector (one
// with an empty Describe), so it is the surface that cannot be evaded.
func selfDefenseGatheredFamilies(t *testing.T, g prometheus.Gatherer) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, f := range mfs {
		if strings.HasPrefix(f.GetName(), selfDefenseMetricPrefix) {
			out[f.GetName()] = f
		}
	}
	return out
}

// selfDefenseCollectorSource is the registry surface the label-safety guard
// measures: Gather (which also sees unchecked collectors) and Describe (which
// pins the declared label names). *prometheus.Registry satisfies both.
type selfDefenseCollectorSource interface {
	prometheus.Gatherer
	Describe(chan<- *prometheus.Desc)
}

// selfDefenseOwnedFamilies is the complete set of self-defense families the
// process bundle may expose. A family outside this set is a spec deviation, not
// an addition.
var selfDefenseOwnedFamilies = map[string]bool{
	metricDenials: true, metricTransitions: true, metricEntries: true,
}

// selfDefenseOwnedFamilyLabels is the exact variable-label set each owned family
// must declare. The two counters carry the single closed-vocabulary "reason"
// label; the entry-count gauge is label-free.
var selfDefenseOwnedFamilyLabels = map[string][]string{
	metricDenials: {"reason"}, metricTransitions: {"reason"}, metricEntries: {},
}

// selfDefenseDescribedLabels returns the declared const and variable label names
// of every self-defense descriptor visible to Describe, keyed by fq name.
// Describe is blind to unchecked collectors, so it complements rather than
// replaces Gather.
func selfDefenseDescribedLabels(g selfDefenseCollectorSource) (map[string][]string, map[string][]string) {
	ch := make(chan *prometheus.Desc, 1024)
	go func() {
		defer close(ch)
		g.Describe(ch)
	}()
	consts := map[string][]string{}
	vars := map[string][]string{}
	for desc := range ch {
		fq, c, v := parseDescLabels(desc.String())
		if fq == "" || !strings.HasPrefix(fq, selfDefenseMetricPrefix) {
			continue
		}
		consts[fq] = c
		vars[fq] = v
	}
	return consts, vars
}

// selfDefenseBundleLabelFindings is the label-safety guard as a pure predicate.
// It returns one finding per violation on the registry it is given:
//
//   - through Gather: no family outside the three owned ones may be published, no
//     emitted series may carry a label name outside {"reason"}, and every emitted
//     "reason" VALUE must be inside the closed vocabulary. Gather is the only
//     surface that also sees an unchecked (Describe-invisible) collector, and it
//     is the only surface that sees emitted values at all, so this is the half
//     that cannot be evaded and the half that pins the reason VALUE domain — a
//     source address smuggled into a reason value is caught here even though the
//     label name is the allowed "reason";
//   - through Describe: exactly the three owned families must be described — no
//     fourth, none missing — each with exactly the expected variable-label set
//     and no const label. Describe is the only surface that shows a declared
//     arity, so this is the half that pins the shape.
//
// Measuring the invariant on both surfaces is deliberate: neither alone is
// sufficient, because a collector with an empty Describe is invisible to the
// descriptor check while a vec with no observation yet is invisible to Gather. An
// empty result is the proof; callers turn findings into failures.
//
// KNOWN LIMITATION: the value-domain check runs only for families under the
// aip_self_defense_ name prefix. A self-defense series RENAMED out of that prefix,
// or published from a file carrying no self-defense metric name, publishes its
// attacker-controlled value under a name this guard never inspects. Closing that
// would require a whole-bundle inventory ratchet, which is out of scope here.
func selfDefenseBundleLabelFindings(g selfDefenseCollectorSource) []string {
	var out []string
	mfs, err := g.Gather()
	if err != nil {
		return append(out, "self-defense bundle registry did not gather: "+err.Error())
	}
	for _, f := range mfs {
		if !strings.HasPrefix(f.GetName(), selfDefenseMetricPrefix) {
			continue
		}
		if !selfDefenseOwnedFamilies[f.GetName()] {
			out = append(out, "bundle registry gathers undeclared self-defense family "+f.GetName())
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if !selfDefenseLabelNameAllowList[l.GetName()] {
					out = append(out, f.GetName()+" emitted forbidden series dimension "+l.GetName()+"; only [reason] is allowed")
				}
				if l.GetName() == "reason" && !selfDefenseClosedReasonValues[l.GetValue()] {
					out = append(out, f.GetName()+" emitted out-of-vocabulary reason value "+l.GetValue()+"; only the closed reasons and "+unknownSelfDefenseReason+" are allowed")
				}
			}
		}
	}
	describedConsts, describedVars := selfDefenseDescribedLabels(g)
	for fq, c := range describedConsts {
		for _, name := range c {
			if !selfDefenseLabelNameAllowList[name] {
				out = append(out, "described "+fq+" declares forbidden const label "+name+"; a const label is still a series dimension")
			}
		}
	}
	for fq := range describedVars {
		if !selfDefenseOwnedFamilies[fq] {
			out = append(out, "bundle registry describes undeclared self-defense descriptor "+fq)
		}
	}
	for fq, want := range selfDefenseOwnedFamilyLabels {
		actual, ok := describedVars[fq]
		if !ok {
			out = append(out, "bundle registry describes no descriptor for "+fq)
			continue
		}
		if strings.Join(actual, ",") != strings.Join(want, ",") {
			out = append(out, "described "+fq+" variable labels = "+strings.Join(actual, ",")+", want "+strings.Join(want, ","))
		}
	}
	return out
}

// requireSelfDefenseFamiliesBounded is the one label-safety assertion, measured
// on the real process bundle registry.
func requireSelfDefenseFamiliesBounded(t *testing.T, g selfDefenseCollectorSource) {
	t.Helper()
	for _, finding := range selfDefenseBundleLabelFindings(g) {
		t.Error(finding)
	}
}

// requireSelfDefenseFamiliesObserved is the observed-side half: once the two
// counters have recorded, Gather must publish exactly the three owned families
// and nothing else under the shared prefix.
func requireSelfDefenseFamiliesObserved(t *testing.T, g prometheus.Gatherer) {
	t.Helper()
	fams := selfDefenseGatheredFamilies(t, g)
	for name := range fams {
		if !selfDefenseOwnedFamilies[name] {
			t.Errorf("bundle registry gathers undeclared self-defense family %s", name)
		}
	}
	for name := range selfDefenseOwnedFamilies {
		if fams[name] == nil {
			t.Errorf("bundle registry is missing self-defense family %s; gathered %v", name, sortedFamilyNames(fams))
		}
	}
	if f := fams[metricEntries]; f != nil {
		for _, m := range f.GetMetric() {
			if len(m.GetLabel()) != 0 {
				t.Errorf("%s is the entry-count gauge and must carry no label, got %v", metricEntries, m.GetLabel())
			}
		}
	}
}

func sortedFamilyNames(fams map[string]*dto.MetricFamily) []string {
	out := make([]string, 0, len(fams))
	for name := range fams {
		out = append(out, name)
	}
	return out
}

func selfDefensePolicy(threshold int) ingressdefense.Policy {
	return ingressdefense.Policy{
		Enabled: true, AuthFailures: threshold, FailureWindow: time.Minute,
		InitialQuarantine: time.Hour, MaxQuarantine: 2 * time.Hour,
	}
}

func selfDefenseState(t *testing.T, maxEntries int, ttl time.Duration) *ingressdefense.State {
	t.Helper()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: maxEntries, StateTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func selfDefenseAddr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{203, 0, 113, byte(i%250 + 1)})
}

func selfDefenseGaugeValue(t *testing.T, g prometheus.Gatherer) float64 {
	t.Helper()
	fams := selfDefenseGatheredFamilies(t, g)
	f, ok := fams[metricEntries]
	if !ok || len(f.GetMetric()) != 1 || f.GetMetric()[0].Gauge == nil {
		t.Fatalf("%s must be present as exactly one gauge sample: %v", metricEntries, sortedFamilyNames(fams))
	}
	return f.GetMetric()[0].Gauge.GetValue()
}

// TestSelfDefenseBundleExposesExactlyThreeBoundedSeries pins requirement 9.3 on
// the REAL process bundle registry, not on a private one: denial count by finite
// reason, quarantine-transition count by finite reason, and the current
// adaptive-state entry count. A fourth self-defense family, or a missing one, is
// a spec deviation. Because the measurement runs against NewBundle's registry, a
// new production file that registers an extra aip_self_defense_ collector on the
// bundle is caught here even when it lives in a different file.
func TestSelfDefenseBundleExposesExactlyThreeBoundedSeries(t *testing.T) {
	t.Parallel()

	b := selfDefenseBundle(t)
	b.SelfDefense.Denial(ingressdefense.ReasonImpossiblePath)
	b.SelfDefense.QuarantineTransition(ingressdefense.ReasonImpossiblePath, 1)

	requireSelfDefenseFamiliesBounded(t, b.Registry)
	requireSelfDefenseFamiliesObserved(t, b.Registry)

	fams := selfDefenseGatheredFamilies(t, b.Registry)
	want := map[string]dto.MetricType{
		metricDenials:     dto.MetricType_COUNTER,
		metricTransitions: dto.MetricType_COUNTER,
		metricEntries:     dto.MetricType_GAUGE,
	}
	for name, kind := range want {
		f, ok := fams[name]
		if !ok {
			t.Fatalf("missing self-defense family %s; got %v", name, sortedFamilyNames(fams))
		}
		if f.GetType() != kind {
			t.Fatalf("%s type = %v, want %v", name, f.GetType(), kind)
		}
		if f.GetHelp() == "" {
			t.Fatalf("%s must carry a help string", name)
		}
	}
}

// TestSelfDefenseBundleLabelSurfaceIsBoundedOnGatherAndDescribe is the dynamic
// half of the label-safety proof, measured on the real bundle registry through
// both collector surfaces. The two counters declare exactly one variable label,
// "reason"; the entry gauge declares none; and no other aip_self_defense_ family
// exists, so no descriptor can carry a source IP, CIDR text, path, query,
// header, User-Agent, credential, principal, or country label.
func TestSelfDefenseBundleLabelSurfaceIsBoundedOnGatherAndDescribe(t *testing.T) {
	t.Parallel()

	b := selfDefenseBundle(t)
	for _, reason := range ingressdefense.AllReasons() {
		b.SelfDefense.Denial(reason)
		b.SelfDefense.QuarantineTransition(reason, 1)
	}
	requireSelfDefenseFamiliesBounded(t, b.Registry)
	requireSelfDefenseFamiliesObserved(t, b.Registry)
}

// TestSelfDefensePromReasonLabelsStayInsideTheClosedVocabulary drives every
// member of the closed reason set and then a battery of attacker-controlled
// values through both counters. Only the closed reasons and the finite "unknown"
// bucket may ever be emitted, and no hostile value may reach the exposition.
//
// This test keeps the private registry on purpose: it is a vocabulary and
// counter-arithmetic test, not a label-surface test, so it needs no view of the
// rest of the bundle.
func TestSelfDefensePromReasonLabelsStayInsideTheClosedVocabulary(t *testing.T) {
	t.Parallel()

	reg, prom := selfDefenseTestRegistry(t)
	closed := map[string]bool{"unknown": true}
	for _, reason := range ingressdefense.AllReasons() {
		closed[string(reason)] = true
	}
	for _, reason := range ingressdefense.AllReasons() {
		prom.Denial(reason)
		prom.QuarantineTransition(reason, 1)
	}
	for _, hostile := range selfDefenseHostileValues {
		prom.Denial(ingressdefense.Reason(hostile))
		prom.QuarantineTransition(ingressdefense.Reason(hostile), 7)
	}

	fams := selfDefenseGatheredFamilies(t, reg)
	if fams[metricDenials] == nil || fams[metricTransitions] == nil {
		t.Fatalf("both counters must be present: %v", sortedFamilyNames(fams))
	}
	bound := len(ingressdefense.AllReasons()) + 1 // the closed set plus "unknown"
	for _, name := range []string{metricDenials, metricTransitions} {
		f := fams[name]
		if len(f.GetMetric()) > bound {
			t.Fatalf("%s has %d series, above the closed-reason bound %d", name, len(f.GetMetric()), bound)
		}
		for _, m := range f.GetMetric() {
			if len(m.GetLabel()) != 1 || m.GetLabel()[0].GetName() != "reason" {
				t.Fatalf("%s emitted a series that is not labeled by exactly {reason}: %v", name, m.GetLabel())
			}
			value := m.GetLabel()[0].GetValue()
			if !closed[value] {
				t.Fatalf("%s emitted non-closed reason label %q", name, value)
			}
			want := float64(1)
			if value == "unknown" {
				want = float64(len(selfDefenseHostileValues))
			}
			if m.GetCounter().GetValue() != want {
				t.Fatalf("%s{reason=%q} = %v, want %v", name, value, m.GetCounter().GetValue(), want)
			}
		}
	}
	if got := len(fams[metricDenials].GetMetric()); got != bound {
		t.Fatalf("denial series = %d, want the closed vocabulary bound %d", got, bound)
	}
	var dump strings.Builder
	for _, f := range fams {
		dump.WriteString(f.String())
	}
	for _, hostile := range selfDefenseHostileValues {
		if strings.Contains(dump.String(), hostile) {
			t.Fatalf("self-defense exposition leaked attacker-controlled value %q:\n%s", hostile, dump.String())
		}
	}
	if strings.Contains(dump.String(), "Bearer") {
		t.Fatalf("self-defense exposition leaked credential material:\n%s", dump.String())
	}
}

// TestSelfDefensePromEntryGaugeFollowsInsertEvictionClearAndExpiry proves the
// gauge is the current adaptive-state entry count: it tracks insertion, bounded
// eviction, successful-auth clearing and lazy hostile-inactivity expiry, and it
// never reports a stale non-zero value once the state is empty.
func TestSelfDefensePromEntryGaugeFollowsInsertEvictionClearAndExpiry(t *testing.T) {
	t.Parallel()

	reg, prom := selfDefenseTestRegistry(t)
	prom.SetEntryCountSource(selfDefenseEntrySource(nil))
	if got := selfDefenseGaugeValue(t, reg); got != 0 {
		t.Fatalf("gauge before any state = %v, want 0", got)
	}

	policy := selfDefensePolicy(5)
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)

	// Insertion and successful-auth clearing.
	tracked := selfDefenseState(t, 64, time.Minute)
	prom.SetEntryCountSource(selfDefenseEntrySource(tracked))
	tracked.RecordProbe(selfDefenseAddr(1), now, policy)
	tracked.RecordProbe(selfDefenseAddr(2), now, policy)
	if got := selfDefenseGaugeValue(t, reg); got != 2 {
		t.Fatalf("gauge after two inserts = %v, want 2", got)
	}
	tracked.Clear(selfDefenseAddr(1))
	if got := selfDefenseGaugeValue(t, reg); got != 1 {
		t.Fatalf("gauge after a successful-auth clear = %v, want 1", got)
	}
	// Hostile inactivity past the state TTL expires the remaining entry lazily.
	if tracked.IsQuarantined(selfDefenseAddr(2), now.Add(2*time.Minute)) {
		t.Fatal("the entry must be expired by the read that passes the state TTL")
	}
	if got := selfDefenseGaugeValue(t, reg); got != 0 {
		t.Fatalf("gauge after lazy expiry = %v, want 0", got)
	}

	// Bounded eviction at capacity: the gauge stays at the capacity instead of
	// growing with unique-address churn.
	evicting := selfDefenseState(t, 1, time.Hour)
	prom.SetEntryCountSource(selfDefenseEntrySource(evicting))
	evicting.RecordProbe(selfDefenseAddr(1), now, policy)
	if got := selfDefenseGaugeValue(t, reg); got != 1 {
		t.Fatalf("gauge at capacity = %v, want 1", got)
	}
	evicting.RecordProbe(selfDefenseAddr(2), now, policy)
	if got := selfDefenseGaugeValue(t, reg); got != 1 {
		t.Fatalf("gauge after bounded eviction = %v, want 1", got)
	}
}

// TestSelfDefensePromIsTotalSoMetricsAreNonAuthoritative pins the design's error
// handling rule that metrics are non-authoritative under ABSENCE. A nil
// collector, an unbound entry-count source, an entry-count source over a nil
// state, and a hostile reason battery must all be no-ops that never panic and
// never fabricate a series. Under FAILURE the invariant is proven end to end by
// TestSelfDefenseMetricsFailureCannotChangeTheWireRefusal, which drives the real
// gate with a panicking label sink.
func TestSelfDefensePromIsTotalSoMetricsAreNonAuthoritative(t *testing.T) {
	t.Parallel()

	var absent *SelfDefenseProm
	for _, reason := range ingressdefense.AllReasons() {
		absent.Denial(reason)
		absent.QuarantineTransition(reason, 3)
	}
	absent.SetEntryCountSource(selfDefenseEntrySource(nil))

	reg, prom := selfDefenseTestRegistry(t)
	prom.SetEntryCountSource(selfDefenseEntrySource(nil))
	prom.SetEntryCountSource(nil)
	for _, hostile := range selfDefenseHostileValues {
		prom.Denial(ingressdefense.Reason(hostile))
		prom.QuarantineTransition(ingressdefense.Reason(hostile), -1)
	}
	if got := selfDefenseGaugeValue(t, reg); got != 0 {
		t.Fatalf("gauge with an unbound source = %v, want 0", got)
	}
	fams := selfDefenseGatheredFamilies(t, reg)
	if len(fams) != 3 {
		t.Fatalf("a total observer must still keep the three families present: %v", sortedFamilyNames(fams))
	}
}

// TestSelfDefenseLabelSurfaceIsMechanicallyClosed is the static half of the
// label-safety proof. It walks EVERY non-test production .go file of this package
// but selects an in-scope file by the shared aip_self_defense_ metric-name prefix
// (see selfDefenseFileDeclaresSelfDefenseMetric), not by any enumeration of the
// three owned metric names: a new sibling file that adds a fourth self-defense
// collector is therefore in scope without any table being updated first. On every
// in-scope file the scan rejects any Prometheus API that can add a label outside
// the declared set, and any forbidden attacker-controlled label name in any
// string literal.
//
// KNOWN LIMITATION, stated because this guard is often read as stronger than it
// is: a self-defense series RENAMED out of the aip_self_defense_ prefix, or
// published from a file carrying no self-defense metric name, is outside the
// scope of BOTH halves, because both are scoped by that same name prefix — the
// static half through selfDefenseFileDeclaresSelfDefenseMetric and the registry
// half through the prefix filter over gathered and described family names.
// Registering a renamed family on the bundle registry does NOT bring it into
// scope. Closing that would require a whole-bundle inventory ratchet, which is
// deliberately out of scope for this change.
func TestSelfDefenseLabelSurfaceIsMechanicallyClosed(t *testing.T) {
	t.Parallel()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !selfDefenseFileDeclaresSelfDefenseMetric(file) {
			continue
		}
		scanned++
		for _, finding := range selfDefenseLabelSurfaceFindings(file) {
			t.Errorf("%s: %s", name, finding)
		}
		for _, literal := range selfDefenseForbiddenTokensInFile(file) {
			t.Errorf("%s carries forbidden attacker-controlled label material %s", name, literal)
		}
	}
	if scanned == 0 {
		t.Fatal("no production file declaring a self-defense metric was scanned; the label-surface guard is vacuous")
	}
}

// TestSelfDefenseSelectionPredicateScopesSiblingsAndIgnoresStrangers is the
// non-vacuity proof for the file-selection predicate itself. A guard that is only
// ever exercised on one hand-written file is a guard that a sibling file can walk
// straight past, so the synthetic fixtures below are driven THROUGH the
// selector, not merely through the label-surface detector:
//
//   - an in-scope sibling that registers a self-defense collector carrying a
//     source_ip label must be selected AND reported by the static guard;
//   - an out-of-scope file that registers a non-self-defense collector carrying a
//     source_ip label must be ignored, so the guard cannot be widened into a
//     whole-package ban.
//
// The unchecked-collector fixture is selected but deliberately not reported here:
// its request_target label is invisible to the static scan, which is precisely
// why the bundle-registry Gather assertion in
// TestSelfDefenseBundleGuardRejectsSiblingCollectors is the mechanism that must
// catch it.
func TestSelfDefenseSelectionPredicateScopesSiblingsAndIgnoresStrangers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		src      string
		inScope  bool
		reported bool
	}{
		{
			name: "in-scope sibling leaks a source address",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"var leaky = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: \"aip\", Name: \"self_defense_a1_hits_total\"}, []string{\"source_ip\"})\n",
			inScope:  true,
			reported: true,
		},
		{
			name: "in-scope sibling with no forbidden label is still scanned",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"func f() *prometheus.CounterVec { return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: \"aip\", Name: \"self_defense_extra_total\"}, []string{\"reason\"}) }\n",
			inScope:  true,
			reported: false,
		},
		{
			name: "in-scope unchecked collector with a request target",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"func f() *prometheus.Desc { return prometheus.NewDesc(\"aip_self_defense_a2_unchecked_total\", \"h\", []string{\"request_target\"}, nil) }\n",
			inScope:  true,
			reported: false,
		},
		{
			name: "in-scope name split by concatenation",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"func f() *prometheus.CounterVec { return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: \"aip\", Name: \"self_defense_\" + \"split_total\"}, []string{\"source_ip\"}) }\n",
			inScope:  true,
			reported: true,
		},
		{
			name: "in-scope name hidden behind a package constant",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"const leakyName = \"self_defense_const_total\"\n\n" +
				"var leaky = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: \"aip\", Name: leakyName}, []string{\"source_ip\"})\n",
			inScope:  true,
			reported: true,
		},
		{
			name: "out-of-scope geoip file is ignored",
			src: "package p\n\nimport \"github.com/prometheus/client_golang/prometheus\"\n\n" +
				"var other = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: \"aip\", Name: \"geoip_blocked_total\"}, []string{\"source_ip\"})\n",
			inScope:  false,
			reported: false,
		},
		{
			name:     "out-of-scope helper mentions no metric name",
			src:      "package p\n\nfunc f(path string) string { return path }\n",
			inScope:  false,
			reported: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := selfDefenseFileDeclaresSelfDefenseMetric(file); got != tc.inScope {
				t.Fatalf("selfDefenseFileDeclaresSelfDefenseMetric = %v, want %v", got, tc.inScope)
			}
			if !tc.inScope {
				// The guard skips an out-of-scope file entirely, so nothing it
				// declares can be reported.
				return
			}
			findings := append(selfDefenseLabelSurfaceFindings(file), selfDefenseForbiddenTokensInFile(file)...)
			if got := len(findings) > 0; got != tc.reported {
				t.Fatalf("static guard reported = %v (%v), want %v", got, findings, tc.reported)
			}
		})
	}
}

// selfDefenseUncheckedCollector is a Describe-invisible prometheus.Collector: it
// sends nothing on Describe, so registry.Describe cannot see it, while its
// Collect still publishes a labelled series that registry.Gather does see. It is
// the exact shape that defeated a Describe-only label-safety guard.
type selfDefenseUncheckedCollector struct {
	desc *prometheus.Desc
}

func (c *selfDefenseUncheckedCollector) Describe(chan<- *prometheus.Desc) {}

func (c *selfDefenseUncheckedCollector) Collect(out chan<- prometheus.Metric) {
	out <- prometheus.MustNewConstMetric(c.desc, prometheus.CounterValue, 7, "/graphql?q=1")
}

// TestSelfDefenseBundleGuardRejectsSiblingCollectors is the non-vacuity proof for
// the bundle-registry label-safety guard. It installs, on a registry that already
// holds the real collector, the shapes that defeated earlier versions of the
// guard:
//
//   - a described extra self-defense counter labelled by source_ip;
//   - an unchecked (Describe-invisible) collector labelled by request_target;
//   - an extra self-defense counter that keeps the ALLOWED label name "reason" but
//     smuggles a source address into the reason VALUE, which is the shape a
//     label-NAME-only check cannot see.
//
// Each must produce a finding naming the offending dimension or value, and a
// clean registry must produce none. A guard that cannot fail is not a guard.
func TestSelfDefenseBundleGuardRejectsSiblingCollectors(t *testing.T) {
	t.Parallel()

	clean := prometheus.NewRegistry()
	RegisterSelfDefenseProm(clean)
	if findings := selfDefenseBundleLabelFindings(clean); len(findings) != 0 {
		t.Fatalf("a clean registry must produce no finding, got %v", findings)
	}

	t.Run("described sibling with a source address", func(t *testing.T) {
		t.Parallel()
		reg := prometheus.NewRegistry()
		RegisterSelfDefenseProm(reg)
		leaky := prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "self_defense_a1_hits_total",
			Help:      "synthetic sibling",
		}, []string{"source_ip"})
		reg.MustRegister(leaky)
		leaky.WithLabelValues("198.51.100.66").Inc()
		gathered, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		if !selfDefenseHasLabelValue(gathered, "source_ip") {
			t.Fatalf("synthetic sibling must actually emit a source_ip series, got %v", gathered)
		}
		findings := selfDefenseBundleLabelFindings(reg)
		if !selfDefenseFindingsMention(findings, "source_ip") {
			t.Fatalf("guard missed the described source_ip sibling: %v", findings)
		}
	})

	t.Run("unchecked sibling with a request target", func(t *testing.T) {
		t.Parallel()
		reg := prometheus.NewRegistry()
		RegisterSelfDefenseProm(reg)
		reg.MustRegister(&selfDefenseUncheckedCollector{desc: prometheus.NewDesc(
			"aip_self_defense_a2_unchecked_total",
			"synthetic Describe-invisible sibling",
			[]string{"request_target"}, nil,
		)})
		consts, vars := selfDefenseDescribedLabels(reg)
		if len(consts) == 0 {
			t.Fatal("the real collector's own descriptors must stay Describe-visible")
		}
		if _, visible := vars["aip_self_defense_a2_unchecked_total"]; visible {
			t.Fatal("fixture must stay Describe-invisible, otherwise it tests nothing")
		}
		findings := selfDefenseBundleLabelFindings(reg)
		if !selfDefenseFindingsMention(findings, "aip_self_defense_a2_unchecked_total") {
			t.Fatalf("guard missed the Describe-invisible sibling family: %v", findings)
		}
		if !selfDefenseFindingsMention(findings, "request_target") {
			t.Fatalf("guard missed the Describe-invisible sibling label: %v", findings)
		}
	})

	t.Run("owned-family sibling smuggling a source address into the reason value", func(t *testing.T) {
		t.Parallel()
		reg := prometheus.NewRegistry()
		RegisterSelfDefenseProm(reg)
		leaky := prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "self_defense_a3_hits_total",
			Help:      "synthetic sibling reusing the allowed reason label name",
		}, []string{"reason"})
		reg.MustRegister(leaky)
		leaky.WithLabelValues("198.51.100.66").Inc()
		gathered, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		if !selfDefenseHasLabelValue(gathered, "reason") {
			t.Fatalf("synthetic sibling must actually emit a reason series, got %v", gathered)
		}
		if !selfDefenseHasExactLabelValue(gathered, "reason", "198.51.100.66") {
			t.Fatalf("fixture must really publish reason=%q, got %v", "198.51.100.66", gathered)
		}
		findings := selfDefenseBundleLabelFindings(reg)
		if !selfDefenseFindingsMention(findings, "out-of-vocabulary reason value 198.51.100.66") {
			t.Fatalf("guard missed the out-of-vocabulary reason value: %v", findings)
		}
	})
}

func selfDefenseFindingsMention(findings []string, needle string) bool {
	for _, f := range findings {
		if strings.Contains(f, needle) {
			return true
		}
	}
	return false
}

func selfDefenseHasLabelValue(mfs []*dto.MetricFamily, labelName string) bool {
	for _, f := range mfs {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName {
					return true
				}
			}
		}
	}
	return false
}

func selfDefenseHasExactLabelValue(mfs []*dto.MetricFamily, labelName, value string) bool {
	for _, f := range mfs {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == value {
					return true
				}
			}
		}
	}
	return false
}

// TestSelfDefenseLabelSurfaceFindingsDetectForbiddenShapes proves the static
// guard's detector is not vacuous: every shape that could add an undeclared label
// is reported, and the legitimate finite shapes are not.
func TestSelfDefenseLabelSurfaceFindingsDetectForbiddenShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		src     string
		wantBad bool
	}{
		{
			name:    "dynamic prometheus labels",
			src:     "package p\n\nfunc f(m *prometheus.CounterVec, path string) { m.With(prometheus.Labels{\"path\": path}).Inc() }\n",
			wantBad: true,
		},
		{
			name:    "const labels on the opts",
			src:     "package p\n\nfunc f() { _ = prometheus.CounterOpts{ConstLabels: prometheus.Labels{\"country\": \"RU\"}} }\n",
			wantBad: true,
		},
		{
			name:    "get metric with explicit labels",
			src:     "package p\n\nfunc f(m *prometheus.CounterVec) { m.GetMetricWith(prometheus.Labels{\"principal\": \"admin\"}).Inc() }\n",
			wantBad: true,
		},
		{
			name:    "add with explicit labels",
			src:     "package p\n\nfunc f(m *prometheus.CounterVec, p string) { m.WithLabelValues(p).Add(1) }\nfunc g() {}\n",
			wantBad: false,
		},
		{
			name:    "label deletion",
			src:     "package p\n\nfunc f(m *prometheus.CounterVec) { m.DeleteLabelValues(\"reason\") }\n",
			wantBad: true,
		},
		{
			name:    "declared finite label list",
			src:     "package p\n\nfunc f() *prometheus.CounterVec { return prometheus.NewCounterVec(prometheus.CounterOpts{Name: \"x\"}, []string{\"reason\"}) }\n",
			wantBad: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, bad := selfDefenseLabelSurface(file)
			if bad != tc.wantBad {
				t.Fatalf("selfDefenseLabelSurface = %v, want %v", bad, tc.wantBad)
			}
			if !tc.wantBad {
				for _, finding := range selfDefenseLabelSurfaceFindings(file) {
					t.Fatalf("clean shape reported a finding: %s", finding)
				}
			}
		})
	}
}

// TestBundleOwnsTheSelfDefenseCollector proves the collector is part of the
// process metrics bundle, exactly as the GeoIP ingress collector is, so the
// composition root can project it as the generation's bounded observer.
func TestBundleOwnsTheSelfDefenseCollector(t *testing.T) {
	t.Parallel()

	b := selfDefenseBundle(t)
	b.SelfDefense.Denial(ingressdefense.ReasonActiveQuarantine)
	b.SelfDefense.QuarantineTransition(ingressdefense.ReasonAuthFailureThreshold, 1)
	fams := selfDefenseGatheredFamilies(t, b.Registry)
	for _, name := range []string{metricDenials, metricTransitions, metricEntries} {
		if _, ok := fams[name]; !ok {
			t.Fatalf("bundle registry is missing %s: %v", name, sortedFamilyNames(fams))
		}
	}
}

// selfDefenseFileDeclaresSelfDefenseMetric is the file-selection predicate of the
// static label-safety guard. It is name-agnostic but NOT name-agnostic in the
// strong sense: it walks the whole package and then selects a file when any
// string the file can fold into a metric name belongs to the aip_self_defense_
// family, either already fully qualified or as the Name a prometheus opts literal
// combines with the shared namespace. It therefore selects a sibling file that
// adds a fourth self-defense collector, hides the name behind a constant, or
// splits it by concatenation, and it never enumerates the three owned metric
// names, which is exactly what made an earlier version of this guard defeatable.
//
// The boundary is the name, and it is deliberate: the predicate scopes this guard
// to the self-defense metric family so it cannot be widened into a whole-package
// ban on legitimate series such as aip_geoip_blocked_total{source_ip}. A
// self-defense series RENAMED out of the aip_self_defense_ prefix is therefore not
// selected here and is not reported by this predicate's caller.
func selfDefenseFileDeclaresSelfDefenseMetric(file *ast.File) bool {
	for _, value := range selfDefenseFileStringValues(file) {
		switch {
		case strings.HasPrefix(value, selfDefenseMetricPrefix):
			return true
		case strings.HasPrefix(value, selfDefenseDeclaredMetricPrefix):
			return true
		case strings.Contains(value, selfDefenseMetricPrefix):
			return true
		}
	}
	return false
}

// selfDefenseFileStringValues folds every string the file can turn into a metric
// name: raw literals, string-literal concatenations, and file-level or
// function-local constants initialised from either.
func selfDefenseFileStringValues(file *ast.File) []string {
	consts := map[string]string{}
	var out []string
	add := func(expr ast.Expr) {
		if v, ok := selfDefenseFoldString(expr, consts); ok {
			out = append(out, v)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i < len(value.Values) {
						if v, ok := selfDefenseFoldString(value.Values[i], consts); ok {
							consts[name.Name] = v
						}
					}
				}
			}
		case *ast.AssignStmt:
			if typed.Tok != token.DEFINE {
				return true
			}
			for i, lhs := range typed.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(typed.Rhs) {
					continue
				}
				if v, ok := selfDefenseFoldString(typed.Rhs[i], consts); ok {
					consts[ident.Name] = v
					out = append(out, v)
				}
			}
		case *ast.BasicLit:
			if typed.Kind == token.STRING {
				if v, err := strconv.Unquote(typed.Value); err == nil {
					out = append(out, v)
				}
			}
		case *ast.BinaryExpr:
			if typed.Op == token.ADD {
				add(typed)
			}
		}
		return true
	})
	return out
}

// selfDefenseFoldString evaluates a constant string expression: a literal, a
// reference to an already-folded identifier, or a concatenation of those.
func selfDefenseFoldString(expr ast.Expr, consts map[string]string) (string, bool) {
	switch typed := expr.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(typed.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := consts[typed.Name]
		return v, ok
	case *ast.ParenExpr:
		return selfDefenseFoldString(typed.X, consts)
	case *ast.BinaryExpr:
		if typed.Op != token.ADD {
			return "", false
		}
		left, ok := selfDefenseFoldString(typed.X, consts)
		if !ok {
			return "", false
		}
		right, ok := selfDefenseFoldString(typed.Y, consts)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
}

// selfDefenseLabelSurface reports the first Prometheus construct in file that
// could introduce a series dimension outside the declared label list.
func selfDefenseLabelSurface(file *ast.File) (string, bool) {
	var offender string
	ast.Inspect(file, func(node ast.Node) bool {
		if offender != "" {
			return false
		}
		switch typed := node.(type) {
		case *ast.CompositeLit:
			if isPrometheusLabels(typed.Type) {
				offender = "prometheus.Labels composite literal adds an undeclared series dimension"
			}
		case *ast.KeyValueExpr:
			if ident, ok := typed.Key.(*ast.Ident); ok && ident.Name == "ConstLabels" {
				offender = "ConstLabels adds an undeclared series dimension"
			}
		case *ast.SelectorExpr:
			switch typed.Sel.Name {
			case "With", "GetMetricWith", "Delete", "DeleteLabelValues", "DeletePartialMatch", "Reset":
				if isPrometheusSelector(typed.X) {
					offender = "prometheus." + typed.Sel.Name + "() can introduce or drop a series dimension"
				}
			}
		}
		return true
	})
	return offender, offender != ""
}

func selfDefenseLabelSurfaceFindings(file *ast.File) []string {
	finding, bad := selfDefenseLabelSurface(file)
	if !bad {
		return nil
	}
	return []string{finding}
}

// selfDefenseForbiddenTokensInFile returns every forbidden attacker-controlled
// label material the file carries in a string literal, so a caller can report
// either the label-surface shape or the forbidden label name.
func selfDefenseForbiddenTokensInFile(file *ast.File) []string {
	var out []string
	for _, literal := range selfDefenseStringLiterals(file) {
		lower := strings.ToLower(literal)
		for _, token := range selfDefenseForbiddenLabelTokens {
			if strings.Contains(lower, token) {
				out = append(out, literal+" ("+token+")")
			}
		}
	}
	return out
}

func isPrometheusLabels(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Labels" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "prometheus"
}

// isPrometheusSelector reports whether expr is a value of the prometheus
// package, which is the only place a dynamic label call can appear. A receiver
// variable is also accepted so the detector reports the offending call shape
// rather than requiring a test fixture to name a concrete collector field.
func isPrometheusSelector(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := typed.X.(*ast.Ident)
		return ok && pkg.Name == "prometheus"
	case *ast.Ident, *ast.StarExpr:
		return true
	}
	return false
}

func selfDefenseStringLiterals(file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || len(lit.Value) < 2 {
			return true
		}
		value := strings.Trim(lit.Value, `"`)
		if value == "" || strings.ContainsAny(value, " \t") {
			return true
		}
		out = append(out, value)
		return true
	})
	return out
}

// parseDescLabels reads the descriptor's own debug rendering, which is the
// client's supported introspection surface, and returns the fully-qualified
// name plus the const and variable label names.
func parseDescLabels(rendered string) (fqName string, constLabels, variableLabels []string) {
	fqName = descFieldQuoted(rendered, "fqName")
	constLabels = descLabelNames(rendered, "constLabels")
	variableLabels = descLabelNames(rendered, "variableLabels")
	return fqName, constLabels, variableLabels
}

func descFieldQuoted(rendered, field string) string {
	marker := field + `: "`
	_, after, ok := strings.Cut(rendered, marker)
	if !ok {
		return ""
	}
	rest := after
	before0, _, ok0 := strings.Cut(rest, `"`)
	if !ok0 {
		return ""
	}
	return before0
}

func descLabelNames(rendered, field string) []string {
	marker := field + ": {"
	_, after, ok := strings.Cut(rendered, marker)
	if !ok {
		return nil
	}
	rest := after
	before0, _, ok0 := strings.Cut(rest, "}")
	if !ok0 {
		return nil
	}
	body := strings.TrimSpace(before0)
	if body == "" {
		return nil
	}
	parts := strings.Split(body, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		out = append(out, name)
	}
	return out
}
