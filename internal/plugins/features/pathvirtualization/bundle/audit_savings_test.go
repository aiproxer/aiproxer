package bundle_test

// Spec: b-leg-path-virtualization Task 11.2, requirements.md 7.3, 7.6, 7.7, 9.1 and
// 9.5, against design.md "Observability" and "Configuration and Composition".
//
// This file is the OPERATOR-FACING half of the audit mode. The rewrite package's own
// audit suite (rewrite/audit_test.go) and the parity probe it added (rewrite/
// parity_probe_test.go) already prove that two rewriter instances agree about one
// payload. That is not the claim an operator sizing a rollout needs. The claim is that
// the numbers a measured deployment publishes - the ones an operator reads to decide
// whether to switch the mode - are the numbers the mutating deployment would have
// realized. So every assertion here is made through the SHIPPED composition seam
// (config.Decode -> bundle.FeatureBundleWithTelemetry -> telemetry.Snapshot) rather
// than through a rewriter a test constructed for itself. A drift between the two
// would then have to be a drift in the wiring, which is the only place one could
// survive the rewrite package's coverage.
//
// Three properties are pinned, and the third is what makes the first two mean
// something:
//
//	7.3  AUDIT MEASURES WHAT REWRITE WOULD DO, EXACTLY. For one identical input, the
//	     audit-mode snapshot and the rewrite-mode snapshot carry the same eligible
//	     count, the same replacement count, the same decoded byte totals, and the
//	     same byte saving - equality, not proximity - across both canonical
//	     authorities, several occurrence counts, three path flavors, and a case with
//	     no eligible leaf at all.
//	7.3  AUDIT PUBLISHES NOTHING. The call each pass was handed comes back
//	     byte-identical, on the same backing arrays, with the attempt decision
//	     carrying no reason code. Without this the equality above could be a
//	     coincidence of two passes that both happened to measure zero.
//	9.1  NO NEGATIVE SAVING IS EVER PUBLISHED. The inbound direction replaces an
//	     alias with a LONGER real root, so its delta is negative by construction;
//	     the published figure is a growth figure or zero, never a negative saving,
//	     and a delta that somehow went the wrong way is clamped rather than reported.
//
// 7.7 is asserted against hostile content in BOTH modes rather than in one, because
// the mode is the only thing that changes between them and a leak introduced by a
// mode branch would be invisible to a single-mode check.
//
// EVERY ASSERTION MESSAGE IN THIS FILE IS CONTENT-FREE. Names are bounded tokens,
// and the numbers reported are counts, byte totals, and bounded labels. No failure
// message interpolates a path, an alias, a workspace tag, a tool name, a JSON
// Pointer, or an argument document - including the fixtures' own bytes, which would
// otherwise be the one way this suite could publish what it exists to prove is never
// published.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

// auditSavingsTool is a canonical tool name the shipped built-in profile layer
// claims with the /file_path argument selector, so the parity fixtures exercise the
// policy a deployment gets with no operator configuration at all.
const auditSavingsTool = "read_file"

// auditSavingsDeclaredSchema is that tool's declared argument schema. It is declared
// so the fixture is the shape a real candidate carries, and so a future
// inference-enabled configuration could resolve the same selector from it.
const auditSavingsDeclaredSchema = `{"type":"object","properties":{"file_path":{"type":"string"},` +
	`"limit":{"type":"integer"}},"required":["file_path"]}`

// auditSavingsOutsidePath is the value the no-eligible-leaf fixture selects: a real
// absolute location under no fixture root, which the rewriter treats as ordinary
// content rather than as a candidate.
//
// It is deliberately a different flavor from two of the three fixture roots, because
// "not a candidate" must be a decision about the MAPPING rather than an accident of
// the flavor: a Windows mapping asked about a POSIX path and a POSIX mapping asked
// about a Windows path both answer not-a-candidate, and the fixture proves it.
const auditSavingsOutsidePath = "/usr/local/share/doc/synthetic_file_not_under_the_root.txt"

// The three fixture roots. Requirement 9.6 asks for a representative long POSIX path
// and a representative long Windows path, and the equality under test has to be
// flavor-independent or it is a statement about one platform. Each root is long
// enough that the fixed V1 alias is strictly shorter by a wide margin (requirement
// 1.4), so a measured saving is a three-digit number rather than a rounding artifact.
const (
	auditSavingsPOSIXRoot = "/home/dev/build-agent/workspaces/aiproxer-example-project/monorepo" +
		"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-audit-savings-fixture"
	auditSavingsWindowsRoot = `C:\Users\dev\build-agent\source\repos\aiproxer-example-project` +
		`\.worktrees\b-leg-path-virtualization-audit-savings-windows-fixture`
	auditSavingsUNCRoot = `\\fileserver\build-agent\workspaces\aiproxer-example-project` +
		`\monorepo\.worktrees\b-leg-path-virtualization-audit-savings-unc-fixture`
)

// auditSavingsWorkspaceID is the runtime's pinned workspace identity for the parity
// fixtures. It is a fixed label rather than a composed value because nothing about it
// is under test: the fixtures assert on the PROJECT ROOT the pin carries, and the
// identity field exists only so the pin is shaped like the one production projects.
const auditSavingsWorkspaceID = "ws_audit_savings"

// auditSavingsFlavor is one path flavor of the parity matrix.
type auditSavingsFlavor struct {
	// name is the bounded case-name fragment for this flavor.
	name string
	// root is the authoritative project root the pinned workspace view carries.
	root string
	// sep is the separator this flavor spells between path segments.
	sep string
}

// auditSavingsFlavors is the flavor axis: a POSIX root, a Windows drive root, and a
// Windows UNC root. Three flavors rather than two so a POSIX-specific behaviour
// cannot satisfy the Windows rows.
var auditSavingsFlavors = []auditSavingsFlavor{
	{name: "posix", root: auditSavingsPOSIXRoot, sep: "/"},
	{name: "windows_drive", root: auditSavingsWindowsRoot, sep: `\`},
	{name: "windows_unc", root: auditSavingsUNCRoot, sep: `\`},
}

// auditSavingsOccurrenceTiers is the occurrence axis. Three tiers spanning one to
// ten path occurrences, because a detector that measured one occurrence correctly
// could still mis-count a request that carries several.
var auditSavingsOccurrenceTiers = []int{1, 3, 10}

// auditSavingsCase is one observable fixture of the audit/rewrite parity matrix.
type auditSavingsCase struct {
	// name is the bounded subtest name. It is assembled from the axes alone, so it
	// carries no path, tool, or payload byte into the test log.
	name string
	// flavor supplies the root and the separator.
	flavor auditSavingsFlavor
	// occurrences is how many path-bearing tool calls the fixture carries.
	occurrences int
	// legacy selects the canonical authority: false for ordered items, true for
	// legacy message parts. The rewriter walks exactly one of the two per call, and
	// requirement 7.3's equality has to hold on both.
	legacy bool
	// eligible is false for the fixtures whose selected leaves carry no workspace
	// path at all. Those are the cases where both modes report zero, and the
	// equality has to be exact there too: two modes that agree on a large number are
	// easy, and two modes that agree on zero are where a detector that measures
	// nothing could hide.
	eligible bool
}

// sampleValue returns the selected leaf value of the i-th fixture occurrence.
//
// Every occurrence has the same decoded length, which is what lets one sample stand
// in for all of them when the expected byte totals are derived.
func (tc auditSavingsCase) sampleValue(index int) string {
	if !tc.eligible {
		return auditSavingsOutsidePath
	}
	return tc.flavor.root + tc.flavor.sep + "pkg" + tc.flavor.sep + fmt.Sprintf("file_%02d.go", index)
}

// authority returns the bounded axis fragment naming this case's canonical
// authority.
func (tc auditSavingsCase) authority() string {
	if tc.legacy {
		return "messages"
	}
	return "items"
}

// buildCall builds one fresh canonical call for this fixture.
//
// It is deliberately a BUILDER rather than a shared value: the two modes and the two
// passes must each receive an independent input, and a fixture that handed the same
// pointer to every run could let a first run's publication be measured by the second
// as if the client had sent it.
func (tc auditSavingsCase) buildCall(t *testing.T) *aipapi.Call {
	t.Helper()
	tools := []aipapi.ToolDef{{
		Name:       auditSavingsTool,
		Parameters: json.RawMessage(auditSavingsDeclaredSchema),
	}}

	call := &aipapi.Call{ID: "call_audit_savings", Tools: tools}
	if tc.legacy {
		parts := make([]aipapi.Part, 0, tc.occurrences)
		for i := range tc.occurrences {
			parts = append(parts, aipapi.Part{
				Kind: aipapi.PartJSON, ToolCallID: fmt.Sprintf("call_%02d", i),
				ToolName: auditSavingsTool,
				Content:  json.RawMessage(auditSavingsArguments(t, tc.sampleValue(i))),
			})
		}
		call.Messages = []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: parts}}
	} else {
		items := make([]aipapi.Item, 0, tc.occurrences)
		for i := range tc.occurrences {
			items = append(items, aipapi.Item{
				Kind: aipapi.ItemKindToolCall, ID: fmt.Sprintf("item_call_%02d", i),
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID: fmt.Sprintf("call_%02d", i), Name: auditSavingsTool,
					Arguments: json.RawMessage(auditSavingsArguments(t, tc.sampleValue(i))),
				},
			})
		}
		call.Items = items
	}

	// The error text is deliberately dropped: canonical validation reports the first
	// offending field, and a fixture that echoed its own payload into a failure
	// message would make this suite publish what it exists to prove is never
	// published.
	if err := call.Validate(); err != nil {
		t.Fatalf("%s: fixture call is not canonical", tc.name)
	}
	return call
}

// auditSavingsArguments builds one completed-argument document holding the selected
// location beside one unselected sibling, so the fixture carries a member no selector
// may reach and the measurement cannot be attributed to a whole-document rewrite.
func auditSavingsArguments(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"file_path": value,
		"limit":     10,
		"sibling":   auditSavingsOutsidePath,
	})
	if err != nil {
		t.Fatal("fixture argument document is not encodable")
	}
	return string(encoded)
}

// auditSavingsCorpus returns every fixture the parity matrix runs, in a stable order.
//
// The cross product is the point: one axis alone would let a detector that handled
// only one authority, one magnitude, or one flavor pass. The no-eligible-leaf cases
// are added per flavor rather than once, so the zero case is also shown to be
// flavor-independent.
func auditSavingsCorpus() []auditSavingsCase {
	var cases []auditSavingsCase
	for _, flavor := range auditSavingsFlavors {
		for _, occurrences := range auditSavingsOccurrenceTiers {
			for _, legacy := range []bool{false, true} {
				cases = append(cases, auditSavingsCase{
					name:        fmt.Sprintf("%s_occurrences_%d", flavor.name, occurrences),
					flavor:      flavor,
					occurrences: occurrences,
					legacy:      legacy,
					eligible:    true,
				})
			}
		}
		for _, legacy := range []bool{false, true} {
			cases = append(cases, auditSavingsCase{
				name: fmt.Sprintf("%s_occurrences_%d_no_eligible_leaf",
					flavor.name, auditSavingsOccurrenceTiers[1]),
				flavor:      flavor,
				occurrences: auditSavingsOccurrenceTiers[1],
				legacy:      legacy,
				eligible:    false,
			})
		}
	}
	// The authority is part of the NAME rather than an unlabelled loop variable, so a
	// failing subtest identifies which of the two canonical surfaces it was driving.
	for i := range cases {
		cases[i].name = fmt.Sprintf("%s_%s", cases[i].authority(), cases[i].name)
	}
	return cases
}

// auditSavingsRun is one complete drive of the shipped composition in one mode.
type auditSavingsRun struct {
	// afterEarly is the recorder's snapshot after the EARLY outbound pass alone. The
	// bundle installs one recorder on both passes, so the per-pass measurement is read
	// off BETWEEN the two passes rather than through a second recorder or a second
	// harness - the shipped wiring has exactly one sink and this reads it twice.
	afterEarly telemetry.Snapshot
	// final is the snapshot after every pass this run drove.
	final telemetry.Snapshot
	// bytesIdentical reports that every call handed to a pass came back byte-for-byte
	// as it went in. Byte equality rather than a Go value comparison, so a change to
	// any nested slice, RawMessage, or extension map would be visible.
	bytesIdentical bool
	// backingShared reports that every selected argument document still occupies the
	// array it occupied before the passes ran. This is STRICTLY STRONGER than
	// bytesIdentical: a pass that built the deep copy it would publish from and wrote
	// that copy back would leave identical bytes on different arrays, and requirement
	// 7.3's "without mutating" is precisely the claim that the measuring mode never
	// builds that copy at all.
	backingShared bool
	// mutated reports that at least one pass changed a call's bytes, which is the
	// publication the mode under test exists to perform or to withhold.
	mutated bool
	// inertDecision reports that the early pass returned the attempt-continue decision
	// with no reason code, so nothing it decided about the call could reach a log line.
	inertDecision bool
}

// run drives the shipped bundle over this fixture in one rollout mode.
//
// The two outbound passes are handed IDENTICAL but INDEPENDENT inputs when share is
// false, which is what makes their two measurements individually attributable: each
// pass then sees a fresh client-shaped request in both modes, so the snapshots can be
// compared pass by pass and in aggregate. When share is true the late pass sees the
// candidate the early pass published, which is production order and the shape the
// idempotence property is about.
func (tc auditSavingsCase) run(t *testing.T, mode rewrite.Mode, share bool) auditSavingsRun {
	t.Helper()
	tel, b := auditSavingsBundle(t, mode)

	// Two INDEPENDENT candidates when the passes must be attributed separately, one
	// shared candidate when the production ordering is what is under test. Deciding
	// this BEFORE either pass runs is what keeps the before-state honest: a baseline
	// captured after the early pass would silently measure the wrong thing.
	early := tc.buildCall(t)
	late := early
	if !share {
		late = tc.buildCall(t)
	}
	earlyBefore := auditSavingsRender(t, early)
	lateBefore := auditSavingsRender(t, late)
	backingBefore := auditSavingsBacking(t, early)
	if !share {
		backingBefore = append(backingBefore, auditSavingsBacking(t, late)...)
	}

	ctx := pinnedWorkspace(tc.flavor.root)
	meta := request.AttemptMeta{
		Workspace: aipworkspace.WorkspaceView{ID: auditSavingsWorkspaceID, ProjectRoot: tc.flavor.root},
	}
	transform := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0]
	hook := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0]

	decision, err := transform.HandleAttempt(ctx, early, meta, request.Services{})
	if err != nil {
		t.Fatalf("%s: the early outbound pass must never return an error", tc.name)
	}
	afterEarly := tel.Snapshot()

	if err := hook.HandleRequestParts(ctx, late, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("%s: the late outbound pass must never return an error", tc.name)
	}
	final := tel.Snapshot()

	earlyAfter := auditSavingsRender(t, early)
	lateAfter := auditSavingsRender(t, late)
	backingAfter := auditSavingsBacking(t, early)
	if !share {
		backingAfter = append(backingAfter, auditSavingsBacking(t, late)...)
	}
	return auditSavingsRun{
		afterEarly:     afterEarly,
		final:          final,
		bytesIdentical: earlyAfter == earlyBefore && lateAfter == lateBefore,
		backingShared:  sameBackingPointers(backingBefore, backingAfter),
		mutated:        earlyAfter != earlyBefore || lateAfter != lateBefore,
		inertDecision:  decision.Kind == request.AttemptContinue && decision.ReasonCode == "",
	}
}

// auditSavingsOutboundPasses is how many outbound passes the run harness drives per
// fixture: the bundle contributes exactly two, the candidate attempt transform and the
// request-part hook, and the generation total is the sum of both.
const auditSavingsOutboundPasses = 2

// auditSavingsBundle builds the shipped composition in one rollout mode and hands
// back the recorder the bundle installed, so the counters are read from the same
// instance the components report to rather than from one a test wired up itself.
func auditSavingsBundle(t *testing.T, mode rewrite.Mode) (*telemetry.Telemetry, aipfeature.FeatureBundle) {
	t.Helper()
	resolved, err := config.Decode(auditSavingsYAML(t, "enabled: true\nmode: "+mode.String()+"\n"))
	if err != nil {
		t.Fatalf("Decode(%s): %v", mode, err)
	}
	tel, b, err := bundle.FeatureBundleWithTelemetry(resolved)
	if err != nil {
		t.Fatalf("%s: FeatureBundleWithTelemetry: %v", mode, err)
	}
	if tel.Inventory().Mode != mode.String() {
		t.Fatalf("inventory mode = %q, want the mode under test", tel.Inventory().Mode)
	}
	return tel, b
}

// auditSavingsYAML parses one operator subtree.
func auditSavingsYAML(t *testing.T, src string) yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("parse operator subtree: %v", err)
	}
	return doc
}

// auditSavingsRender renders one canonical call as JSON so a change to any nested
// value is visible even where a Go comparison would not catch it.
func auditSavingsRender(t *testing.T, call *aipapi.Call) string {
	t.Helper()
	encoded, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("canonical call is not encodable: %v", err)
	}
	return string(encoded)
}

// auditSavingsBacking returns the address of the first byte of every argument
// document the fixture selected, in fixture order.
func auditSavingsBacking(t *testing.T, call *aipapi.Call) []*byte {
	t.Helper()
	var out []*byte
	for i := range call.Items {
		item := &call.Items[i]
		if item.ToolCall == nil || len(item.ToolCall.Arguments) == 0 {
			continue
		}
		out = append(out, &item.ToolCall.Arguments[0])
	}
	for m := range call.Messages {
		for p := range call.Messages[m].Parts {
			part := &call.Messages[m].Parts[p]
			if len(part.Content) == 0 {
				continue
			}
			out = append(out, &part.Content[0])
		}
	}
	if len(out) == 0 {
		t.Fatal("fixture built no argument document to compare")
	}
	return out
}

// sameBackingPointers reports whether two address lists are element-wise identical.
func sameBackingPointers(before, after []*byte) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}

// auditSavingsExpectation derives the decoded-value byte totals one eligible fixture
// occurrence must contribute, from the mapping itself rather than from a constant.
//
// Deriving it from VirtualizePath is what keeps the fixture honest: the expected
// saving is the difference the real rewriter's own mapping decision produces, so the
// test states the requirement's claim ("the replacement is strictly shorter") without
// restating the tag algorithm, the separator rules, or the flavor normalization.
func (tc auditSavingsCase) auditSavingsExpectation(t *testing.T) (before, after int64) {
	t.Helper()
	if !tc.eligible {
		return 0, 0
	}
	sample := tc.sampleValue(0)
	mapping, reason := pathvirtualization.DeriveMapping(tc.flavor.root)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatalf("%s: fixture root is not one of the supported absolute forms", tc.name)
	}
	virtualized, matched := mapping.VirtualizePath(sample)
	if !matched {
		t.Fatalf("%s: fixture leaf is not a candidate under the fixture root", tc.name)
	}
	return int64(len(sample)), int64(len(virtualized))
}

// assertSavingsParity is the requirement 7.3 equality, made on the whole published
// outbound projection rather than on one counter, so a drift in any breakdown of the
// measurement fails rather than only a drift in the headline figure.
func assertSavingsParity(t *testing.T, tc auditSavingsCase, audit, mutated auditSavingsRun) {
	t.Helper()
	// The comparison is made on the RENDERED form rather than field by field, because
	// that is the form a metrics exporter receives and because it covers the series
	// this suite does not enumerate. The rendered values are deliberately NOT
	// interpolated into the failure message: they are the projection this feature
	// promises is content-free, so printing one on failure would make this suite the
	// leak it exists to rule out. A mismatch is reported by the integer that differs.
	if got, want := mustMarshal(t, audit.final.Outbound), mustMarshal(t, mutated.final.Outbound); got != want {
		t.Errorf("%s: audit and rewrite disagree about the same input's outbound measurement: %s",
			tc.name, firstSavingsCounterDifference(auditSavingsCounters(audit.final), auditSavingsCounters(mutated.final)))
	}
	// The generation-wide tally is compared separately because it is the figure an
	// operator actually reads, and because it is where the two directions could
	// disagree even with the outbound counters agreeing.
	if got, want := mustMarshal(t, audit.final.Total), mustMarshal(t, mutated.final.Total); got != want {
		t.Errorf("%s: audit and rewrite disagree about the generation tally: %s",
			tc.name, firstSavingsCounterDifference(savingsGenerationCounters(audit.final), savingsGenerationCounters(mutated.final)))
	}
	if audit.final.Outbound.Reports != auditSavingsOutboundPasses {
		t.Errorf("%s: outbound reports = %d, want %d: both outbound passes must have run",
			tc.name, audit.final.Outbound.Reports, auditSavingsOutboundPasses)
	}
}

// firstSavingsCounterDifference names the first published counter whose two values
// disagree, so a parity failure identifies itself without printing a projection.
func firstSavingsCounterDifference(audit, rewrite []savingsCounter) string {
	if len(audit) != len(rewrite) {
		return "the two projections published a different number of counters"
	}
	for i := range audit {
		if audit[i].value != rewrite[i].value {
			return fmt.Sprintf("%s = %d under audit and %d under rewrite",
				audit[i].name, audit[i].value, rewrite[i].value)
		}
	}
	return "the projections differ in a series rather than in a counter"
}

// savingsGenerationCounters narrows the counter enumeration to the generation-wide
// tally, for the second of the two parity comparisons.
func savingsGenerationCounters(s telemetry.Snapshot) []savingsCounter {
	var out []savingsCounter
	for _, counter := range auditSavingsCounters(s) {
		if strings.HasPrefix(counter.name, "total.") {
			out = append(out, counter)
		}
	}
	return out
}

// assertSavingsMeasured pins the absolute figure, not only the equality, because two
// modes can agree on zero and that is the case a parity test alone cannot rule out.
//
// passes is how many outbound passes the snapshot aggregates, because the run harness
// drives both of them and the figure an operator reads is the generation total rather
// than one pass's contribution.
func assertSavingsMeasured(t *testing.T, tc auditSavingsCase, snapshot telemetry.Snapshot, passes int64) {
	t.Helper()
	before, after := tc.auditSavingsExpectation(t)
	virtualized := snapshot.Outbound.Virtualized
	wantEligible := int64(0)
	if tc.eligible {
		wantEligible = int64(tc.occurrences) * passes
	}
	if virtualized.Eligible != wantEligible {
		t.Errorf("%s: eligible occurrences = %d, want %d", tc.name, virtualized.Eligible, wantEligible)
	}
	if virtualized.Rewritten != wantEligible {
		t.Errorf("%s: rewritten occurrences = %d, want %d: requirement 9.1 makes the alias strictly shorter",
			tc.name, virtualized.Rewritten, wantEligible)
	}
	if got, want := virtualized.BytesBefore, before*wantEligible; got != want {
		t.Errorf("%s: bytes before = %d, want %d decoded-value bytes", tc.name, got, want)
	}
	if got, want := virtualized.BytesAfter, after*wantEligible; got != want {
		t.Errorf("%s: bytes after = %d, want %d decoded-value bytes", tc.name, got, want)
	}
	if got, want := virtualized.BytesSaved, (before-after)*wantEligible; got != want {
		t.Errorf("%s: bytes saved = %d, want %d", tc.name, got, want)
	}
	if virtualized.BytesSaved < 0 {
		t.Errorf("%s: a published saving is negative (%d)", tc.name, virtualized.BytesSaved)
	}
	// The generation-wide figure is the OUTBOUND saving, and this fixture drives no
	// inbound pass, so it must be the same number rather than a net of two directions.
	if snapshot.Total.PassObservedOpportunityBytes != virtualized.BytesSaved {
		t.Errorf("%s: generation saving = %d, want the outbound figure %d",
			tc.name, snapshot.Total.PassObservedOpportunityBytes, virtualized.BytesSaved)
	}
	if tc.eligible && virtualized.BytesSaved <= 0 {
		t.Errorf("%s: the fixture measured no saving, so the parity assertion above would be vacuous",
			tc.name)
	}
}

// TestAuditReportsExactlyTheRewriteSavingIsRequirement73AtTheCompositionBoundary.
//
// It is the whole point of the mode an operator configures: a measured rollout is
// worth something only if switching to rewrite would realize what the measurement
// said. The comparison is exact and it is made on the published projection, across
// both canonical authorities, three occurrence magnitudes, three path flavors, and
// the case where neither mode has any eligible leaf to measure.
func TestAuditReportsExactlyTheRewriteSaving(t *testing.T) {
	t.Parallel()
	for _, tc := range auditSavingsCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			audit := tc.run(t, rewrite.ModeAudit, false)
			mutated := tc.run(t, rewrite.ModeRewrite, false)

			assertSavingsParity(t, tc, audit, mutated)
			assertSavingsMeasured(t, tc, audit.final, auditSavingsOutboundPasses)
			assertSavingsMeasured(t, tc, mutated.final, auditSavingsOutboundPasses)

			// Requirement 7.3's "without mutating canonical requests", asserted at
			// the same seam as the equality so the two cannot be separated: an audit
			// mode that mutated would be reporting a figure it had already spent.
			if !audit.bytesIdentical || !audit.backingShared {
				t.Errorf("%s: audit mode changed a canonical call (bytes identical=%t, backing shared=%t)",
					tc.name, audit.bytesIdentical, audit.backingShared)
			}
			if !audit.inertDecision {
				t.Errorf("%s: the early pass returned a decision carrying call detail", tc.name)
			}
			// The same fixture under rewrite must actually publish, or the equality
			// above would be satisfied by two inert modes.
			if tc.eligible && !mutated.mutated {
				t.Errorf("%s: rewrite mode published nothing, so audit's figure describes no rewrite", tc.name)
			}
			if tc.eligible && mutated.bytesIdentical {
				t.Errorf("%s: rewrite mode published no byte change despite eligible occurrences", tc.name)
			}
		})
	}
}

// TestEachOutboundPassMeasuresItsOwnInputIdenticallyInBothModes separates the two
// passes' contributions, which the aggregate equality deliberately hides.
//
// The aggregate is what an operator reads; the per-pass figures are what make that
// aggregate attributable. A drift confined to the LATE pass - the one that re-runs
// after request shaping - would be averaged into a matching total and never surface.
func TestEachOutboundPassMeasuresItsOwnInputIdenticallyInBothModes(t *testing.T) {
	t.Parallel()
	for _, tc := range auditSavingsCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			audit := tc.run(t, rewrite.ModeAudit, false)
			mutated := tc.run(t, rewrite.ModeRewrite, false)

			// First pass: exactly one report's worth of measurement in both modes.
			auditEarly, mutatedEarly := audit.afterEarly.Outbound, mutated.afterEarly.Outbound
			if got, want := auditEarly.Reports, int64(1); got != want {
				t.Errorf("%s: early-pass reports = %d, want %d", tc.name, got, want)
			}
			if got, want := mustMarshal(t, auditEarly.Virtualized), mustMarshal(t, mutatedEarly.Virtualized); got != want {
				t.Errorf("%s: the early outbound pass measured differently in the two modes: %s",
					tc.name, firstSavingsCounterDifference(
						savingsDirectionCounters("early_audit", auditEarly.Virtualized),
						savingsDirectionCounters("early_rewrite", mutatedEarly.Virtualized)))
			}

			// Second pass: the difference between the two snapshots. It is computed
			// rather than read off a second recorder because the bundle installs
			// exactly one, and a suite that built its own second recorder would be
			// asserting a property of the suite rather than of the shipped wiring.
			auditLate := auditSavingsDelta(audit.afterEarly, audit.final)
			mutatedLate := auditSavingsDelta(mutated.afterEarly, mutated.final)
			if got, want := auditSavingsDeltaJSON(t, audit.afterEarly, audit.final),
				auditSavingsDeltaJSON(t, mutated.afterEarly, mutated.final); got != want {
				t.Errorf("%s: the late outbound pass measured differently in the two modes: %s",
					tc.name, firstSavingsCounterDifference(
						savingsDirectionCounters("late_audit", auditLate),
						savingsDirectionCounters("late_rewrite", mutatedLate)))
			}

			// Task 12.1's pass breakdown makes the SAME per-pass comparison readable
			// without subtracting two snapshots: each pass wrote its own row, so the
			// published series IS the per-pass attribution this test used to reconstruct
			// by hand. Comparing the rendered series keeps the per-pass parity requirement
			// 7.3 states - and it is the assertion that would catch a wiring change that
			// stamped both passes with the same value.
			if got, want := mustMarshal(t, audit.final.Outbound.ByPass),
				mustMarshal(t, mutated.final.Outbound.ByPass); got != want {
				t.Errorf("%s: the per-pass breakdown differs between the two modes: %s vs %s",
					tc.name, got, want)
			}
		})
	}
}

// savingsDirectionCounters names one pass's contribution field by field, so a
// per-pass parity failure identifies the counter that moved.
func savingsDirectionCounters(prefix string, set telemetry.DirectionCounters) []savingsCounter {
	return []savingsCounter{
		{name: prefix + ".eligible", value: set.Eligible},
		{name: prefix + ".rewritten", value: set.Rewritten},
		{name: prefix + ".bytes_before", value: set.BytesBefore},
		{name: prefix + ".bytes_after", value: set.BytesAfter},
		{name: prefix + ".bytes_saved", value: set.BytesSaved},
		{name: prefix + ".skipped", value: set.Skipped},
	}
}

// auditSavingsDelta is one pass's own contribution to the generation tally, read as
// the difference between the recorder before and after it ran.
//
// BytesSaved is recomputed from the delta rather than subtracted, because the
// published figure is clamped at zero: subtracting two clamped figures would hide a
// negative per-pass delta instead of reporting it.
func auditSavingsDelta(before, after telemetry.Snapshot) telemetry.DirectionCounters {
	outboundBefore := before.Outbound.Virtualized
	outboundAfter := after.Outbound.Virtualized
	delta := telemetry.DirectionCounters{
		Eligible:    outboundAfter.Eligible - outboundBefore.Eligible,
		Rewritten:   outboundAfter.Rewritten - outboundBefore.Rewritten,
		BytesBefore: outboundAfter.BytesBefore - outboundBefore.BytesBefore,
		BytesAfter:  outboundAfter.BytesAfter - outboundBefore.BytesAfter,
	}
	delta.BytesSaved = delta.BytesBefore - delta.BytesAfter
	return delta
}

// auditSavingsDeltaJSON renders one pass's contribution as JSON so the comparison
// needs no field list that could omit a counter.
func auditSavingsDeltaJSON(t *testing.T, before, after telemetry.Snapshot) string {
	t.Helper()
	return mustMarshal(t, auditSavingsDelta(before, after))
}

// TestAReappliedOutboundPassMeasuresZeroOnlyAfterARewrite states the second half of
// production order, and it is stated here rather than left implicit because it is the
// reason the parity matrix above hands each pass its own input.
//
// In production both outbound passes see ONE candidate. A rewrite publishes the alias,
// so the late pass finds no real-root prefix and measures zero: requirement 2.9's
// idempotence, measured. An audit publishes nothing, so the late pass finds exactly
// what the early pass found and measures the same figure again - which is correct
// per-pass, since it measured its own input, and which is why the generation-wide
// total of an audit deployment counts an occurrence once per outbound pass.
//
// Task 11.2 left the generation total UNASSERTED because no correct answer had been
// decided: an operator reading an audit deployment could not recover the rewrite figure
// without knowing how many passes re-measured, and the measured ratio was exactly 2.00.
// Task 12.1 settled it by publishing the pass breakdown, so the total is asserted here
// as the arithmetic the two modes actually produce, and the per-candidate figure an
// operator needs is recovered from the early pass's own row. The full recovery rule is
// stated in audit_savings_pass_test.go; what this test owns is that the production
// ORDER still holds - the late pass contributes nothing after a rewrite and everything
// after an audit - and that the two are now told apart rather than summed.
func TestAReappliedOutboundPassMeasuresZeroOnlyAfterARewrite(t *testing.T) {
	t.Parallel()
	tc := auditSavingsCase{
		name:        "posix_items_occurrences_3_shared_candidate",
		flavor:      auditSavingsFlavors[0],
		occurrences: auditSavingsOccurrenceTiers[1],
		eligible:    true,
	}
	before, after := tc.auditSavingsExpectation(t)
	perPass := before - after
	if perPass <= 0 {
		t.Fatal("fixture measurement is not positive; the assertions below would be vacuous")
	}

	mutated := tc.run(t, rewrite.ModeRewrite, true)
	if first := auditSavingsDelta(zeroSnapshot(), mutated.afterEarly); first.Eligible != int64(tc.occurrences) {
		t.Errorf("the first rewrite pass reported %d eligible occurrences, want %d",
			first.Eligible, tc.occurrences)
	}
	if second := auditSavingsDelta(mutated.afterEarly, mutated.final); second.Eligible != 0 ||
		second.Rewritten != 0 || second.BytesSaved != 0 {
		t.Errorf("the second pass re-measured an already virtualized candidate: eligible=%d rewritten=%d saved=%d, want 0/0/0",
			second.Eligible, second.Rewritten, second.BytesSaved)
	}
	if got, want := mutated.final.Outbound.Virtualized.BytesSaved, perPass*int64(tc.occurrences); got != want {
		t.Errorf("generation saving = %d, want %d: one pass's worth on a candidate the second pass found idempotent",
			got, want)
	}
	// The generation total is now ASSERTED rather than left open.
	if got, want := mutated.final.Total.PassObservedOpportunityBytes, perPass*int64(tc.occurrences); got != want {
		t.Errorf("the rewrite generation total = %d, want %d", got, want)
	}
	// The late pass ran and found nothing, and says so with a report count rather than an
	// absent row: "ran and found nothing" and "never ran" are different operational facts.
	late, present := savingsPassRow(mutated.final, outbound.PassRequestPart.String())
	if !present {
		t.Error("the rewrite deployment published no late-pass row, so a pass that never ran is " +
			"indistinguishable from one that found nothing")
	} else if late.Reports != 1 || late.Eligible != 0 || late.BytesSaved != 0 {
		t.Errorf("the rewrite late-pass row = %+v, want one report and no measurement", late)
	}

	audit := tc.run(t, rewrite.ModeAudit, true)
	if first := auditSavingsDelta(zeroSnapshot(), audit.afterEarly); first.BytesSaved != perPass*int64(tc.occurrences) {
		t.Errorf("the first audit pass reported a saving of %d, want %d",
			first.BytesSaved, perPass*int64(tc.occurrences))
	}
	if second := auditSavingsDelta(audit.afterEarly, audit.final); second.BytesSaved != perPass*int64(tc.occurrences) {
		t.Errorf("the second audit pass reported a saving of %d, want %d: it measured the same unmutated input",
			second.BytesSaved, perPass*int64(tc.occurrences))
	}
	// The audit total is the sum of two equal per-pass contributions, and it is stated as
	// exactly that: the figure an operator recovers is the early row, which the
	// sibling suite proves equals the rewrite deployment's total.
	if got, want := audit.final.Total.PassObservedOpportunityBytes, 2*perPass*int64(tc.occurrences); got != want {
		t.Errorf("the audit generation total = %d, want %d: two passes each measured the same candidate",
			got, want)
	}
	if got, want := audit.final.Outbound.Virtualized.BytesSaved, 2*perPass*int64(tc.occurrences); got != want {
		t.Errorf("the audit outbound saving = %d, want %d", got, want)
	}
	// The mode's non-mutation contract is unchanged by the breakdown: audit still
	// publishes nothing.
	if !audit.bytesIdentical || !audit.backingShared {
		t.Error("audit mode changed a canonical call on a shared candidate")
	}
}

// zeroSnapshot is the recorder's state before any pass has run.
//
// Nothing observes before the first pass, so the delta over the first pass is that
// pass's whole contribution; reading it from the shipped zero value rather than from a
// hand-written zero keeps the arithmetic on the type the projection actually publishes.
func zeroSnapshot() telemetry.Snapshot {
	return telemetry.Snapshot{}
}

// TestInboundExpansionIsPublishedAsGrowthAndNeverAsANegativeSaving is requirement
// 9.1 read from the direction where the sign actually flips.
//
// The outbound replacement is the alias, which requirement 9.1 guarantees is strictly
// shorter, so its delta is a saving by construction. The INBOUND replacement is the
// real root, which is necessarily longer than the alias it replaces: expanding an
// alias back to a filesystem path COSTS bytes. Summing that into "bytes saved" would
// report a heavily virtualizing deployment as losing on every expansion, which is
// arithmetically true and operationally useless - so it is published as growth, and
// the saving figure stays zero.
//
// Both modes are driven because the inbound finalizer measures the same payload in
// both: an audit deployment still has to know what expansion costs, and it learns it
// from the identical detection pass.
func TestInboundExpansionIsPublishedAsGrowthAndNeverAsANegativeSaving(t *testing.T) {
	t.Parallel()
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			tel, b := auditSavingsBundle(t, mode)
			root := auditSavingsFlavors[0].root
			mapping, reason := pathvirtualization.DeriveMapping(root)
			if reason != pathvirtualization.SkipReasonNone {
				t.Fatal("fixture root is not one of the supported absolute forms")
			}
			virtual := mapping.VirtualRoot + "pkg" + auditSavingsFlavors[0].sep + "file_00.go"
			// The growth is derived from the two rendered values rather than from the
			// two root lengths: the mapper's splice consumes the separator between the
			// matched root and the carried suffix, so len(root)-len(VirtualRoot) is one
			// byte short of the figure a real expansion produces. Comparing against the
			// mapper's own published value keeps the assertion exact either way.
			expanded, result := mapping.ExpandPath(virtual)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatal("the fixture alias is not one this mapping expands")
			}
			growth := int64(len(expanded) - len(virtual))

			driveInboundSavings(t, tel, b, root, virtual)

			snapshot := tel.Snapshot()
			restore := snapshot.Inbound.Restore
			if snapshot.Inbound.Reports != 1 {
				t.Fatalf("inbound reports = %d, want 1", snapshot.Inbound.Reports)
			}
			// The precondition of the whole assertion: the raw inbound delta really is
			// negative, so the clamp and the growth figure are being exercised rather
			// than a case where subtraction would have been positive anyway.
			if restore.BytesAfter <= restore.BytesBefore {
				t.Fatalf("fixture does not lengthen: before=%d after=%d", restore.BytesBefore, restore.BytesAfter)
			}
			if restore.BytesSaved != 0 {
				t.Errorf("inbound saving = %d, want 0: expansion grows the value and a negative delta is not a saving",
					restore.BytesSaved)
			}
			if got, want := snapshot.Inbound.BytesGrown(), growth; got != want {
				t.Errorf("inbound growth = %d, want %d", got, want)
			}
			if got, want := restore.BytesBefore, int64(len(virtual)); got != want {
				t.Errorf("inbound bytes before = %d, want the alias's decoded length %d", got, want)
			}
			// The published total is the real root's decoded length, which is the alias's
			// length plus exactly the growth above: the suffix is carried through
			// untouched, so the delta is the root/alias difference alone.
			if got, want := restore.BytesAfter, restore.BytesBefore+growth; got != want {
				t.Errorf("inbound bytes after = %d, want %d", got, want)
			}
			// The inbound delta must never reach the generation-wide saving figure. A
			// deployment whose operator reads that number is sizing a rollout from it.
			if snapshot.Total.PassObservedOpportunityBytes != 0 {
				t.Errorf("generation saving = %d, want 0: the inbound delta is a cost, not a saving",
					snapshot.Total.PassObservedOpportunityBytes)
			}
			// And the mode really did differ: rewrite published the expansion, audit
			// published nothing and said so with the bounded audit-mode reason.
			if mode == rewrite.ModeRewrite {
				if snapshot.Inbound.Expanded != 1 || snapshot.Inbound.Rejected != 0 {
					t.Errorf("rewrite did not publish the expansion: expanded=%d rejected=%d",
						snapshot.Inbound.Expanded, snapshot.Inbound.Rejected)
				}
				if counted := auditSavingsReasonCount(snapshot, expansion.ReasonExpanded.String()); counted != 1 {
					t.Errorf("reason %q counted %d times, want 1", expansion.ReasonExpanded.String(), counted)
				}
			} else {
				if snapshot.Inbound.Expanded != 0 || snapshot.Inbound.Noop != 1 {
					t.Errorf("audit published an expansion: expanded=%d noop=%d",
						snapshot.Inbound.Expanded, snapshot.Inbound.Noop)
				}
				// The measuring mode must still say WHY it published nothing, and the
				// answer is its own bounded label rather than the mutating one.
				if counted := auditSavingsReasonCount(snapshot, expansion.ReasonAuditMode.String()); counted != 1 {
					t.Errorf("reason %q counted %d times, want 1", expansion.ReasonAuditMode.String(), counted)
				}
				if counted := auditSavingsReasonCount(snapshot, expansion.ReasonExpanded.String()); counted != 0 {
					t.Errorf("audit reported the expansion reason %d times, want 0", counted)
				}
			}
		})
	}
}

// TestANegativeDeltaIsClampedRatherThanPublishedInEitherDirection pins the defensive
// half of the same rule.
//
// Requirement 9.1 makes a negative OUTBOUND delta unreachable through the rewriter,
// because the only outbound replacement is a strictly shorter alias. The projection
// must not DEPEND on that guarantee to stay truthful, because a future mutation path,
// a different mapping, or a manually constructed report would otherwise publish a
// negative "bytes saved" - the single most misleading number this feature could
// report, and one an operator would read as a fact about their deployment.
func TestANegativeDeltaIsClampedRatherThanPublishedInEitherDirection(t *testing.T) {
	t.Parallel()
	tel, _ := auditSavingsBundle(t, rewrite.ModeRewrite)

	// One observation whose declared replacement is LONGER than its input.
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats: rewrite.Stats{
			Eligible: 1, Rewritten: 1,
			BytesBefore: 10, BytesAfter: 99,
		},
	})
	snapshot := tel.Snapshot()
	if got := snapshot.Outbound.Virtualized.BytesSaved; got != 0 {
		t.Errorf("a growing outbound delta was published as a saving of %d, want 0", got)
	}
	// Clamping the SAVING must not clamp the MEASUREMENT: the raw totals are what
	// requirement 7.6 asks for, and they are accumulated exactly as measured.
	if got, want := snapshot.Outbound.Virtualized.BytesBefore, int64(10); got != want {
		t.Errorf("bytes before = %d, want the raw total %d", got, want)
	}
	if got, want := snapshot.Outbound.Virtualized.BytesAfter, int64(99); got != want {
		t.Errorf("bytes after = %d, want the raw total %d", got, want)
	}

	// The same clamp on the inbound direction, whose negative delta is the reachable
	// case: growth is published, the saving stays zero.
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeExpanded, Reason: expansion.ReasonExpanded,
		Stats: rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 12, BytesAfter: 101},
	})
	snapshot = tel.Snapshot()
	if got := snapshot.Inbound.Restore.BytesSaved; got != 0 {
		t.Errorf("a growing inbound delta was published as a saving of %d, want 0", got)
	}
	if got, want := snapshot.Inbound.BytesGrown(), int64(89); got != want {
		t.Errorf("inbound growth = %d, want %d", got, want)
	}
	if snapshot.Total.PassObservedOpportunityBytes != 0 {
		t.Errorf("generation opportunity = %d, want 0", snapshot.Total.PassObservedOpportunityBytes)
	}

	// And the generation total clamps PER OBSERVATION rather than summing raw
	// negatives: a subsequent genuine saving must not be reduced by the earlier
	// negative one, which is what a sum of signed deltas would do.
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats: rewrite.Stats{
			Eligible: 1, Rewritten: 1, BytesBefore: 100, BytesAfter: 40,
		},
	})
	snapshot = tel.Snapshot()
	if got, want := snapshot.Total.PassObservedOpportunityBytes, int64(60); got != want {
		t.Errorf("generation saving = %d, want the genuine %d: a clamped negative must not subtract from it", got, want)
	}
	// And nothing the projection publishes reads negative, in either direction, after
	// an observation that tried to. Every published counter is enumerated rather than
	// inferred from the rendering, because a string search for a minus sign would be
	// satisfied by a hyphen inside a bounded label.
	for _, counter := range auditSavingsCounters(snapshot) {
		if counter.value < 0 {
			t.Errorf("%s is published as %d; a metric that can read negative is the most "+
				"misleading number this feature could report", counter.name, counter.value)
		}
	}
}

// savingsCounter pairs one published counter with the bounded name this suite reports
// it under, so a failure identifies the field without printing the snapshot.
type savingsCounter struct {
	name  string
	value int64
}

// auditSavingsCounters enumerates every integer the projection publishes, in the three
// value families it composes them from: the generation tally, each direction's
// occurrence/replacement/byte accounting, and each direction's pass-level scalars.
//
// The series counts are omitted because they are published as bounded labels plus a
// count, and the counts are checked where the series are read.
func auditSavingsCounters(s telemetry.Snapshot) []savingsCounter {
	counters := []savingsCounter{
		{name: "total.reports", value: s.Total.Reports},
		{name: "total.eligible", value: s.Total.Eligible},
		{name: "total.rewritten", value: s.Total.Rewritten},
		{name: "total.pass_observed_opportunity_bytes", value: s.Total.PassObservedOpportunityBytes},
		{name: "total.skipped", value: s.Total.Skipped},
	}
	for _, direction := range []struct {
		prefix string
		set    telemetry.DirectionCounters
	}{
		{prefix: "outbound.virtualized", set: s.Outbound.Virtualized},
		{prefix: "inbound.restore", set: s.Inbound.Restore},
	} {
		counters = append(counters,
			savingsCounter{name: direction.prefix + ".eligible", value: direction.set.Eligible},
			savingsCounter{name: direction.prefix + ".rewritten", value: direction.set.Rewritten},
			savingsCounter{name: direction.prefix + ".bytes_before", value: direction.set.BytesBefore},
			savingsCounter{name: direction.prefix + ".bytes_after", value: direction.set.BytesAfter},
			savingsCounter{name: direction.prefix + ".bytes_saved", value: direction.set.BytesSaved},
			savingsCounter{name: direction.prefix + ".skipped", value: direction.set.Skipped},
		)
	}
	counters = append(counters,
		savingsCounter{name: "outbound.reports", value: s.Outbound.Reports},
		savingsCounter{name: "outbound.transform_failed", value: s.Outbound.TransformFailed},
		savingsCounter{name: "outbound.root_unusable", value: s.Outbound.RootUnusable},
		savingsCounter{name: "outbound.workspace_unresolved", value: s.Outbound.WorkspaceUnresolved},
		savingsCounter{name: "inbound.reports", value: s.Inbound.Reports},
		savingsCounter{name: "inbound.expanded", value: s.Inbound.Expanded},
		savingsCounter{name: "inbound.noop", value: s.Inbound.Noop},
		savingsCounter{name: "inbound.rejected", value: s.Inbound.Rejected},
		savingsCounter{name: "inbound.mandatory_overflows", value: s.Inbound.MandatoryOverflows},
	)
	// The per-pass breakdown publishes integers too, so it belongs in the enumeration
	// that proves nothing this feature publishes can read negative. A row's LABEL is not
	// enumerated: it is a bounded closed-set member, checked where the series is read, and
	// a hyphen inside one of those labels is not a negative.
	return append(counters, savingsPassCounterFields(s)...)
}

// savingsPassCounterFields enumerates every integer the per-pass breakdown publishes, so
// the "no published counter reads negative" sweep covers the new dimension too.
func savingsPassCounterFields(s telemetry.Snapshot) []savingsCounter {
	var out []savingsCounter
	for _, row := range s.Outbound.ByPass {
		prefix := "outbound.by_pass." + row.Pass
		out = append(out,
			savingsCounter{name: prefix + ".reports", value: row.Reports},
			savingsCounter{name: prefix + ".eligible", value: row.Eligible},
			savingsCounter{name: prefix + ".rewritten", value: row.Rewritten},
			savingsCounter{name: prefix + ".bytes_before", value: row.BytesBefore},
			savingsCounter{name: prefix + ".bytes_after", value: row.BytesAfter},
			savingsCounter{name: prefix + ".bytes_saved", value: row.BytesSaved},
		)
	}
	return out
}

// The hostile fixture for the content-freedom assertions. Every content-bearing input
// the feature handles is present at once: a private project root, the alias and
// workspace tag derived from it, a private tool name, a JSON Pointer, a call ID, and
// a reserved alias carrying the current tag. A configuration that leaks is one where
// all of it is actually in scope.
const (
	auditSavingsPrivateRoot = "/home/dev/private/builds/aiproxer-example-project-monorepo" +
		"/services/interactive-proxy/.worktrees/b-leg-audit-savings-private-fixture"
	auditSavingsPrivateTool    = "acme_internal_read_secrets"
	auditSavingsPrivatePointer = "/payload/secret_path"
	auditSavingsPrivateCallID  = "call_7f3a_2b19"
	auditSavingsPrivateSuite   = `{"type":"object","properties":{"payload":{"type":"object",` +
		`"properties":{"secret_path":{"type":"string"}}}}}`
	auditSavingsPrivateSuffix = "pkg/aipapi/call.go"
	// auditSavingsStaleAlias is a well-formed alias carrying a DIFFERENT workspace
	// tag, which is the fail-closed case requirement 6.5 and 8.3 exist for. It is a
	// marker too: nothing about it may be published either.
	auditSavingsStaleAlias = "/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/pkg/aipapi/call.go"
)

// auditSavingsPrivateSubtree is the fully configured operator subtree: enabled,
// one mode, schema inference on, a declared mandatory bound, and one declared profile
// carrying the private tool name and the private JSON Pointer.
func auditSavingsPrivateSubtree(mode rewrite.Mode) string {
	return "enabled: true\nmode: " + mode.String() + "\nschema_inference: true\n" +
		"mandatory_max_args_bytes: 65536\n" +
		"tool_profiles:\n  - names: [\"" + auditSavingsPrivateTool + "\"]\n" +
		"    arg_json_pointers: [\"" + auditSavingsPrivatePointer + "\"]\n"
}

// auditSavingsPrivateBundle builds the hostile configuration in one mode.
func auditSavingsPrivateBundle(t *testing.T, mode rewrite.Mode) (*telemetry.Telemetry, aipfeature.FeatureBundle) {
	t.Helper()
	resolved, err := config.Decode(auditSavingsYAML(t, auditSavingsPrivateSubtree(mode)))
	if err != nil {
		t.Fatalf("Decode(%s): %v", mode, err)
	}
	tel, b, err := bundle.FeatureBundleWithTelemetry(resolved)
	if err != nil {
		t.Fatalf("%s: FeatureBundleWithTelemetry: %v", mode, err)
	}
	return tel, b
}

// TestNoRenderedMetricCarriesContentInEitherMode is requirement 7.7 at the seam an
// operator's metrics exporter actually reads, asserted in BOTH modes.
//
// One mode is not enough here, and that is the reason this suite exists rather than
// deferring to the telemetry package's own hostile fixture: the mode is the only input
// that differs between the two registrations, so a leak introduced by the mode branch
// would be invisible to a single-mode check. The counters under test are driven
// non-zero - a real outbound publication, an inbound expansion, and a fail-closed
// stale alias - because a projection that leaks nothing because it recorded nothing
// would pass the assertion trivially.
func TestNoRenderedMetricCarriesContentInEitherMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite} {
		t.Run(mode.String(), func(t *testing.T) {
			t.Parallel()
			tel, b := auditSavingsPrivateBundle(t, mode)
			drivePrivateSavings(t, tel, b)

			snapshot := tel.Snapshot()
			// Non-vacuity, per direction: the content assertions below are only
			// meaningful over counters that actually moved.
			if snapshot.Outbound.Reports != 2 || snapshot.Outbound.Virtualized.BytesSaved <= 0 {
				t.Fatalf("the outbound fixture recorded reports=%d saving=%d, want 2 and a positive saving",
					snapshot.Outbound.Reports, snapshot.Outbound.Virtualized.BytesSaved)
			}
			if snapshot.Inbound.Reports != 2 || snapshot.Inbound.BytesGrown() <= 0 {
				t.Fatalf("the inbound fixture recorded reports=%d growth=%d, want 2 and positive growth",
					snapshot.Inbound.Reports, snapshot.Inbound.BytesGrown())
			}
			if snapshot.Inbound.Rejected != 1 {
				t.Errorf("the stale-alias refusal counted %d rejections, want 1", snapshot.Inbound.Rejected)
			}

			// The positive control, and it is what makes the negative one mean anything:
			// every byte this suite searches for MUST be present in the fixture the
			// components were driven with. A marker list that quietly stopped matching
			// the fixture - a renamed constant, a retagged alias, a changed private tool
			// name - would make every assertion below pass while checking nothing.
			assertSavingsMarkersAreReachable(t, auditSavingsPrivateFixtureSurface(t))

			markers := auditSavingsMarkers()
			assertNoSavingsContent(t, "snapshot", mustMarshal(t, snapshot), markers)
			assertNoSavingsContent(t, "inventory", mustMarshal(t, tel.Inventory()), markers)
			for _, tally := range savingsAllTallies(snapshot) {
				if !isSavingsBoundedLabel(tally.Reason) {
					t.Error("a published series label is not a bounded token (requirements.md 7.7)")
				}
				if tally.Count <= 0 {
					t.Error("a published series entry carries a non-positive count")
				}
			}
		})
	}
}

// TestOutOfVocabularyObservationsStillFoldIntoABoundedSlotInAuditMode is the
// cardinality half of requirement 7.7, on the recorder an audit deployment installs.
//
// The shipped components only ever produce members of their own closed vocabularies,
// so the folding cannot be reached from the outside. It is still load-bearing: the
// exported named types accept any value a caller assembled, and a fold is what keeps
// the series bounded and the label content-free for such a value instead of indexing
// out of range, vanishing, or exporting the raw value as an unbounded number.
//
// The hostile observations are handed to the SAME recorder the bundle installed for
// the audit generation, so what is being shown is that an audit deployment's published
// surface is bounded by construction - not that a standalone recorder would be.
func TestOutOfVocabularyObservationsStillFoldIntoABoundedSlotInAuditMode(t *testing.T) {
	t.Parallel()
	tel, _ := auditSavingsBundle(t, rewrite.ModeAudit)

	const hostileObservations = 64
	for i := range hostileObservations {
		// Ordinals outside every closed vocabulary, plus root-refusal codes carrying a
		// real root, a derived alias, and a workspace tag - the exact values that
		// would export as unbounded numbers or as path bytes under a naive map.
		tel.ObserveOutbound(outbound.Report{
			Outcome:    outbound.Outcome(100 + i),
			RootReason: pathvirtualization.SkipReason(auditSavingsPrivateRoot),
			Stats: rewrite.Stats{
				Eligible: i, Rewritten: i, BytesBefore: i * 3, BytesAfter: i,
				Skips: []rewrite.Skip{
					{Reason: rewrite.SkipReason(100 + i), Count: 1},
					{Reason: rewrite.SkipReason(i % 12), Count: 1},
				},
			},
		})
		tel.ObserveExpansion(expansion.Report{
			Outcome:    expansion.Outcome(100 + i),
			Reason:     expansion.Reason(100 + i),
			RootReason: pathvirtualization.SkipReason(auditSavingsStaleAlias),
			Stats: rewrite.Stats{
				Eligible: i, Rewritten: i, BytesBefore: i, BytesAfter: i * 3,
				Skips: []rewrite.Skip{{Reason: rewrite.SkipReason(100 + i), Count: 1}},
			},
		})
	}

	snapshot := tel.Snapshot()
	outboundOutcomes := savingsVocabularySize(func(i int) string { return outbound.Outcome(i).String() })
	expansionOutcomes := savingsVocabularySize(func(i int) string { return expansion.Outcome(i).String() })
	expansionReasons := savingsVocabularySize(func(i int) string { return expansion.Reason(i).String() })
	skipReasons := savingsVocabularySize(func(i int) string { return rewrite.SkipReason(i).String() })

	for _, series := range []struct {
		name  string
		held  int
		bound int
	}{
		{name: "outbound_outcomes", held: len(snapshot.Outbound.Outcomes), bound: outboundOutcomes},
		{name: "outbound_root_reasons", held: len(snapshot.Outbound.RootReasons), bound: savingsRootReasonSlots},
		{name: "outbound_skips", held: len(snapshot.Outbound.Virtualized.Skips), bound: skipReasons},
		{name: "expansion_outcomes", held: len(snapshot.Inbound.Outcomes), bound: expansionOutcomes},
		{name: "expansion_reasons", held: len(snapshot.Inbound.Reasons), bound: expansionReasons},
		{name: "expansion_root_reasons", held: len(snapshot.Inbound.RootReasons), bound: savingsRootReasonSlots},
		{name: "expansion_skips", held: len(snapshot.Inbound.Restore.Skips), bound: skipReasons},
	} {
		if series.held > series.bound {
			t.Errorf("%s holds %d entries, want at most the %d its closed vocabulary allows",
				series.name, series.held, series.bound)
		}
	}
	// Every hostile value landed in ONE bounded slot per series rather than in 64
	// distinct ones, which is the property the fold exists for. Because no genuine
	// expansion reason was fed, the whole series is that one slot.
	if len(snapshot.Inbound.Reasons) != 1 {
		t.Errorf("the expansion reason series holds %d entries, want exactly the one bounded slot "+
			"every out-of-vocabulary value folds into", len(snapshot.Inbound.Reasons))
	} else if got := snapshot.Inbound.Reasons[0].Count; got != hostileObservations {
		t.Errorf("the bounded expansion-reason slot counted %d, want the %d out-of-vocabulary values",
			got, hostileObservations)
	}
	if got := snapshot.Outbound.Outcomes[len(snapshot.Outbound.Outcomes)-1].Count; got != hostileObservations {
		t.Errorf("the bounded outbound-outcome slot counted %d, want the %d out-of-vocabulary values",
			got, hostileObservations)
	}
	for _, tally := range savingsAllTallies(snapshot) {
		if !isSavingsBoundedLabel(tally.Reason) {
			t.Error("a published series label is not a bounded token (requirements.md 7.7)")
		}
	}
	assertNoSavingsContent(t, "hostile snapshot", mustMarshal(t, snapshot), auditSavingsMarkers())
}

// drivePrivateSavings runs all three shipped components over hostile content: the two
// outbound passes over a private-root path-bearing call, the inbound finalizer over an
// alias the current mapping owns, and the inbound finalizer over a stale-workspace
// alias it must refuse.
func drivePrivateSavings(t *testing.T, tel *telemetry.Telemetry, b aipfeature.FeatureBundle) {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(auditSavingsPrivateRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatal("the private fixture root is not one of the supported absolute forms")
	}
	if mapping.VirtualRoot == "" {
		t.Fatal("the private fixture root derives no active alias")
	}
	target := auditSavingsPrivateRoot + "/" + auditSavingsPrivateSuffix
	virtual := mapping.VirtualRoot + auditSavingsPrivateSuffix

	ctx := pinnedWorkspace(auditSavingsPrivateRoot)
	meta := request.AttemptMeta{
		Workspace: aipworkspace.WorkspaceView{ID: "ws_private", ProjectRoot: auditSavingsPrivateRoot},
	}
	transform := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)[0]
	hook := aipfeature.Get(b.PlaneSet, aipfeature.PlaneRequestPartHooks)[0]
	finalizer := aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0]

	// Two independent candidates: one for the early outbound pass and one for the
	// late one, so each pass measures a fresh client-shaped input and the two reports
	// stay individually attributable.
	early := auditSavingsPrivateCall(t, target)
	late := auditSavingsPrivateCall(t, target)
	if _, err := transform.HandleAttempt(ctx, early, meta, request.Services{}); err != nil {
		t.Fatal("the early outbound pass must never return an error")
	}
	if err := hook.HandleRequestParts(ctx, late, sdkhooks.PartMeta{}); err != nil {
		t.Fatal("the late outbound pass must never return an error")
	}

	tool := aipapi.ToolDef{Name: auditSavingsPrivateTool, Parameters: json.RawMessage(auditSavingsPrivateSuite)}
	for _, alias := range []string{virtual, auditSavingsStaleAlias} {
		result, err := finalizer.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: auditSavingsPrivateCallID, ToolName: auditSavingsPrivateTool,
			ArgsJSON: json.RawMessage(`{"payload":{"secret_path":"` + alias + `"}}`),
		}, tool, nil, toolcall.Meta{Workspace: meta.Workspace})
		if err != nil {
			t.Fatal("the expansion finalizer must never return an error")
		}
		if result.ReasonCode == "" || !isSavingsBoundedLabel(result.ReasonCode) {
			t.Error("a published reason code is not a bounded token (requirements.md 7.7)")
		}
	}
}

// auditSavingsPrivateCall builds one hostile item-authoritative call whose tool name
// and argument shape match the private declared profile, so the pass really selects
// and rewrites the leaf rather than skipping it.
func auditSavingsPrivateCall(t *testing.T, target string) *aipapi.Call {
	t.Helper()
	return &aipapi.Call{
		ID: auditSavingsPrivateCallID,
		Tools: []aipapi.ToolDef{{
			Name:       auditSavingsPrivateTool,
			Parameters: json.RawMessage(auditSavingsPrivateSuite),
		}},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindToolCall, ID: "item_private", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: auditSavingsPrivateCallID, Name: auditSavingsPrivateTool,
				Arguments: json.RawMessage(`{"payload":{"secret_path":"` + target +
					`"},"note":"` + auditSavingsPrivateRoot + `/README.md"}`),
			},
		}},
	}
}

// driveInboundSavings runs only the expansion finalizer, so the inbound figures this
// test asserts on are the inbound direction's own and cannot be diluted by an
// outbound pass.
func driveInboundSavings(t *testing.T, tel *telemetry.Telemetry, b aipfeature.FeatureBundle,
	root, virtual string,
) {
	t.Helper()
	tool := aipapi.ToolDef{Name: auditSavingsTool, Parameters: json.RawMessage(auditSavingsDeclaredSchema)}
	view := aipworkspace.WorkspaceView{ID: auditSavingsWorkspaceID, ProjectRoot: root}
	finalizer := aipfeature.Get(b.PlaneSet, aipfeature.PlaneToolCallFinalizers)[0]
	result, err := finalizer.Finalize(pinnedWorkspace(root), toolcall.CompletedCall{
		ToolCallID: "call_inbound", ToolName: auditSavingsTool,
		ArgsJSON: json.RawMessage(auditSavingsArguments(t, virtual)),
	}, tool, nil, toolcall.Meta{Workspace: view})
	if err != nil {
		t.Fatal("the expansion finalizer must never return an error")
	}
	if result.ReasonCode == "" || !isSavingsBoundedLabel(result.ReasonCode) {
		t.Error("a published reason code is not a bounded token (requirements.md 7.7)")
	}
}

// isSavingsBoundedLabel reports whether a published dimension is a single fixed token:
// no separator, no path-ish byte, and no unbounded run of text.
//
// It is deliberately crude so that it cannot be satisfied by a value that merely
// happens to be absent from the marker list, and it is a property of the SHAPE rather
// than of a table of expected labels: a series that grows a new legitimate member
// still has to spell it as one lowercase token.
func isSavingsBoundedLabel(label string) bool {
	if label == "" || len(label) > 48 {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// auditSavingsPrivateFixtureSurface renders every byte the hostile fixture actually
// carries: the canonical call, the argument document the alias expansion saw, the
// stale alias, and the operator subtree.
//
// It exists for the positive control. A leak check is only as good as its marker list,
// and a marker list derived from the same constants the fixture is built from can stop
// matching the moment either side changes. Requiring every marker to be FOUND in this
// rendering turns that silent divergence into a failure: if the fixture no longer
// contains the bytes the suite searches for, the suite says so rather than reporting a
// clean run.
//
// The check deliberately covers a subset of the marker list: the outside-path marker
// belongs to the parity corpus rather than to the hostile fixture, and requiring it
// here would assert a fixture shape this one does not have.
func auditSavingsPrivateFixtureSurface(t *testing.T) string {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(auditSavingsPrivateRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatal("the private fixture root is not one of the supported absolute forms")
	}
	surface := strings.Join([]string{
		auditSavingsRender(t, auditSavingsPrivateCall(t, auditSavingsPrivateRoot+"/"+auditSavingsPrivateSuffix)),
		auditSavingsPrivateSubtree(rewrite.ModeRewrite),
		auditSavingsPrivateTool + auditSavingsPrivateCallID + auditSavingsStaleAlias,
		mapping.VirtualRoot + auditSavingsPrivateSuffix,
	}, "\n")
	return surface
}

// assertSavingsMarkersAreReachable is the positive control over the marker list.
func assertSavingsMarkersAreReachable(t *testing.T, surface string) {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(auditSavingsPrivateRoot)
	if reason != pathvirtualization.SkipReasonNone {
		t.Fatal("the private fixture root is not one of the supported absolute forms")
	}
	for _, marker := range []string{
		auditSavingsPrivateRoot,
		mapping.RealRoot,
		mapping.VirtualRoot,
		mapping.WorkspaceTag,
		auditSavingsPrivateTool,
		auditSavingsPrivateCallID,
		auditSavingsPrivatePointer,
		auditSavingsStaleAlias,
		auditSavingsPrivateSuffix,
	} {
		if !strings.Contains(surface, marker) {
			t.Fatalf("the hostile fixture no longer carries one searched byte (%d bytes); "+
				"the content-freedom assertions would be vacuous", len(marker))
		}
	}
}

// auditSavingsMarkers returns every byte a leak of the hostile fixture would carry.
//
// The root, the alias, and the workspace tag are DERIVED from the fixture root rather
// than copied from the source, because the whole point is to catch the exact bytes the
// mapper produces; a hand-written marker would go stale silently the moment the tag
// algorithm changed, which is precisely when a leak would be hardest to notice.
func auditSavingsMarkers() []string {
	markers := []string{
		"/.__aip_v1__",
		auditSavingsPrivateTool,
		auditSavingsPrivatePointer,
		auditSavingsPrivateCallID,
		auditSavingsPrivateSuffix,
		auditSavingsStaleAlias,
		auditSavingsOutsidePath,
		"acme_internal",
		"secret_path",
	}
	for _, root := range []string{auditSavingsPrivateRoot} {
		mapping, _ := pathvirtualization.DeriveMapping(root)
		markers = append(markers, root, mapping.RealRoot, mapping.VirtualRoot, mapping.WorkspaceTag)
	}
	return markers
}

// savingsRootReasonSlots is the size of the root-refusal series: one slot per member
// of the lexical core's closed refusal vocabulary plus one bounded slot for a code
// outside it. It is stated here rather than imported because the series size is an
// unexported implementation fact of the projection, and the assertion that matters is
// only that the series cannot grow with traffic.
const savingsRootReasonSlots = 7

// savingsVocabularySize counts one closed enum's members by walking its ordinals until
// its own total String method answers "unknown".
//
// Deriving the bound from the vocabulary rather than copying a constant beside it is
// what keeps this test honest across a future member: a hardcoded size would silently
// start failing, or silently stop bounding, the day the vocabulary changed.
func savingsVocabularySize(label func(int) string) int {
	const ceiling = 256
	for size := range ceiling {
		if label(size) == "unknown" {
			return size
		}
	}
	return ceiling
}

// savingsAllTallies flattens every series a snapshot publishes, so the bounded-label
// check runs over all of them rather than over the one a test happened to read.
func savingsAllTallies(s telemetry.Snapshot) []telemetry.Tally {
	var out []telemetry.Tally
	out = append(out, s.Outbound.Outcomes...)
	out = append(out, s.Outbound.RootReasons...)
	out = append(out, s.Outbound.Virtualized.Skips...)
	out = append(out, s.Inbound.Outcomes...)
	out = append(out, s.Inbound.Reasons...)
	out = append(out, s.Inbound.RootReasons...)
	out = append(out, s.Inbound.Restore.Skips...)
	return out
}

// assertNoSavingsContent fails when a rendered observable surface carries any byte the
// hostile fixture put in scope. Only the marker itself is named, never the rendered
// value, so this suite's own failure path cannot become the leak it is hunting.
func assertNoSavingsContent(t *testing.T, label, rendered string, markers []string) {
	t.Helper()
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		if strings.Contains(rendered, marker) {
			t.Errorf("the rendered %s carries content (requirements.md 7.7)", label)
		}
	}
}

// auditSavingsReasonCount returns how many times one bounded label appears in a
// snapshot's expansion reason series.
func auditSavingsReasonCount(s telemetry.Snapshot, label string) int64 {
	var counted int64
	for _, tally := range s.Inbound.Reasons {
		if tally.Reason == label {
			counted = tally.Count
		}
	}
	return counted
}
