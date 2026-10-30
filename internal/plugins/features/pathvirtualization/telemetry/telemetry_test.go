package telemetry_test

// This file is the RED half of the feature's content-free metrics/inventory
// projection, requirements.md 7.6, 7.7, 7.8 and 9.5.
//
// What is being pinned, in the order the requirements state it:
//
//   - 7.8 INVENTORY. Enablement, mode, and the bounded configuration shape come from
//     config.Resolved.Shape() and from nothing else. A tool name, a JSON Pointer, a
//     vocabulary key, and a project root are all operator input and all potentially
//     sensitive, so the inventory reports the SHAPE of a configuration and never its
//     content.
//
//   - 7.6 COUNTERS. Eligible occurrences, rewritten occurrences, skipped occurrences by
//     bounded reason, bytes-before, bytes-after, bytes-saved, expansion failures, and
//     mandatory-buffer overflows. Every one of those is either a count or a byte total,
//     and every dimension they are broken down by is a closed enum.
//
//   - 7.7 CONTENT FREEDOM. No real path, virtualized path suffix, payload byte, tool
//     name, tool-call ID, or high-cardinality hash may appear in any label, dimension,
//     or value. The hostile-payload case is the interesting one, because a benign
//     fixture cannot tell a bounded dimension from a content-bearing one.
//
//   - 9.5 REALIZED SAVINGS. An operator must be able to compute realized byte savings
//     per request/turn from what is recorded, and the figure must never be applied when
//     it is negative.
//
// The saving accounting deserves its own note before any test reads it, because it is
// the one number here that can be wrong in a way that flatters the feature. Both
// directions of this feature REPLACE a decoded string value with another decoded string
// value, and the shared engine measures DECODED-VALUE lengths in both cases. In the
// OUTBOUND direction the replacement is the alias, which requirement 9.1 guarantees is
// strictly shorter than the real root, so before - after is a genuine saving. In the
// INBOUND direction the replacement is the real root, which is necessarily LONGER than
// the alias it replaced, so before - after is NEGATIVE: expanding an alias back to a
// real path costs bytes rather than saving them. Summing both directions into one
// "bytes saved" figure would report a deployment that virtualizes heavily as losing
// money on every expansion, which is arithmetically true and operationally useless. The
// projection therefore keeps the two directions apart, records each one's own saving
// (clamped at zero because a negative value is not a saving), and exposes the raw
// before/after totals per direction so an operator can see the inbound cost rather than
// having it silently folded away.

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/telemetry"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

// The fixture is deliberately built from content that MUST NOT reach an observable
// surface: a real root, the derived alias, a private tool name, a JSON Pointer, a
// call ID, and a workspace tag. Every content-freedom assertion below searches for
// these bytes.
const (
	hostileRoot    = "/home/dev/projects/aiproxer-example-project"
	hostileSuffix  = "pkg/aipapi/call.go"
	hostileAlias   = "/.__aip_v1__/w_hr7xnivm5at2ku356qya/"
	hostileTool    = "acme_internal_read_secrets"
	hostilePointer = "/payload/secret_path"
	hostileCallID  = "call_7f3a_2b19"
	hostileTarget  = hostileRoot + "/" + hostileSuffix
	hostileVirtual = hostileAlias + hostileSuffix
)

// hostileConfig is a fully configured operator subtree: enabled, rewriting, schema
// inference on, a declared mandatory bound, and a declared profile carrying a private
// tool name and a JSON Pointer. It is the configuration most likely to leak, because it
// is the one where every content-bearing input is actually present.
const hostileConfig = "enabled: true\nmode: rewrite\nschema_inference: true\n" +
	"mandatory_max_args_bytes: 65536\n" +
	"tool_profiles:\n  - names: [\"" + hostileTool + "\"]\n" +
	"    arg_json_pointers: [\"" + hostilePointer + "\"]\n"

// contentMarkers are the exact bytes a leak would carry. The list is spelled out rather
// than derived, because a marker list computed from the fixture would silently shrink
// if the fixture shrank.
var contentMarkers = []string{
	hostileRoot,
	hostileSuffix,
	hostileAlias,
	"/.__aip_v1__",
	"hr7xnivm5at2ku356qya",
	hostileTool,
	hostilePointer,
	hostileCallID,
	"/home/dev",
	"acme_internal",
}

func hostileResolve(t *testing.T, subtree string) config.Resolved {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(subtree), &doc); err != nil {
		t.Fatalf("parse configuration: %v", err)
	}
	resolved, err := config.Decode(doc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return resolved
}

// TestTheInventoryReportsEnablementModeAndBoundedShapeOnly is requirement 7.8 stated
// as a projection: the inventory names whether the feature is on, which mode it is in,
// and how big its configuration is, and it names nothing about what that configuration
// contains.
func TestTheInventoryReportsEnablementModeAndBoundedShapeOnly(t *testing.T) {
	t.Parallel()
	resolved := hostileResolve(t, hostileConfig)
	tel := telemetry.New(resolved.Shape())

	inventory := tel.Inventory()
	if !inventory.Enabled {
		t.Fatal("an enabled configuration reported itself disabled")
	}
	if inventory.Mode != rewrite.ModeRewrite.String() {
		t.Fatalf("mode = %q, want %q", inventory.Mode, rewrite.ModeRewrite.String())
	}
	if !inventory.SchemaInference {
		t.Fatal("an inference-enabled configuration reported inference off")
	}
	if inventory.MandatoryMaxArgsBytes != 65536 {
		t.Fatalf("declared bound = %d, want 65536", inventory.MandatoryMaxArgsBytes)
	}
	if inventory.ProfileCount != 1 {
		t.Fatalf("profile count = %d, want 1", inventory.ProfileCount)
	}
	if inventory.PathKeyCount <= 0 {
		t.Fatalf("path key count = %d, want the shipped vocabulary", inventory.PathKeyCount)
	}
	assertNoContent(t, "inventory", mustJSON(t, inventory))
}

// TestTheInventoryReportsADisabledGenerationAsDisabled is the other half of 7.1 seen
// from the observability side. A disabled generation constructs nothing, so the
// inventory must be answerable for it too - there is a diagnostics surface that says
// "off", which is what the no-flag-protocol argument needs in place of a flag.
func TestTheInventoryReportsADisabledGenerationAsDisabled(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{})
	inventory := tel.Inventory()
	if inventory.Enabled {
		t.Fatal("a zero configuration reported itself enabled")
	}
	if inventory.ProfileCount != 0 || inventory.PathKeyCount != 0 {
		t.Fatalf("a disabled generation reported counts: %+v", inventory)
	}
	assertNoContent(t, "disabled inventory", mustJSON(t, inventory))
}

// TestTheInventoryReportsTheModeAsABoundedLabel is the enum-rendering obligation at the
// inventory boundary, including the value no configuration can produce.
func TestTheInventoryReportsTheModeAsABoundedLabel(t *testing.T) {
	t.Parallel()
	for _, mode := range []rewrite.Mode{rewrite.ModeAudit, rewrite.ModeRewrite, rewrite.Mode(0xFF)} {
		tel := telemetry.New(config.Shape{Enabled: true, Mode: mode})
		if got := tel.Inventory().Mode; got != mode.String() {
			t.Fatalf("mode %v reported as %q, want the bounded label %q", mode, got, mode.String())
		}
		if got := tel.Inventory().Mode; !isBoundedLabel(got) {
			t.Fatalf("mode label %q is not a bounded token", got)
		}
	}
}

// TestNoObservationCarriesContentUnderHostileInput is the content-freedom obligation,
// driven with content that is present in every dimension at once: a real root, the
// alias it derives, a private tool name, a JSON Pointer, and a call ID. Each pass is
// driven to a DIFFERENT bounded outcome so the counters under test are not all zero -
// a projection that leaks nothing because it recorded nothing would pass this trivially.
func TestNoObservationCarriesContentUnderHostileInput(t *testing.T) {
	t.Parallel()
	resolved := hostileResolve(t, hostileConfig)
	tel := telemetry.New(resolved.Shape())

	driveOutbound(t, tel, resolved, rewrite.ModeRewrite)
	driveExpansion(t, tel, resolved, rewrite.ModeRewrite)

	snapshot := tel.Snapshot()
	if snapshot.Total.Reports == 0 {
		t.Fatal("the fixture recorded no reports; the content-freedom assertion would be vacuous")
	}
	if snapshot.Outbound.Virtualized.BytesSaved <= 0 {
		t.Fatalf("the fixture recorded no outbound saving: %+v", snapshot.Outbound)
	}
	assertNoContent(t, "snapshot", mustJSON(t, snapshot))
	assertNoContent(t, "inventory", mustJSON(t, tel.Inventory()))
}

// TestTheSnapshotPublishesPassOpportunitiesUnderTheirOwnWireName is the export contract
// the pass-opportunity rename exists to state (requirements.md 7.6, 9.5): the outbound
// figure is published as a sum of PASS OBSERVATIONS under a key that says so, and the
// misleading `bytes_saved` key is gone, because that name claimed realized candidate
// savings that a sum of pass observations cannot support.
//
// The assertion goes through the rendered form an operator receives, because the wire key
// is the deliverable: the Go field name is invisible outside the process, and an export
// that silently kept the old key would keep telling an audit deployment that its doubled
// opportunity sum is realized savings.
func TestTheSnapshotPublishesPassOpportunitiesUnderTheirOwnWireName(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 2, Rewritten: 2, BytesBefore: 100, BytesAfter: 40},
	})

	encoded := mustJSON(t, tel.Snapshot())
	// The decoded value is asserted rather than discarded: a field dropped from the
	// export would leave it at its zero value while the encoder still "worked".
	var decoded telemetry.Snapshot
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("snapshot does not round-trip through the standard decoders: %v (%s)", err, encoded)
	}
	if got := decoded.Total.PassObservedOpportunityBytes; got != 60 {
		t.Fatalf("round-tripped total pass opportunities = %d, want 60", got)
	}
	if got := decoded.Outbound.Virtualized.BytesSaved; got != 60 {
		t.Fatalf("round-tripped outbound pass opportunities = %d, want 60", got)
	}
	// The raw keys are checked as well, because the symmetric round-trip above cannot see
	// a RENAME: the same tag would read the value back under whichever name it published.
	var published struct {
		Total    map[string]json.RawMessage `json:"total"`
		Outbound struct {
			Virtualized map[string]json.RawMessage `json:"virtualized"`
		} `json:"outbound"`
	}
	if err := json.Unmarshal([]byte(encoded), &published); err != nil {
		t.Fatalf("snapshot is not readable as an object: %v (%s)", err, encoded)
	}
	for label, fields := range map[string]map[string]json.RawMessage{
		"total":                published.Total,
		"outbound.virtualized": published.Outbound.Virtualized,
	} {
		if _, exists := fields["pass_observed_opportunity_bytes"]; !exists {
			t.Fatalf("%s does not publish the pass opportunities under their own key: %s", label, encoded)
		}
		if _, exists := fields["bytes_saved"]; exists {
			t.Fatalf("%s still publishes %q, which names a saving a pass sum cannot support: %s",
				label, "bytes_saved", encoded)
		}
	}
	assertNoContent(t, "snapshot", encoded)
}

// TestSkippedOccurrencesAreCountedByBoundedReason is requirement 7.6's third counter.
// The tally must be keyed by the shared engine's closed reason vocabulary and must
// carry no per-surface identity of any kind.
func TestSkippedOccurrencesAreCountedByBoundedReason(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	// Two payloads that produce two DIFFERENT closed skip reasons: a payload that is not
	// an object, and one whose selected leaf is not a string.
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats: rewrite.Stats{
			Eligible: 1, Rewritten: 1, BytesBefore: 60, BytesAfter: 20,
			Skips: []rewrite.Skip{
				{Reason: rewrite.SkipReasonSelectorNotString, Count: 2},
				{Reason: rewrite.SkipReasonPayloadNotObject, Count: 1},
			},
		},
	})
	tel.ObserveOutbound(outbound.Report{Outcome: outbound.OutcomeRewriterRan})

	snapshot := tel.Snapshot()
	byReason := snapshot.Outbound.Virtualized.Skips
	if len(byReason) != 2 {
		t.Fatalf("recorded %d skip reasons, want the two that occurred: %+v", len(byReason), byReason)
	}
	// Occurred reasons are held in vocabulary order, which is what makes two identical
	// runs produce byte-identical output.
	if byReason[0].Reason != rewrite.SkipReasonPayloadNotObject.String() ||
		byReason[1].Reason != rewrite.SkipReasonSelectorNotString.String() {
		t.Fatalf("skip reasons are not in vocabulary order: %+v", byReason)
	}
	if byReason[0].Count != 1 || byReason[1].Count != 2 {
		t.Fatalf("skip counts = %+v, want 1 and 2", byReason)
	}
	if got := snapshot.Total.Skipped; got != 3 {
		t.Fatalf("total skipped = %d, want 3", got)
	}
	// A report with no skips must not create an entry, so the series stays bounded by
	// the vocabulary rather than by traffic.
	if got := len(snapshot.Outbound.Virtualized.Skips); got != 2 {
		t.Fatalf("an empty report grew the skip series to %d entries", got)
	}
	assertNoContent(t, "skip tally", mustJSON(t, snapshot))
}

// TestTheSkipSeriesIsBoundedByTheClosedVocabulary is the cardinality obligation for
// this one series: no amount of hostile traffic may widen it, and no label outside the
// shared engine's own String() may appear.
func TestTheSkipSeriesIsBoundedByTheClosedVocabulary(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	for i := range 64 {
		tel.ObserveOutbound(outbound.Report{
			Outcome: outbound.OutcomeRewriterRan,
			Stats: rewrite.Stats{
				Skips: []rewrite.Skip{
					{Reason: rewrite.SkipReasonSelectorUnresolved, Count: i + 1},
					// A reason outside the closed vocabulary, which the engine itself
					// documents as droppable. A projection must not index it either.
					{Reason: rewrite.SkipReason(0xFF), Count: 5},
				},
			},
		})
	}
	snapshot := tel.Snapshot()
	series := snapshot.Outbound.Virtualized.Skips
	if len(series) != 1 {
		t.Fatalf("the skip series holds %d entries, want only the one in-vocabulary reason: %+v",
			len(series), series)
	}
	if series[0].Reason != rewrite.SkipReasonSelectorUnresolved.String() {
		t.Fatalf("skip reason = %q, want %q", series[0].Reason, rewrite.SkipReasonSelectorUnresolved.String())
	}
	// 1 + 2 + ... + 64, so the counter is shown to aggregate rather than overwrite.
	if want := int64(64 * 65 / 2); series[0].Count != want {
		t.Fatalf("skip count = %d, want %d", series[0].Count, want)
	}
}

// assertOutcomesFoldToBoundedSlot proves that every value outside a closed outcome
// vocabulary lands in the recorder's OWN bounded slot, and that the last genuine
// member keeps counting only its own observations.
//
// wantUnknown is the number of out-of-vocabulary observations the loop actually fed,
// and wantGenuineLast is the number that wrapped back onto the last genuine member -
// both counted where the values are produced, because both vocabularies are uint8 and
// therefore wrap.
//
// A recorder that shared the bounded slot with the last genuine member would satisfy
// the bounded-series counts above while still publishing an unrecognised condition as
// a specific one. In the expansion direction that specific one is a client-facing
// refusal, so the mislabel would assert refusals that never happened.
func assertOutcomesFoldToBoundedSlot(
	tb testing.TB,
	name string,
	series []telemetry.Tally,
	genuineLast string,
	wantUnknown int64,
	wantGenuineLast int64,
) {
	tb.Helper()
	var gotUnknown, gotGenuineLast int64
	for _, tally := range series {
		switch tally.Reason {
		case "unknown":
			gotUnknown = tally.Count
		case genuineLast:
			gotGenuineLast = tally.Count
		}
	}
	if gotUnknown != wantUnknown {
		tb.Fatalf("%s: the bounded slot counted %d, want the %d out-of-vocabulary values; "+
			"they are being published as a genuine outcome instead: %+v",
			name, gotUnknown, wantUnknown, series)
	}
	if gotGenuineLast != wantGenuineLast {
		tb.Fatalf("%s: %q counted %d, want %d; the fold is stealing the last genuine "+
			"member's slot: %+v", name, genuineLast, gotGenuineLast, wantGenuineLast, series)
	}
}

// TestExpansionFailuresAreCountedSeparately is requirement 7.6's failure counter. A
// fail-closed refusal in the INBOUND direction is the one outcome here that costs a
// client its tool call, so it is counted on its own rather than folded into the noop
// tally an operator would otherwise read as routine.
func TestExpansionFailuresAreCountedSeparately(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeRejected, Reason: expansion.ReasonWorkspaceMismatch,
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeRejected, Reason: expansion.ReasonMalformedReservedAlias,
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeExpanded, Reason: expansion.ReasonExpanded,
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeNoop, Reason: expansion.ReasonNoAlias,
	})

	snapshot := tel.Snapshot()
	inbound := snapshot.Inbound
	if inbound.Reports != 4 {
		t.Fatalf("inbound reports = %d, want 4", inbound.Reports)
	}
	if inbound.Rejected != 2 {
		t.Fatalf("inbound rejections = %d, want 2", inbound.Rejected)
	}
	if inbound.Expanded != 1 {
		t.Fatalf("inbound expansions = %d, want 1", inbound.Expanded)
	}
	if inbound.Noop != 1 {
		t.Fatalf("inbound no-ops = %d, want 1", inbound.Noop)
	}
	// The refusal reason is a bounded label series of its own, which is what lets an
	// operator separate a stale workspace from a malformed alias without reading bytes.
	// Every decision carried a distinct reason, so the series must account for all four.
	if len(inbound.Reasons) != 4 {
		t.Fatalf("inbound reason series holds %d entries, want the four that occurred: %+v",
			len(inbound.Reasons), inbound.Reasons)
	}
	byLabel := map[string]int64{}
	for _, tally := range inbound.Reasons {
		byLabel[tally.Reason] = tally.Count
	}
	for _, want := range []string{
		expansion.ReasonWorkspaceMismatch.String(),
		expansion.ReasonMalformedReservedAlias.String(),
		expansion.ReasonExpanded.String(),
		expansion.ReasonNoAlias.String(),
	} {
		if byLabel[want] != 1 {
			t.Fatalf("reason %q counted %d times, want 1: %+v", want, byLabel[want], inbound.Reasons)
		}
	}
	assertNoContent(t, "inbound reasons", mustJSON(t, inbound.Reasons))
}

// TestMandatoryBufferOverflowsAreCounted is requirement 7.6's last counter, driven
// through the feature-side observation the finalizer actually makes. See
// expansion.Report.ArgsOverDeclaredBound for why this is the reachable half of the
// condition.
func TestMandatoryBufferOverflowsAreCounted(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeNoop, Reason: expansion.ReasonNoAlias,
		ArgsOverDeclaredBound: true,
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeNoop, Reason: expansion.ReasonNoAlias,
		ArgsOverDeclaredBound: true,
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeNoop, Reason: expansion.ReasonNoAlias,
	})

	snapshot := tel.Snapshot()
	if got := snapshot.Inbound.MandatoryOverflows; got != 2 {
		t.Fatalf("mandatory overflows = %d, want 2", got)
	}
	assertNoContent(t, "overflow counter", mustJSON(t, snapshot))
}

// TestRealizedSavingsAreReportedPerDirectionAndNeverNegative is requirement 9.5. Three
// things are pinned at once, and all three matter:
//
//  1. the OUTBOUND figure is bytes-before minus bytes-after over the decoded values,
//     which is a real saving because the alias is strictly shorter;
//  2. the INBOUND figure is NOT computed as a saving, because expanding an alias back
//     to a real root grows the value - the delta is negative and a negative delta is
//     not a saving;
//  3. neither figure is ever negative in the published projection, because a negative
//     "bytes saved" is the one number here that would be read as a fact about the
//     deployment and would be wrong.
func TestRealizedSavingsAreReportedPerDirectionAndNeverNegative(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 2, Rewritten: 2, BytesBefore: 100, BytesAfter: 40},
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeExpanded, Reason: expansion.ReasonExpanded,
		// The alias is 40 bytes; the real root it expands to is 100. The delta is -60.
		Stats: rewrite.Stats{Eligible: 2, Rewritten: 2, BytesBefore: 40, BytesAfter: 100},
	})

	snapshot := tel.Snapshot()
	if got, want := snapshot.Outbound.Virtualized.BytesSaved, int64(60); got != want {
		t.Fatalf("outbound saving = %d, want %d", got, want)
	}
	if got, want := snapshot.Outbound.Virtualized.BytesBefore, int64(100); got != want {
		t.Fatalf("outbound bytes-before = %d, want %d", got, want)
	}
	if got, want := snapshot.Outbound.Virtualized.BytesAfter, int64(40); got != want {
		t.Fatalf("outbound bytes-after = %d, want %d", got, want)
	}
	if got := snapshot.Inbound.Restore.BytesSaved; got != 0 {
		t.Fatalf("inbound saving = %d, want 0: expansion grows the value and a negative delta is not a saving", got)
	}
	// The inbound COST is still visible, because hiding it would make the projection
	// useless to an operator sizing a rollout.
	if got, want := snapshot.Inbound.BytesGrown(), int64(60); got != want {
		t.Fatalf("inbound growth = %d, want %d", got, want)
	}
	if got, want := snapshot.Inbound.Restore.BytesBefore, int64(40); got != want {
		t.Fatalf("inbound bytes-before = %d, want %d", got, want)
	}
	if got, want := snapshot.Inbound.Restore.BytesAfter, int64(100); got != want {
		t.Fatalf("inbound bytes-after = %d, want %d", got, want)
	}
	// And the grand total is the OUTBOUND saving only. Summing the inbound delta in
	// would report a heavily virtualizing deployment as losing bytes on every
	// expansion, which is arithmetically true and operationally useless.
	if got, want := snapshot.Total.PassObservedOpportunityBytes, int64(60); got != want {
		t.Fatalf("total saving = %d, want the outbound figure %d", got, want)
	}
	if snapshot.Total.PassObservedOpportunityBytes < 0 {
		t.Fatal("a published saving is negative")
	}
}

// TestANegativeOutboundDeltaIsClampedRatherThanReported pins the defensive half of the
// same rule. Requirement 9.1 guarantees the outbound replacement is strictly shorter, so
// a negative outbound delta cannot occur through the rewriter - and the projection must
// not DEPEND on that guarantee to stay truthful, because a future mutation path or a
// manually constructed report would otherwise publish a negative saving.
func TestANegativeOutboundDeltaIsClampedRatherThanReported(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 10, BytesAfter: 99},
	})
	snapshot := tel.Snapshot()
	if got := snapshot.Outbound.Virtualized.BytesSaved; got != 0 {
		t.Fatalf("a growing outbound delta was reported as a saving of %d", got)
	}
	if got, want := snapshot.Outbound.Virtualized.BytesAfter, int64(99); got != want {
		t.Fatalf("bytes-after = %d, want the raw total %d: clamping the SAVING must not hide the measurement", got, want)
	}
}

// TestOutboundTransformFailuresAreCounted is requirement 8.2's bounded reason made
// countable, and it is the one outbound outcome that is neither a rewrite nor a noop.
func TestOutboundTransformFailuresAreCounted(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	tel.ObserveOutbound(outbound.Report{Outcome: outbound.OutcomeTransformationFailed})
	tel.ObserveOutbound(outbound.Report{
		Outcome:    outbound.OutcomeProjectRootUnusable,
		RootReason: pathvirtualization.SkipReasonRelativeRoot,
	})
	tel.ObserveOutbound(outbound.Report{
		Outcome:    outbound.OutcomeProjectRootUnusable,
		RootReason: pathvirtualization.SkipReasonDeviceNamespace,
	})
	tel.ObserveOutbound(outbound.Report{Outcome: outbound.OutcomeWorkspaceUnresolved})
	tel.ObserveOutbound(outbound.Report{Outcome: outbound.OutcomeRewriterRan})

	snapshot := tel.Snapshot()
	out := snapshot.Outbound
	if out.Reports != 5 {
		t.Fatalf("outbound reports = %d, want 5", out.Reports)
	}
	if out.TransformFailed != 1 {
		t.Fatalf("transform failures = %d, want 1", out.TransformFailed)
	}
	if out.RootUnusable != 2 {
		t.Fatalf("root refusals = %d, want 2", out.RootUnusable)
	}
	if out.WorkspaceUnresolved != 1 {
		t.Fatalf("workspace refusals = %d, want 1", out.WorkspaceUnresolved)
	}
	if len(out.RootReasons) != 2 {
		t.Fatalf("root reason series holds %d entries, want 2: %+v", len(out.RootReasons), out.RootReasons)
	}
	// Vocabulary order, not first-seen order: relative_root precedes device_namespace in
	// the lexical core's own enumeration, and a series that reordered itself between reads
	// would be unusable as evidence.
	if out.RootReasons[0].Reason != pathvirtualization.SkipReasonRelativeRoot.String() ||
		out.RootReasons[1].Reason != pathvirtualization.SkipReasonDeviceNamespace.String() {
		t.Fatalf("root reasons are not in vocabulary order: %+v", out.RootReasons)
	}
	assertNoContent(t, "outbound counters", mustJSON(t, snapshot))
}

// TestAuditAndRewriteReportTheSameMeasurementAndOnlyRewriteMutates is requirement 7.3 at
// the projection boundary: audit reads the same bytes the mutation would and reports the
// rewrite's own numbers, while only rewrite publishes the alias. A measurement mode whose
// figures were an estimate would tell an operator nothing about switching to rewrite, and
// a measurement mode that mutated would be the mutation requirement 7.3 forbids.
func TestAuditAndRewriteReportTheSameMeasurementAndOnlyRewriteMutates(t *testing.T) {
	t.Parallel()
	resolved := hostileResolve(t, hostileConfig)

	measureAttempt := func(mode rewrite.Mode) (telemetry.Snapshot, *aipapi.Call) {
		t.Helper()
		tel := telemetry.New(resolved.Shape())
		transform := outbound.NewAttemptTransform(mode, resolved.Resolver,
			outbound.WithReporter(tel.ObserveOutbound))
		call := hostileCall()
		meta := request.AttemptMeta{
			Workspace: aipworkspace.WorkspaceView{ID: "ws_hostile", ProjectRoot: hostileRoot},
		}
		if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
			t.Fatalf("HandleAttempt in %v mode: %v", mode, err)
		}
		return tel.Snapshot(), call
	}

	auditSnapshot, auditCall := measureAttempt(rewrite.ModeAudit)
	rewriteSnapshot, rewriteCall := measureAttempt(rewrite.ModeRewrite)

	// The early pass measured the same candidate in both modes. The two rows are compared
	// as rendered values, so every figure requirement 7.6 asks for is covered.
	auditRow, ok := findPassRow(auditSnapshot.Outbound.ByPass, outbound.PassAttempt.String())
	if !ok {
		t.Fatalf("audit recorded no attempt row: %s", mustRenderPass(t, auditSnapshot.Outbound.ByPass))
	}
	rewriteRow, ok := findPassRow(rewriteSnapshot.Outbound.ByPass, outbound.PassAttempt.String())
	if !ok {
		t.Fatalf("rewrite recorded no attempt row: %s", mustRenderPass(t, rewriteSnapshot.Outbound.ByPass))
	}
	auditJSON := mustRenderPass(t, []telemetry.PassCounters{auditRow})
	rewriteJSON := mustRenderPass(t, []telemetry.PassCounters{rewriteRow})
	if auditJSON != rewriteJSON {
		t.Fatalf("audit measured a different candidate:\naudit=%s\nrewrite=%s", auditJSON, rewriteJSON)
	}
	// Non-vacuity: the equal rows really carry the hostile candidate's saving, so the
	// comparison above is two measurements and not two empty rows.
	if auditRow.BytesSaved <= 0 || auditRow.Eligible == 0 {
		t.Fatalf("the fixture measured nothing, so the equality would be vacuous: %+v", auditRow)
	}

	// Only rewrite published the alias. Audit must return the very bytes it was handed.
	if got, want := string(auditCall.Items[0].ToolCall.Arguments),
		string(hostileCall().Items[0].ToolCall.Arguments); got != want {
		t.Fatalf("audit mutated the candidate:\ngot  %s\nwant %s", got, want)
	}
	if got := string(rewriteCall.Items[0].ToolCall.Arguments); !strings.Contains(got, hostileAlias) {
		t.Fatalf("rewrite published no alias: %s", got)
	}
}

// TestTheSnapshotIsBoundedWhateverTheTraffic is the cardinality obligation stated
// globally rather than per series: every dimension is a closed vocabulary, so no input may
// produce more series entries than those vocabularies bound, and the total series COUNT is
// a constant of the design rather than a function of traffic.
//
// The traffic is deliberately adversarial on every axis at once. Ordinals outside each
// vocabulary, skip reasons outside the shared engine's, and root reasons carrying a project
// root all reach the recorder together, which is the only way to show that the bound holds
// against values this build does not define rather than merely against the ones it does.
func TestTheSnapshotIsBoundedWhateverTheTraffic(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	// Both vocabularies are uint8, so 100+i WRAPS: four of these 256 observations land
	// back inside the closed vocabulary. The expectations below are therefore counted as
	// the loop feeds rather than assumed, because a wrap that is not accounted for is
	// exactly the kind of genuine member this test exists to keep separate from the
	// bounded slot.
	var outboundUnknown, outboundLastGenuine int64
	var expansionUnknown, expansionLastGenuine int64
	for i := range 256 {
		if outbound.Outcome(100+i) > outbound.OutcomeWorkspaceUnresolved {
			outboundUnknown++
		}
		if outbound.Outcome(100+i) == outbound.OutcomeWorkspaceUnresolved {
			outboundLastGenuine++
		}
		if expansion.Outcome(100+i) > expansion.OutcomeRejected {
			expansionUnknown++
		}
		if expansion.Outcome(100+i) == expansion.OutcomeRejected {
			expansionLastGenuine++
		}
		tel.ObserveOutbound(outbound.Report{
			Outcome:    outbound.Outcome(100 + i),
			RootReason: pathvirtualization.SkipReason(hostileRoot + strconv.Itoa(i)),
			Stats: rewrite.Stats{
				Eligible: i, Rewritten: i, BytesBefore: i * 3, BytesAfter: i,
				Skips: []rewrite.Skip{
					{Reason: rewrite.SkipReason(i % 12), Count: 1},
					{Reason: rewrite.SkipReason(100 + i), Count: 1},
				},
			},
		})
		tel.ObserveExpansion(expansion.Report{
			Outcome:    expansion.Outcome(100 + i),
			Reason:     expansion.Reason(100 + i),
			RootReason: pathvirtualization.SkipReason(hostileAlias + strconv.Itoa(i)),
			// The three genuine codes are mixed in alongside the hostile ones so the
			// bound is shown to hold for both.
			Stats: rewrite.Stats{
				Eligible: i, Rewritten: i, BytesBefore: i, BytesAfter: i * 3,
				Skips: []rewrite.Skip{{Reason: rewrite.SkipReason(100 + i), Count: 1}},
			},
		})
	}
	snapshot := tel.Snapshot()
	// The series COUNT is fixed by the vocabularies: outbound outcomes, outbound root
	// reasons, the outbound skip series, expansion outcomes, expansion reasons, expansion
	// root reasons, and the inbound skip series.
	// The outcome bounds are the closed vocabulary plus the one bounded slot for a value
	// outside it. The extra entry is not slack in the bound: it is where the 256 hostile
	// outcomes above land, and it is asserted to carry the "unknown" label below rather
	// than any genuine outcome's.
	if got := len(snapshot.Outbound.Outcomes); got > 5 {
		t.Fatalf("outbound outcome series holds %d entries, want at most the 4 closed outcomes plus the one bounded slot: %+v",
			got, snapshot.Outbound.Outcomes)
	}
	if got := len(snapshot.Outbound.RootReasons); got > 6 {
		t.Fatalf("outbound root reason series holds %d entries, want at most the 6 closed codes: %+v",
			got, snapshot.Outbound.RootReasons)
	}
	if got := len(snapshot.Outbound.Virtualized.Skips); got > 11 {
		t.Fatalf("outbound skip series holds %d entries, want at most the 11 recordable closed reasons: %+v",
			got, snapshot.Outbound.Virtualized.Skips)
	}
	if got := len(snapshot.Inbound.Outcomes); got > 4 {
		t.Fatalf("expansion outcome series holds %d entries, want at most the 3 closed outcomes plus the one bounded slot: %+v",
			got, snapshot.Inbound.Outcomes)
	}
	if got := len(snapshot.Inbound.Reasons); got > 14 {
		t.Fatalf("expansion reason series holds %d entries, want at most the 13 recordable closed reasons plus the one bounded slot: %+v",
			got, snapshot.Inbound.Reasons)
	}
	if got := len(snapshot.Inbound.Restore.Skips); got > 11 {
		t.Fatalf("expansion skip series holds %d entries, want at most the 11 recordable closed reasons: %+v",
			got, snapshot.Inbound.Restore.Skips)
	}
	// A widened bound is only meaningful if the extra entry is the bounded one. Without
	// this the fold could land the 256 hostile outcomes in a genuine slot and still
	// satisfy the counts above - which is precisely the defect the spare slot removes,
	// and in the expansion direction it would publish refusals that never happened.
	assertOutcomesFoldToBoundedSlot(t, "outbound", snapshot.Outbound.Outcomes,
		"workspace_unresolved", outboundUnknown, outboundLastGenuine)
	assertOutcomesFoldToBoundedSlot(t, "expansion", snapshot.Inbound.Outcomes,
		"rejected", expansionUnknown, expansionLastGenuine)
	// Every one of the 256 expansion observations reached a REASON series entry, and the
	// series is still bounded by the closed vocabulary - which is the property the count
	// above states. The 243 in the final slot are the out-of-vocabulary values, all folded
	// into ONE bounded slot rather than 243 series, which is what the fold is for. The
	// remaining 13 observations carry the recordable reasons the feed cycles through, the
	// no-decision value being the one member the series deliberately omits.
	if got := snapshot.Inbound.Reasons[len(snapshot.Inbound.Reasons)-1].Count; got != 243 {
		t.Fatalf("the bounded slot counted %d, want the 243 out-of-vocabulary values: %+v",
			got, snapshot.Inbound.Reasons)
	}
	// And every entry is a genuine vocabulary member or the bounded fallback: none of them
	// is the 244 distinct values a naive map keyed on the raw reason would have produced.
	if got := len(snapshot.Inbound.Reasons); got > 14 {
		t.Fatalf("expansion reason series holds %d entries, want the closed vocabulary: %+v",
			got, snapshot.Inbound.Reasons)
	}
	// Every label in every series must be one the closed vocabularies publish, never a
	// value assembled from input.
	for _, tally := range allTallies(snapshot) {
		if !isBoundedLabel(tally.Reason) {
			t.Fatalf("series label %q is not a bounded token", tally.Reason)
		}
		if tally.Count <= 0 {
			t.Fatalf("series %q has a non-positive count %d", tally.Reason, tally.Count)
		}
	}
	assertNoContent(t, "hostile snapshot", mustJSON(t, snapshot))
}

// allTallies flattens every series a snapshot publishes, so the bounded-label check runs
// over all of them rather than over the one the test happened to look at.
func allTallies(s telemetry.Snapshot) []telemetry.Tally {
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

// TestTheTelemetryIsSafeUnderConcurrentReporters is the concurrency contract the three
// reporters document. One instance is shared by every retry, race participant, and
// failover candidate of a logical turn, so an implementation that keeps counters must
// be safe for concurrent use - and this is the test that makes the claim checkable.
func TestTheTelemetryIsSafeUnderConcurrentReporters(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	const writers, perWriter = 8, 64
	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for range perWriter {
				tel.ObserveOutbound(outbound.Report{
					Outcome: outbound.OutcomeRewriterRan,
					Stats: rewrite.Stats{
						Eligible: 1, Rewritten: 1, BytesBefore: 100, BytesAfter: 40,
						Skips: []rewrite.Skip{{Reason: rewrite.SkipReasonSelectorUnresolved, Count: 1}},
					},
				})
				tel.ObserveExpansion(expansion.Report{
					Outcome: expansion.OutcomeExpanded, Reason: expansion.ReasonExpanded,
					Stats: rewrite.Stats{
						Eligible: 1, Rewritten: 1, BytesBefore: 40, BytesAfter: 100,
					},
				})
				_ = writer
			}
		}(writer)
	}
	wg.Wait()

	snapshot := tel.Snapshot()
	if got, want := snapshot.Outbound.Reports, int64(writers*perWriter); got != want {
		t.Fatalf("outbound reports = %d, want %d: a lost update under concurrency", got, want)
	}
	if got, want := snapshot.Inbound.Reports, int64(writers*perWriter); got != want {
		t.Fatalf("inbound reports = %d, want %d: a lost update under concurrency", got, want)
	}
	if got, want := snapshot.Outbound.Virtualized.BytesSaved, int64(writers*perWriter*60); got != want {
		t.Fatalf("outbound saving = %d, want %d: a lost update under concurrency", got, want)
	}
	// Repeated reads of an unchanged recorder must be identical, which is what makes the
	// -count=5 determinism obligation hold.
	first := mustJSON(t, snapshot)
	for i := range 4 {
		if got := mustJSON(t, tel.Snapshot()); got != first {
			t.Fatalf("snapshot %d differs from the first: %s != %s", i, got, first)
		}
	}
}

// TestIdenticalTrafficProducesIdenticalSnapshots is the determinism obligation: two
// recorders fed the SAME reports in the same order must publish byte-identical JSON,
// because a metrics export that reorders its own series between reads is unusable as
// evidence.
func TestIdenticalTrafficProducesIdenticalSnapshots(t *testing.T) {
	t.Parallel()
	feed := func() string {
		tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
		for i := range 16 {
			tel.ObserveOutbound(outbound.Report{
				Outcome:    outbound.OutcomeProjectRootUnusable,
				RootReason: mixedRootReason(i),
				Stats: rewrite.Stats{
					Eligible: i, BytesBefore: 100 * i, BytesAfter: 10 * i,
					Skips: []rewrite.Skip{{Reason: rewrite.SkipReason(i % 12), Count: i}},
				},
			})
			tel.ObserveExpansion(expansion.Report{
				Outcome: expansion.OutcomeRejected, Reason: expansion.Reason(i % 13),
				Stats: rewrite.Stats{BytesBefore: 10 * i, BytesAfter: 100 * i},
			})
		}
		return mustJSON(t, tel.Snapshot())
	}
	first := feed()
	for i := range 5 {
		if got := feed(); got != first {
			t.Fatalf("run %d produced a different snapshot:\nfirst=%s\ngot=%s", i, first, got)
		}
	}
}

// TestNothingIsRecordedWithoutAnExplicitObservation is the negative-space check for
// the recorder's whole design: it has no ambient hook, no package state, and no way to
// learn anything about a request it was not handed. Building one and reading it back
// must therefore yield the zero snapshot.
func TestNothingIsRecordedWithoutAnExplicitObservation(t *testing.T) {
	t.Parallel()
	tel := telemetry.New(config.Shape{Enabled: true, Mode: rewrite.ModeRewrite})
	snapshot := tel.Snapshot()
	if snapshot.Total.Reports != 0 || snapshot.Outbound.Reports != 0 || snapshot.Inbound.Reports != 0 {
		t.Fatalf("a fresh recorder reported observations it was never handed: %s", mustJSON(t, snapshot))
	}
	if snapshot.Total.PassObservedOpportunityBytes != 0 || snapshot.Total.Eligible != 0 {
		t.Fatalf("a fresh recorder reported counters: %s", mustJSON(t, snapshot))
	}
	// The zero snapshot must still be a complete, renderable value, because a metrics
	// exporter reads it whether or not anything has happened yet.
	assertNoContent(t, "zero snapshot", mustJSON(t, snapshot))
}

// TestANilRecorderIsInertAndReadsAsAnEmptyGeneration is decision 1 of doc.go seen from
// the caller's side: observability is opt-in, so a deployment that never constructs a
// recorder can hold a nil one without branching at every call site. Every entry point is
// total on a nil receiver, and the read side answers with the zero projection rather than
// a panic.
func TestANilRecorderIsInertAndReadsAsAnEmptyGeneration(t *testing.T) {
	t.Parallel()
	var tel *telemetry.Telemetry
	// Reporting through a nil recorder is the shape a caller that opted out actually has
	// when it installs a reporter unconditionally.
	tel.ObserveOutbound(outbound.Report{
		Outcome: outbound.OutcomeRewriterRan,
		Stats:   rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 10, BytesAfter: 5},
	})
	tel.ObserveExpansion(expansion.Report{
		Outcome: expansion.OutcomeExpanded, Reason: expansion.ReasonExpanded,
		Stats: rewrite.Stats{Eligible: 1, Rewritten: 1, BytesBefore: 5, BytesAfter: 10},
	})

	if got := tel.Inventory(); got.Enabled || got.Mode != "" || got.ProfileCount != 0 || got.PathKeyCount != 0 {
		t.Fatalf("a nil recorder published an inventory it never had: %+v", got)
	}
	snapshot := tel.Snapshot()
	if snapshot.Total.Reports != 0 || snapshot.Outbound.Reports != 0 || snapshot.Inbound.Reports != 0 ||
		snapshot.Total.PassObservedOpportunityBytes != 0 || len(snapshot.Outbound.ByPass) != 0 {
		t.Fatalf("a nil recorder published counters it never recorded: %s", mustJSON(t, snapshot))
	}
}

// TestTheInventoryIsCapturedAtConstructionAndIgnoresLaterTrafficAndConfiguration is the
// boundary rule that keeps 7.8 honest. The inventory is built from the compiled shape
// ONCE, at construction, and never re-read, because a diagnostics surface whose answer
// depends on when it was asked - or on how much traffic passed - is a fact about the read
// rather than a fact about the generation.
//
// The comparison goes through the RENDERED form rather than through struct equality,
// because that is the form a diagnostics reader actually receives: two inventories whose
// JSON differs are two different published surfaces regardless of whether Go considers the
// values equal. It also keeps this test working if a future field is a slice, which is
// precisely the kind of field that would make a struct comparison in a test quietly
// non-compiling rather than quietly passing.
func TestTheInventoryIsCapturedAtConstructionAndIgnoresLaterTrafficAndConfiguration(t *testing.T) {
	t.Parallel()
	resolved := hostileResolve(t, hostileConfig)
	tel := telemetry.New(resolved.Shape())
	first := mustJSON(t, tel.Inventory())

	// Traffic must not reach the inventory: being configured and being recorded are two
	// different facts, and a diagnostics surface that conflated them would change its
	// answer as the deployment was used.
	driveOutbound(t, tel, resolved, rewrite.ModeRewrite)
	driveExpansion(t, tel, resolved, rewrite.ModeRewrite)
	if snapshot := tel.Snapshot(); snapshot.Total.Reports == 0 {
		t.Fatal("the fixture recorded no traffic, so the immutability check would be vacuous")
	}
	if got := mustJSON(t, tel.Inventory()); got != first {
		t.Fatalf("recording traffic changed the inventory: %s != %s", got, first)
	}

	for i := range 8 {
		if got := mustJSON(t, tel.Inventory()); got != first {
			t.Fatalf("inventory read %d differs: %s != %s", i, got, first)
		}
	}
	// Mutating the caller's own shape after construction must not reach the recorder.
	shape := resolved.Shape()
	tel = telemetry.New(shape)
	shape.ProfileCount = 9999
	shape.Mode = rewrite.ModeAudit
	shape.Enabled = false
	if got := tel.Inventory().ProfileCount; got == 9999 {
		t.Fatal("the recorder retained the caller's shape by reference")
	}
	if !tel.Inventory().Enabled {
		t.Fatal("mutating the caller's shape changed the recorder's enablement")
	}
}

func assertNoContent(t *testing.T, label, rendered string) {
	t.Helper()
	for _, marker := range contentMarkers {
		if strings.Contains(rendered, marker) {
			t.Fatalf("the %s carries content %q (requirements.md 7.7): %s", label, marker, rendered)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// isBoundedLabel reports whether a rendered dimension is a single fixed token: no
// separator, no path-ish byte, and no unbounded run of text. It is the shape every label
// this projection publishes has, and it is deliberately crude so that it cannot be
// satisfied by a value that merely happens to be absent from the marker list.
func isBoundedLabel(label string) bool {
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

// mixedRootReason alternates a genuine refusal code with a value carrying a real root, so
// the determinism fixture exercises both the in-vocabulary and out-of-vocabulary paths in a
// fixed, reproducible order.
func mixedRootReason(i int) pathvirtualization.SkipReason {
	if i%2 == 0 {
		return pathvirtualization.SkipReasonEmptyRoot + pathvirtualization.SkipReason(strconv.Itoa(i))
	}
	return pathvirtualization.SkipReason(hostileRoot + strconv.Itoa(i))
}

// driveOutbound runs both outbound passes over a hostile path-bearing call so the
// fixture produces a non-zero measurement rather than an empty one.
func driveOutbound(t *testing.T, tel *telemetry.Telemetry, resolved config.Resolved, mode rewrite.Mode) {
	t.Helper()
	transform := outbound.NewAttemptTransform(mode, resolved.Resolver, outbound.WithReporter(tel.ObserveOutbound))
	hook := outbound.NewRequestPartHook(mode, resolved.Resolver, outbound.WithHookReporter(tel.ObserveOutbound))
	call := hostileCall()
	meta := request.AttemptMeta{
		Workspace: aipworkspace.WorkspaceView{ID: "ws_hostile", ProjectRoot: hostileRoot},
	}
	if _, err := transform.HandleAttempt(t.Context(), call, meta, request.Services{}); err != nil {
		t.Fatalf("HandleAttempt: %v", err)
	}
	ctx := aipworkspace.WithWorkspaceView(t.Context(), meta.Workspace)
	late := hostileCall()
	if err := hook.HandleRequestParts(ctx, late, sdkhooks.PartMeta{}); err != nil {
		t.Fatalf("HandleRequestParts: %v", err)
	}
}

// driveExpansion runs the expansion finalizer over a hostile completed call, once
// expanding a real-root alias-bearing value and once refusing a stale-workspace alias,
// so the fixture exercises both a publication and a fail-closed refusal.
func driveExpansion(t *testing.T, tel *telemetry.Telemetry, resolved config.Resolved, mode rewrite.Mode) {
	t.Helper()
	fin, err := expansion.NewFinalizer(resolved.Resolver, mode, resolved.ExpansionPolicy(),
		expansion.WithReporter(tel.ObserveExpansion))
	if err != nil {
		t.Fatalf("NewFinalizer: %v", err)
	}
	tool := aipapi.ToolDef{
		Name:       hostileTool,
		Parameters: []byte(`{"type":"object","properties":{"payload":{"type":"object","properties":{"secret_path":{"type":"string"}}}}}`),
	}
	if _, err := fin.Finalize(t.Context(), toolcall.CompletedCall{
		ToolCallID: hostileCallID, ToolName: hostileTool,
		ArgsJSON: json.RawMessage(`{"payload":{"secret_path":"` + hostileVirtual + `"}}`),
	}, tool, nil, toolcall.Meta{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	// A stale alias of a DIFFERENT tag is the fail-closed half, and it must reach the
	// projection as a bounded reason rather than as the alias bytes.
	if _, err := fin.Finalize(t.Context(), toolcall.CompletedCall{
		ToolCallID: hostileCallID, ToolName: hostileTool,
		ArgsJSON: json.RawMessage(`{"payload":{"secret_path":"/.__aip_v1__/w_aaaaaaaaaaaaaaaaaaaa/x"}}`),
	}, tool, nil, toolcall.Meta{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

// hostileCall is one path-bearing completed tool call whose every content-bearing field
// is drawn from the marker list.
func hostileCall() *aipapi.Call {
	return &aipapi.Call{
		ID: hostileCallID,
		Tools: []aipapi.ToolDef{{
			Name:       hostileTool,
			Parameters: []byte(`{"type":"object","properties":{"secret_path":{"type":"string"}}}`),
		}},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindToolCall, ID: hostileCallID, Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID: hostileCallID,
				Name:   hostileTool,
				// The shape matches the declared profile's /payload/secret_path pointer, so
				// the pass really does select and rewrite this leaf rather than skipping it.
				// A fixture whose pointer did not resolve would record nothing and make the
				// content-freedom assertion vacuous.
				Arguments: json.RawMessage(`{"payload":{"secret_path":"` + hostileTarget + `"}}`),
			},
		}},
	}
}
