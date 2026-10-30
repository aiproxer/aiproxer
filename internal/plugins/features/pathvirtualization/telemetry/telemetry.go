package telemetry

// This file is the recorder: the one mutable value in the package, and the only place
// a report's bounded content becomes a number.
//
// Two properties are structural rather than checked, and both are load-bearing:
//
//   - IT IS A CONSTRUCTED VALUE. There is no package state, no init() registration, and
//     no ambient hook, so a deployment that wants counters constructs a recorder and a
//     deployment that does not never has one. That is what makes "the feature's
//     observability is explicit" true rather than aspirational, and it is why the
//     composition seam is the only thing that can decide to observe at all.
//
//   - IT ACCUMULATES, IT NEVER OBSERVES. Nothing in this file reads a request, a call,
//     a payload, or a workspace. Both entry points take a report a component had already
//     decided to emit, which is what makes the recorder incapable of changing any
//     decision and safe to add to a safety-relevant feature.
//
// The concurrency contract is stated in the type's own doc because it is load-bearing:
// ONE recorder is shared by every retry, race participant, and failover candidate of a
// logical turn, since the bundle hands the same instance to all three of its components
// and the runtime holds those components for the whole generation. The mutex is the
// whole mechanism - the counters are otherwise plain integers, and every series is a
// fixed-size array indexed by a closed vocabulary, so recording allocates nothing and
// its memory does not grow with traffic.

import (
	"sync"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/outbound"
)

// Telemetry accumulates the feature's bounded, content-free counters for one bundle.
//
// It is safe for concurrent use: a reporter may be invoked from any candidate, retry,
// or race participant of a logical turn, and one recorder is shared by all of them.
type Telemetry struct {
	// inventory is the compiled configuration SHAPE, copied at construction.
	//
	// It is a copy rather than a retained config.Shape because the inventory must be
	// immutable: a diagnostics surface whose answer depends on when it was asked is not
	// a fact about the generation, it is a fact about the read. See [Telemetry.Inventory].
	inventory Inventory

	mu    sync.Mutex
	out   outboundCounters
	in    inboundCounters
	total totalCounters
}

// New builds a recorder for one compiled configuration.
//
// It takes the compiled shape rather than the configuration, which is the whole point:
// config.Resolved carries no operator text at all, so every dimension this recorder can
// ever publish is already bounded before it arrives. A caller that passed the raw
// configuration instead would be handing this package a tool name and a JSON Pointer, and
// the type system would not stop it.
//
// A disabled shape produces a working recorder with no counters to read, rather than a
// nil one. That is deliberate and it is what makes requirement 7.1's "disabled by
// default" observable: a generation with the feature off still answers "is it on?" with
// "no", which is the fact a diagnostics inventory exists to report and exactly what a
// process flag could not express.
func New(shape config.Shape) *Telemetry {
	return &Telemetry{inventory: newInventory(shape)}
}

// Inventory returns the bounded diagnostics projection of the compiled configuration.
//
// It is derived from state captured at construction and never re-read, so repeated calls
// return the same value no matter how much traffic has passed since. See [Inventory] for
// what it deliberately does not contain.
func (t *Telemetry) Inventory() Inventory {
	if t == nil {
		return Inventory{}
	}
	return t.inventory
}

// ObserveOutbound records one outbound pass's report.
//
// It satisfies outbound.Reporter, so it can be installed directly as that pass's reporter
// option rather than wrapped.
func (t *Telemetry) ObserveOutbound(report outbound.Report) {
	if t == nil {
		return
	}
	// The statistics value is read into a local so every field the fold touches is read
	// from the caller's report exactly once. It is a value, so there is no aliasing
	// concern; the local just makes the fold read as one input rather than as repeated
	// field selections, which keeps the accounting order obvious.
	stats := report.Stats

	t.mu.Lock()
	defer t.mu.Unlock()
	t.out.reports++
	t.out.recordPass(report.Pass, stats)
	t.total.reports++
	t.total.eligible += int64(stats.Eligible)
	t.total.rewritten += int64(stats.Rewritten)
	t.total.skipped += t.out.virtualized.record(stats)
	t.total.recordOutboundBytes(stats)
	t.out.recordOutcome(report.Outcome)
	if reason := report.RootReason; reason != pathvirtualization.SkipReasonNone {
		t.out.recordRootReason(reason)
	}
}

// ObserveExpansion records one expansion decision's report.
//
// It satisfies expansion.Reporter, so it can be installed directly as that pass's
// reporter option rather than wrapped.
func (t *Telemetry) ObserveExpansion(report expansion.Report) {
	if t == nil {
		return
	}
	stats := report.Stats

	t.mu.Lock()
	defer t.mu.Unlock()
	t.in.reports++
	t.total.reports++
	t.total.eligible += int64(stats.Eligible)
	t.total.rewritten += int64(stats.Rewritten)
	// Inbound growth is separate from outbound pass opportunities.
	t.total.skipped += t.in.restore.record(stats)
	t.in.recordOutcome(report.Outcome, report.Reason)
	if report.ArgsOverDeclaredBound {
		t.in.mandatoryOverflows++
	}
	if reason := report.RootReason; reason != pathvirtualization.SkipReasonNone {
		t.in.recordRootReason(reason)
	}
}

// Snapshot returns the current counters as a bounded, content-free value.
//
// The value is a copy: it shares no state with the recorder, so a reader can hold it
// while traffic continues and every field of it is a snapshot of one instant. Two
// recorders fed the same reports publish byte-identical snapshots, which is what makes
// the projection usable as evidence rather than as a stream of moving numbers.
func (t *Telemetry) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return Snapshot{
		Inventory: t.inventory,
		Outbound:  t.out.snapshot(),
		Inbound:   t.in.snapshot(),
		Total:     t.total.snapshot(),
	}
}
