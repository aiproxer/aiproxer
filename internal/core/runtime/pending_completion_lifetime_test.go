// Publication-lifetime regressions for the pending completion-result publication
// (spec: .kiro/specs/agent-loop-explicit-completion-protocol, design Completion
// Evidence and Pending Result / Concurrency and Lifecycle; requirements 6.3-6.7,
// 11.3, 11.5, 12.5).
//
// Every case here pins one seam of the publication's whole lifetime: reservation,
// staging, activation, physical delivery, and withdrawal. The private value is
// owned for that entire window, so a candidate, its frozen origin, and its usage
// cursor must survive each transition and must disappear at exactly one of them.
//
// The seam-level cases drive the real private drain entry and the real staging
// callback with real channel barriers. Nothing sleeps to coordinate a race, and no
// case installs a fake closed state: the fences under test are the live caller,
// the shared A-leg, the frozen origin, and the publication window.
package runtime

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// errPendingLifetimeBoom is the bounded stand-in failure these cases inject.
var errPendingLifetimeBoom = errors.New("pending lifetime boom")

// requirePendingPublicationCloseWon asserts that a Close which withdrew the
// publication before the private stage FAILED the terminal with a close-won
// cause, instead of reporting a successful turn for a batch that will never be
// released.
//
// Only the sites that read the error out of the RECV loop use this. A real
// Close both withdraws the publication window and ends the shared A-leg, so
// whichever of the two the receive loop observes first decides the surfaced
// cause: the publication fence reports errPendingPublicationWithdrawn, while the
// A-leg scope reports leglifecycle.ErrALegCanceled. The receive loop consults
// the A-leg scope on paths that return before the publication fence is
// evaluated, so under load the cancellation is legitimately observed first.
// Pinning a single one of the two made the expectation scheduler-dependent
// without strengthening it: both causes are the same terminal outcome.
//
// This loosens the EXPECTED cause, never the invariant. The failure modes these
// cases exist to catch stay fatal, namely a nil error and any unrelated cause.
// The load-bearing assertions that follow remain unchanged and are what actually
// make a leak observable: no NORMAL-COMPLETED billing handoff, no deliverable
// publication, no queued event, no retained candidate, and exactly one
// conservative observer finish.
//
// Sites that read the withdrawal straight off a fence (a stagePendingCompletion
// or accepted-normal claim, or the staging callback) must keep pinning
// errPendingPublicationWithdrawn exactly: those paths never consult the A-leg
// scope, so a second cause is not reachable there.
func requirePendingPublicationCloseWon(t *testing.T, err error, msg string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("%s: a withdrawn publication must fail the terminal, got success", msg)
	case errors.Is(err, errPendingPublicationWithdrawn):
		return
	case errors.Is(err, leglifecycle.ErrALegCanceled):
		return
	default:
		t.Fatalf("%s: got %v, want a close-won cause (%v or %v)", msg, err,
			errPendingPublicationWithdrawn, leglifecycle.ErrALegCanceled)
	}
}

// pendingLifetimeBarrierWait bounds the one wait that is expected to be
// instantaneous: the deferred observer finish a winning Close must perform while
// the publication preflight is blocked. It only turns a missing barrier into a
// bounded failure; every ordering assertion is still made from a channel barrier.
const pendingLifetimeBarrierWait = 5 * time.Second

// pendingLifetimeOtherAttempt is a live attempt that owns no private publication,
// so every "wrong origin" case can hand it to a seam that must refuse it.
func pendingLifetimeOtherAttempt(t *testing.T) *attemptSession {
	t.Helper()
	return newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "aleg-pending-1", BLegID: "bleg-pending-2", Seq: 2},
		cand: controlCandidate("control-b"),
	})
}

// pendingLifetimeState reads the private publication state the way production
// reads it, so a case can assert on a staged, reserved, or deferred reservation
// instead of on one narrow accessor.
func pendingLifetimeState(p *responsePipeline) (staged, reserved, deferred, activated bool) {
	if p == nil {
		return false, false, false, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return len(p.pending.staged) > 0, p.pending.reserved, p.pending.deferredObs, p.pending.activated
}

// pendingLifetimeOrigin returns the origin of the currently retained private
// candidate, which is the identity every later seam revalidates.
func pendingLifetimeOrigin(p *responsePipeline) *attemptSession {
	prepared := p.pendingPreparedSnapshot()
	if prepared == nil {
		return nil
	}
	return prepared.origin
}

// pendingLifetimeDirectQueue installs the private batch for the REAL pipeline and
// attempt of a real receive stream, with no backend frame queued before it.
//
// The queue is installed directly instead of through an accepted terminal, so a
// case that needs one private publication delivered by the REAL Recv can attribute
// every client-event effect to that physical delivery alone. Nothing about the
// candidate is hand-built: the real preparation supplies it.
func pendingLifetimeDirectQueue(t *testing.T, fixture *pendingRealStream) ([]aipapi.Event, *pendingCompletion) {
	t.Helper()
	ctx := context.Background()
	prep, err := fixture.pipe.preparePendingCompletion(ctx, fixture.stream.facts, fixture.attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the real preparation must really own a candidate")
	claimPublication(t, fixture.pipe, prep.prepared)
	reservation, ok := fixture.pipe.reservePendingPublication(prep.prepared, true)
	require.True(t, ok, "the claimed candidate must be reservable")
	// The batch production itself builds for this candidate: its own held events,
	// no previewed customer usage, then its accepted finish.
	batch := make([]aipapi.Event, 0, len(prep.prepared.events)+1)
	batch = append(batch, prep.prepared.events...)
	batch = append(batch, prep.prepared.finish)
	require.True(t, fixture.pipe.stageReservedPublication(reservation, fixture.attempt, batch,
		aipapi.Event{}, pendingCustomerAbsent),
		"the live reservation must install the private batch")
	require.True(t, fixture.pipe.activateReservedPendingPublication(prep.prepared, fixture.attempt),
		"an activated publication becomes deliverable")
	return batch, prep.prepared
}

// pendingLifetimeLabels labels one canonical sequence, optionally dropping a kind,
// so a case can compare a delivered batch against its expected shape.
func pendingLifetimeLabels(events []aipapi.Event, drop aipapi.EventKind) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		if drop != "" && ev.Kind == drop {
			continue
		}
		switch ev.Kind {
		case aipapi.EventTextDelta:
			out = append(out, "text:"+ev.Delta)
		case aipapi.EventUsageDelta:
			out = append(out, "usage")
		default:
			out = append(out, string(ev.Kind))
		}
	}
	return out
}

// TestPendingLifetime_reservationRetainsTheFrozenCandidateAndOrigin pins the
// ownership half of the reservation: once the accepted terminal claims the one
// publication, the pipeline keeps the very candidate and its frozen origin, so
// every later seam can revalidate the same attempt instead of guessing one.
//
// It also pins that a repeated finish route cannot re-prepare over a live
// reservation: preparation is idempotent, so the claim stays consumed.
func TestPendingLifetime_reservationRetainsTheFrozenCandidateAndOrigin(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "retained answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	claimed, ok := p.takePendingPublication()
	require.True(t, ok, "the first claim must reserve the one publication")
	require.Same(t, prep.prepared, claimed)

	assert.Same(t, prep.prepared, p.pendingPreparedSnapshot(),
		"a reservation retains the frozen candidate for the whole drain")
	assert.Same(t, attempt, pendingLifetimeOrigin(p),
		"a reservation retains the frozen origin, never the live slot")
	require.NotNil(t, pendingLifetimeOrigin(p))
	assert.Equal(t, attempt.bleg.BLegID, p.pendingPreparedSnapshot().text.blegID,
		"the retained candidate must keep the B-leg identity it was copied under")

	// A repeated finish route converges on the same prepared value instead of
	// preparing a second candidate over the live reservation.
	again, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.Same(t, prep.prepared, again.prepared,
		"a repeated finish route must read the same prepared value")

	_, claimedTwice := p.takePendingPublication()
	assert.False(t, claimedTwice,
		"a repeated terminal call must find nothing to publish while the reservation is live")
}

// TestPendingLifetime_wrongAttemptCannotDrainTheReservedPublication pins the live
// slot half of the physical delivery fence through the REAL receive entry.
//
// The first Recv ALWAYS selects the retained ORIGINAL origin, never the live slot,
// so this case builds the schedule that actually replaces that origin: a complete
// replacement attempt with its own passive backend is published into the live slot
// while the response still retains the ORIGINAL candidate. That is a real
// replacement fence, so the delivery must conservatively withdraw the candidate the
// retained origin owns, release none of it, and close its deferred observer exactly
// once. Foreign-caller protection on its own is pinned at the discard, staging, and
// activation seams, where no replacement has been published.
func TestPendingLifetime_wrongAttemptCannotDrainTheReservedPublication(t *testing.T) {
	t.Parallel()

	fixture := newPendingRealStream(t, pendingRealOptions{observer: true})
	pipe := fixture.pipe
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")

	batch, prepared := pendingLifetimeDirectQueue(t, fixture)
	require.Same(t, fixture.attempt, pipe.pendingPublicationOriginSnapshot(),
		"the retained candidate must still own its original attempt")

	// A complete replacement attempt is published into the live slot with its own
	// passive backend, so the receive loop has benign input to continue on.
	replacement := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: fixture.stream.facts.aLegID, BLegID: "bleg-pending-replacement", Seq: 2},
		cand: controlCandidate("control-b"),
	})
	replacement.storeInner(&pendingRealEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "benign replacement output"},
	}})
	testInstallSlot(&fixture.stream.attempt, replacement)

	ev, recvErr := fixture.stream.Recv(context.Background())
	require.NoError(t, recvErr, "a replaced attempt must not fail the receive; err=%v", recvErr)
	assert.NotEqual(t, batch[0].Kind, ev.Kind,
		"a replaced attempt must never release the retained candidate's batch")

	// Zero private releases: nothing from the withdrawn candidate reaches the
	// client, including the accepted finish and the result text.
	_, queued := pipe.pendingPublicationHead()
	assert.False(t, queued, "a replacement must leave no queued private event")
	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"a replacement must dispose the retained candidate instead of stranding it")
	assert.Zero(t, pendingRealCountText([]aipapi.Event{ev}, prepared.text.result),
		"a replacement must never release the retained result text")

	// Conservative, once-only cleanup of the withdrawn candidate's observer.
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn candidate must close its deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn candidate must finish the observer conservatively, never successfully")
}

// TestPendingLifetime_crossOriginDiscardDoesNotEraseAnotherLiveCandidate pins the
// ownership rule of the discard seam: a caller that owns no private publication
// must neither be told it closed a deferred observer nor clear state that belongs
// to another live candidate.
func TestPendingLifetime_crossOriginDiscardDoesNotEraseAnotherLiveCandidate(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "owned answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	other := pendingLifetimeOtherAttempt(t)
	assert.False(t, p.discardPendingPublication(other),
		"a foreign origin must never be reported as the owner of this candidate")
	assert.Same(t, prep.prepared, p.pendingPreparedSnapshot(),
		"a cross-origin discard must not erase another live candidate")
	assert.True(t, p.pendingPublicationAccepted(), "the real reservation must survive a cross-origin discard")

	// The real owner's own withdrawal does clear it: a merely prepared candidate is
	// state the abandoning owner must be able to dispose, because preparation alone
	// released nothing but a later abandoning terminal has no other owner for it.
	assert.True(t, p.discardPendingPublication(attempt),
		"the real owner must be able to dispose its own candidate")
	assert.Nil(t, p.pendingPreparedSnapshot(), "the real owner's own discard must clear the state")
	assert.False(t, p.pendingPublicationAccepted(), "the real reservation must be released with it")
}

// TestPendingLifetime_candidateSurvivesStagingAndClearsAtThePhysicalFinish pins
// the whole owned window: the candidate and its reservation survive the REAL
// staging callback and activation, and they are disposed only when the accepted
// finish is physically delivered through the REAL receive entry.
func TestPendingLifetime_candidateSurvivesStagingAndClearsAtThePhysicalFinish(t *testing.T) {
	t.Parallel()

	// Real owners, real observer, and a passive inner backend with no queued
	// frame, so the only events this receive can release come from the private
	// publication itself.
	fixture := newPendingRealStream(t, pendingRealOptions{observer: true})
	pipe := fixture.pipe
	attempt := fixture.attempt
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")
	ctx := context.Background()

	prep, err := pipe.preparePendingCompletion(ctx, fixture.stream.facts, attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	claimPublication(t, pipe, prep.prepared)
	require.NoError(t, fixture.term.stagePendingCompletion(ctx, attempt, pipe,
		fixture.stream.facts.terminalFacts(),
		pendingPublication{
			prepared: prep.prepared, facts: fixture.stream.facts, callerCtx: ctx,
			publishable: func() bool { return recvPendingPublishable(fixture.stream) },
			endALeg:     true,
		}), "the real staging callback must install the private batch")
	assert.Same(t, prep.prepared, pipe.pendingPreparedSnapshot(),
		"staging must not dispose an accepted owner's candidate")
	assert.True(t, pipe.pendingPublicationAccepted(), "staging must keep the reservation live")
	staged, reserved, deferred, activated := pendingLifetimeState(pipe)
	assert.True(t, staged, "staging must record the preflighted batch")
	assert.True(t, reserved, "staging must keep the reservation")
	assert.True(t, deferred, "staging must record the deferred observer finish before any delivery")
	assert.False(t, activated, "a staged batch is not deliverable yet")

	require.True(t, pipe.activateReservedPendingPublication(prep.prepared, attempt),
		"an activated publication becomes deliverable")

	// The batch the real staging installed, derived from the real preparation.
	batch := make([]aipapi.Event, 0, len(prep.prepared.events)+1)
	batch = append(batch, prep.prepared.events...)
	batch = append(batch, prep.prepared.finish)

	// The physical drain is lexical to Recv, so the real receive entry drives it.
	var delivered []aipapi.Event
	for range batch {
		ev, recvErr := fixture.stream.Recv(ctx)
		require.NoError(t, recvErr,
			"every staged event must be delivered without ending the stream; err=%v", recvErr)
		delivered = append(delivered, ev)
		if ev.Kind != aipapi.EventResponseFinished {
			assert.Same(t, prep.prepared, pipe.pendingPreparedSnapshot(),
				"the candidate must be retained until the physical finish")
		}
	}
	require.Equal(t, pendingLifetimeLabels(batch, ""), pendingLifetimeLabels(delivered, ""),
		"the accepted batch must be delivered completely, in canonical order")

	// A fully delivered publication ends the stream, so the end of the stream is
	// ONE separate receive and never another delivered event.
	_, recvErr := fixture.stream.Recv(ctx)
	require.ErrorIs(t, recvErr, io.EOF,
		"a fully delivered publication ends the stream at EOF; err=%v", recvErr)

	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"a completed delivery must dispose the retained candidate")
	_, queued := pipe.pendingPublicationHead()
	assert.False(t, queued, "a completed delivery must leave no queued event")
	assert.Equal(t, 1, observer.finishCount(), "a fully delivered publication finishes the observer exactly once")
	assert.Equal(t, response.OutcomeSuccessReleased, observer.lastOutcome(),
		"a fully delivered publication finishes the observer successfully")
}

// TestPendingLifetime_usageCursorAdvancesForEveryPoppedEvent pins the usage cursor:
// the refreshed customer usage sits at a real POSITION inside the activated batch,
// so the cursor must advance once per popped event and report that position
// exactly once, and never at any other position.
//
// Ordinary usage a provider or a gate contributed can carry EXACTLY the same token
// counters as the refreshed customer quantity, so equal counters are a value
// coincidence, not event identity. This case therefore pins both sides of the
// requirement: the explicitly supplied position names the real customer entry,
// and an equal-counter ordinary usage delta on either side of it reports nothing.
func TestPendingLifetime_usageCursorAdvancesForEveryPoppedEvent(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "cursor answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	customer := aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 11, TotalTokens: 11}
	// Equal counters, different accounting metadata and a different raw payload:
	// these are ordinary provider usage entries, not the previewed customer event.
	equalOrdinary := func(source string) aipapi.Event {
		return aipapi.Event{
			Kind: aipapi.EventUsageDelta, OutputTokens: 11, TotalTokens: 11,
			RawUsageJSON: `{"ordinary_usage":"` + source + `"}`,
			Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			},
		}
	}
	batch := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: prep.prepared.text.result},
		equalOrdinary("before"),
		customer,
		equalOrdinary("after"),
		prep.prepared.finish,
	}
	claimPublication(t, p, prep.prepared)
	reservation, ok := p.reservePendingPublication(prep.prepared, false)
	require.True(t, ok, "the claimed candidate must be reservable")
	// The customer entry's position is a known fact of this batch, supplied
	// explicitly instead of being rediscovered from event values.
	require.True(t, p.stageReservedPublication(reservation, attempt, batch, customer, pendingCustomerAt(4)),
		"the live reservation must install its own batch")
	require.True(t, p.activateReservedPendingPublication(prep.prepared, attempt),
		"an activated publication becomes deliverable")

	var usageAt []int
	for at := range batch {
		if p.pendingPublicationNextUsage() {
			usageAt = append(usageAt, at)
		}
		ev, popped := p.popPendingCompletionRelease()
		require.True(t, popped, "every activated event must be deliverable exactly once")
		assert.Equal(t, batch[at].Kind, ev.Kind, "the drain must deliver canonical order")
	}
	assert.Equal(t, []int{4}, usageAt,
		"only the real customer entry may be recognized, once, and an equal-counter ordinary usage delta before or after it must report nothing")
	assert.False(t, p.pendingPublicationNextUsage(),
		"an exhausted drain must never report another usage position")
}

// TestPendingReal_equalTokenOrdinaryUsageEmitsTheCustomerCallbackExactlyOnce pins
// requirement 11.3 through the REAL receive entry: an ordinary usage entry that
// carries EXACTLY the same three token counters as the refreshed customer
// quantity, both before and after the real customer entry, must never reach the
// customer usage callback, and the real entry must reach it exactly once.
//
// The value-coincidence defect is invisible at the seam: it needs the real
// reconstruction to produce a customer quantity an ordinary entry can duplicate.
// So the private batch is activated on the real pipeline with the REAL previewed
// customer quantity, the ordinary entries only duplicate its counters while
// carrying distinct raw payload markers, and the whole thing is delivered by the
// actual Recv loop. The customer position is supplied explicitly: nothing here
// lets the runtime rediscover it from event values.
func TestPendingReal_equalTokenOrdinaryUsageEmitsTheCustomerCallbackExactlyOnce(t *testing.T) {
	t.Parallel()

	probe := &pendingLifetimeUsageProbe{}
	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 3, TotalTokens: 3},
		outCount:  accountingapp.CountResult{OutputTokens: 4, TotalTokens: 7},
	}
	// NO upstream usage events: the only customer quantity in this response is the
	// one the real preparation previewed, so a provider callback can never be
	// mistaken for the customer callback this case counts.
	fixture := newPendingRealStream(t, pendingRealOptions{collector: collector, usageObserver: probe})
	pipe := fixture.pipe
	attempt := fixture.attempt
	stream := fixture.stream
	ctx := context.Background()

	prep, err := pipe.preparePendingCompletion(ctx, stream.facts, attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the real preparation must really own a candidate")

	// The REAL customer quantity, from the real reconstruction through the real
	// collector. Nothing about it is guessed: the ordinary entries below copy its
	// counters, and the delivered probe event is compared against this same event.
	customer := pipe.previewCustomerUsage(ctx, prep.prepared.events, stream.facts.terminalFacts())
	require.Equal(t, aipapi.EventUsageDelta, customer.Kind, "the real preparation must preview customer usage")
	require.Positive(t, customer.TotalTokens, "the real customer quantity must be non-zero")

	// Same three counters, distinct raw payload markers and ordinary provider
	// accounting metadata: value coincidences, never the customer entry.
	equalOrdinary := func(source string) aipapi.Event {
		return aipapi.Event{
			Kind:         aipapi.EventUsageDelta,
			InputTokens:  customer.InputTokens,
			OutputTokens: customer.OutputTokens,
			TotalTokens:  customer.TotalTokens,
			RawUsageJSON: `{"ordinary_usage":"` + source + `"}`,
			Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			},
		}
	}
	batch := make([]aipapi.Event, 0, len(prep.prepared.events)+4)
	batch = append(batch, prep.prepared.events...)
	batch = append(batch, equalOrdinary("before"))
	// The customer position is the batch length immediately BEFORE the append, and
	// it is handed to the staging seam explicitly.
	customerAt := pendingCustomerAt(len(batch))
	batch = append(batch, customer)
	batch = append(batch, equalOrdinary("after"))
	batch = append(batch, prep.prepared.finish)

	claimPublication(t, pipe, prep.prepared)
	reservation, ok := pipe.reservePendingPublication(prep.prepared, false)
	require.True(t, ok, "the claimed candidate must be reservable")
	require.True(t, pipe.stageReservedPublication(reservation, attempt, batch, customer, customerAt),
		"the live reservation must install the activated private batch")
	require.True(t, pipe.activateReservedPendingPublication(prep.prepared, attempt),
		"an activated publication becomes deliverable")

	released, drainErr := fixture.drain(t)
	require.True(t, errors.Is(drainErr, io.EOF),
		"a fully delivered publication ends the stream at EOF; released=%v err=%v",
		pendingRealLabels(released), drainErr)

	usageCount := pendingRealCountKind(released, aipapi.EventUsageDelta)
	require.GreaterOrEqual(t, usageCount, 3,
		"the fixture must really deliver the equal-counter ordinary entries around the customer entry, so the collision is possible; released=%v",
		pendingRealLabels(released))

	events := probe.seen()
	require.Len(t, events, 1,
		"only the real customer entry may reach the customer usage callback, even when ordinary usage shares its counters; released=%v seen=%+v",
		pendingRealLabels(released), events)
	assert.Equal(t, customer.InputTokens, events[0].InputTokens,
		"the emitted usage must be the real customer quantity")
	assert.Equal(t, customer.OutputTokens, events[0].OutputTokens,
		"the emitted usage must be the real customer quantity")
	assert.Equal(t, customer.TotalTokens, events[0].TotalTokens,
		"the emitted usage must be the real customer quantity")
	assert.Equal(t, attempt.bleg.BLegID, events[0].BLegID,
		"the emitted usage must stay attributed to the real B-leg")
	assert.Equal(t, int(attempt.bleg.Seq), events[0].AttemptSeq,
		"the emitted usage must stay attributed to the real attempt sequence")
}

// TestPendingLifetime_physicalDeliveryRunsTheOrdinaryClientEventEffectsOnce pins
// the effects the private drain used to bypass: the attempt's client-event
// accounting and the recovery policy's client observation must run on actual
// physical delivery, including the actual finish, and never while staging.
//
// The private queue is installed directly on the real pipeline and attempt, and a
// fresh recovery policy is installed at the seam the receive loop really observes
// client events through. Everything is read from that one policy and that one
// accounting snapshot, so no separate handmade policy can claim effects the stream
// under test never produced.
func TestPendingLifetime_physicalDeliveryRunsTheOrdinaryClientEventEffectsOnce(t *testing.T) {
	t.Parallel()

	policy := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	fixture := newPendingRealStream(t, pendingRealOptions{recoveryPolicy: policy})
	attempt := fixture.attempt
	batch, _ := pendingLifetimeDirectQueue(t, fixture)
	ctx := context.Background()

	// Staging and activation prefight the whole batch but release nothing, so no
	// client-event effect has run yet.
	assert.True(t, attempt.accountingSnapshot().RemoteCompletedAt.IsZero(),
		"staging must not account a client finish")
	assert.True(t, attempt.accountingSnapshot().ProxyCompletedAt.IsZero(),
		"staging must not complete the proxy")
	assert.Equal(t, streamrecovery.DecisionRecoverPreOutput,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"staging must not report client output to the recovery policy")

	// The result text is the first client-visible output of the publication, so it
	// is the point where the recovery policy must start reporting client output
	// while the accepted finish is still queued.
	textAt := pendingRealIndexOf(batch, aipapi.EventTextDelta)
	finishAt := pendingRealIndexOf(batch, aipapi.EventResponseFinished)
	require.GreaterOrEqual(t, textAt, 0, "the private batch must really carry the result text")
	require.Greater(t, finishAt, textAt, "the private batch must really end with the accepted finish")
	for range textAt + 1 {
		ev, recvErr := fixture.stream.Recv(ctx)
		require.NoError(t, recvErr, "every staged event before the finish must be delivered; err=%v", recvErr)
		require.NotEqual(t, aipapi.EventResponseFinished, ev.Kind,
			"the accepted finish must stay queued while the result text is delivered")
	}

	assert.Equal(t, streamrecovery.DecisionFinishPostOutput,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"the physically delivered result must be reported as client output by the recovery policy")
	assert.True(t, attempt.accountingSnapshot().RemoteCompletedAt.IsZero(),
		"a queued-but-undelivered finish must not be accounted as a client finish")

	// Delivering the accepted finish is what completes the client side, once.
	for range finishAt - textAt - 1 {
		_, recvErr := fixture.stream.Recv(ctx)
		require.NoError(t, recvErr, "every staged event up to the finish must be delivered; err=%v", recvErr)
	}
	final, recvErr := fixture.stream.Recv(ctx)
	require.NoError(t, recvErr, "the accepted finish must be delivered; err=%v", recvErr)
	require.Equal(t, aipapi.EventResponseFinished, final.Kind, "the accepted finish must be delivered next")

	accounting := attempt.accountingSnapshot()
	assert.False(t, accounting.RemoteCompletedAt.IsZero(),
		"the physically delivered finish must be accounted by the attempt")
	assert.False(t, accounting.ProxyCompletedAt.IsZero(),
		"the physically delivered finish must complete the proxy")
	assert.Equal(t, streamrecovery.DecisionPassThrough,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"a delivered finish must be reported as a finished response")

	// The end of the stream is ONE separate receive, and it must not repeat the
	// client finish effects.
	_, recvErr = fixture.stream.Recv(ctx)
	require.ErrorIs(t, recvErr, io.EOF, "a fully delivered publication ends the stream at EOF; err=%v", recvErr)
	after := attempt.accountingSnapshot()
	assert.Equal(t, accounting.RemoteCompletedAt, after.RemoteCompletedAt,
		"the end of the stream must not re-run the client finish accounting")
	assert.Equal(t, accounting.ProxyCompletedAt, after.ProxyCompletedAt,
		"the end of the stream must not re-run the proxy completion")
	assert.Equal(t, streamrecovery.DecisionPassThrough,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"the end of the stream must not change what the policy observed")
}

// TestPendingLifetime_abandonWithoutObserverStillClearsOwnedPendingState pins the
// cleanup half: an attempt without a final-stream observer owns no observer finish
// to close, but its own reserved and staged publication must still be discarded so
// no phantom release effect survives a failed settlement, handoff, or loser path.
func TestPendingLifetime_abandonWithoutObserverStillClearsOwnedPendingState(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "no observer answer")
	require.Nil(t, attempt.finalStreamObs, "the fixture must really have no final-stream observer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)

	claimPublication(t, p, prep.prepared)
	reservation, ok := p.reservePendingPublication(prep.prepared, false)
	require.True(t, ok, "the claimed candidate must be reservable")
	require.True(t, p.stageReservedPublication(reservation, attempt,
		pendingFenceEvents(prep.prepared.text.result), aipapi.Event{}, pendingCustomerAbsent),
		"the live reservation must install its own batch")
	staged, _, _, _ := pendingLifetimeState(p)
	require.True(t, staged, "the fixture must really hold a staged batch")

	newTurnTerminal().abandonDeferredFinalObservation(p, attempt)
	assert.Nil(t, p.pendingPreparedSnapshot(),
		"an abandoning caller must drop the owned candidate even without an observer")
	assert.False(t, p.pendingPublicationActive(), "an abandoned publication is never deliverable")
	_, queued := p.pendingPublicationHead()
	assert.False(t, queued, "an abandoned publication must leave no queued event")
	staged, reserved, _, _ := pendingLifetimeState(p)
	assert.False(t, staged, "an abandoned publication must drop its staged batch")
	assert.False(t, reserved, "an abandoned publication must drop its reservation")
}

// TestPendingLifetime_abandonOfAnotherAttemptLeavesTheOwnedCandidateIntact pins the
// ownership rule of the abandon seam: a caller that owns no private publication
// must not clear the live candidate of another origin.
func TestPendingLifetime_abandonOfAnotherAttemptLeavesTheOwnedCandidateIntact(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "owned answer")
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	other := pendingLifetimeOtherAttempt(t)
	_ = pendingOpenObserver(t, other)
	newTurnTerminal().abandonDeferredFinalObservation(p, other)
	assert.Same(t, prep.prepared, p.pendingPreparedSnapshot(),
		"an abandoning caller must not clear a candidate it does not own")
}

// TestPendingReal_surfaceFailureInvalidatesTheCandidateBeforeAnySuccessReturn pins
// the surfaced-failure half: a terminal the provider turned into a controlled
// failure never publishes a private result, and its successful return must not
// leave a candidate behind for a later route.
func TestPendingReal_surfaceFailureInvalidatesTheCandidateBeforeAnySuccessReturn(t *testing.T) {
	t.Parallel()

	fixture := newPendingRealStream(t, pendingRealOptions{
		decision: &pendingLifetimeSurfaceFailure{providerID: "pending-lifetime-surface-failure"},
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})
	pipe := fixture.pipe

	released, _ := fixture.drain(t)
	assert.Zero(t, pendingRealCountText(released, pendingRealResult),
		"a surfaced failure must never publish the private result; released=%v", pendingRealLabels(released))
	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"a surfaced failure must invalidate the candidate before any success return")
	assert.False(t, pipe.pendingPublicationActive(),
		"a surfaced failure must never make a private publication deliverable")
	_, queued := pipe.pendingPublicationHead()
	assert.False(t, queued, "a surfaced failure must leave no queued private result")
}

// pendingLifetimeSurfaceFailure is a terminal decision provider that turns the
// accepted finish into the platform's controlled failure decision.
type pendingLifetimeSurfaceFailure struct {
	providerID string
	calls      int
}

func (p *pendingLifetimeSurfaceFailure) ID() string { return p.providerID }

func (p *pendingLifetimeSurfaceFailure) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	p.calls++
	return terminaldecision.Decision{Kind: terminaldecision.DecisionSurfaceFailure, ReasonCode: "pending_surface_failure"}, nil
}

// TestPendingReal_realCustomerUsageIsEmittedOnceAtItsDeliveredPosition pins the
// requirement 11.3 usage seam on the REAL receive loop: the refreshed customer
// usage that the private drain releases must reach the real usage observer exactly
// once, at the usage position between the published result and the accepted
// finish, and it must be the customer quantity the collector really produced
// rather than any operator or provider usage.
func TestPendingReal_realCustomerUsageIsEmittedOnceAtItsDeliveredPosition(t *testing.T) {
	t.Parallel()

	probe := &pendingLifetimeUsageProbe{}
	collector := &pendingRealCollector{
		callCount: accountingapp.CountResult{InputTokens: 3, TotalTokens: 3},
		outCount:  accountingapp.CountResult{OutputTokens: 4, TotalTokens: 7},
	}
	fixture := newPendingRealStream(t, pendingRealOptions{
		collector:     collector,
		usageObserver: probe,
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})
	attempt := fixture.attempt

	released, err := fixture.drain(t)
	require.GreaterOrEqual(t, pendingRealIndexOf(released, aipapi.EventResponseFinished), 0,
		"the accepted finish must be released before the stream ends; released=%v", pendingRealLabels(released))
	require.True(t, errors.Is(err, io.EOF),
		"a fully delivered private publication ends the stream at EOF; released=%v err=%v",
		pendingRealLabels(released), err)

	events := probe.seen()
	require.Len(t, events, 1,
		"the released customer usage must reach the real usage observer exactly once; saw=%+v", events)
	assert.Equal(t, attempt.bleg.BLegID, events[0].BLegID,
		"the customer usage must stay attributed to the real B-leg")
	assert.Equal(t, int(attempt.bleg.Seq), events[0].AttemptSeq,
		"the customer usage must stay attributed to the real attempt sequence")
	assert.Equal(t, 4, events[0].OutputTokens,
		"the emitted customer usage must be the reconstructed customer quantity")

	usageAt := pendingRealIndexOf(released, aipapi.EventUsageDelta)
	textAt := pendingRealIndexOf(released, aipapi.EventTextDelta)
	finishAt := pendingRealIndexOf(released, aipapi.EventResponseFinished)
	require.GreaterOrEqual(t, usageAt, 0,
		"the refreshed customer usage must be released; released=%v", pendingRealLabels(released))
	assert.Less(t, textAt, usageAt, "the result must precede the customer usage")
	assert.Less(t, usageAt, finishAt, "the customer usage must precede the finish")
	assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventUsageDelta),
		"the customer usage must be released exactly once; released=%v", pendingRealLabels(released))

	text, calls := collector.outputText()
	assert.Positive(t, calls, "the real customer reconstruction must run for this terminal")
	assert.Contains(t, text, pendingRealResult,
		"the customer projection must count the published result as ordinary assistant content")
}

// pendingLifetimeUsageProbe is the real customer-plane usage observer. It records
// every event the runtime emits so a case can prove the emission count and the
// exact quantity and attribution it carried.
type pendingLifetimeUsageProbe struct {
	mu     sync.Mutex
	events []usage.Event
}

func (p *pendingLifetimeUsageProbe) OnUsage(_ context.Context, ev usage.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return nil
}

func (p *pendingLifetimeUsageProbe) seen() []usage.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]usage.Event, len(p.events))
	copy(out, p.events)
	return out
}

// pendingLifetimeALegTerminal gives one terminal a REAL shared A-leg, so a case can
// land an authoritative A-leg cause at a real fence instead of fabricating one.
func pendingLifetimeALegTerminal(t *testing.T, aLegID string) (*turnTerminal, *leglifecycle.ALeg) {
	t.Helper()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aLeg := coord.StartALeg(aLegID)
	require.NotNil(t, aLeg, "the fixture must really own a shared A-leg")
	return newTurnTerminalWithALeg(aLeg, aLegEndBase), aLeg
}

// pendingLifetimeRecvStream composes the real receive facade over exactly the
// owners one publication delivery sequences across, so a case can hand the real
// publication seams the same view Recv holds.
func pendingLifetimeRecvStream(p *responsePipeline, terminal *turnTerminal, attempt *attemptSession) *retryRecvStream {
	return &retryRecvStream{
		facts:            pendingDrainFacts(),
		responsePipeline: p,
		terminal:         terminal,
		recovery:         &recoveryController{},
		attempt:          attemptSlot{current: attempt},
	}
}

// pendingLifetimeStageOnce drives the REAL staging callback so a case holds a
// genuine staged reservation rather than a hand-built queue, then reports the
// retained candidate the seams must revalidate.
func pendingLifetimeStageOnce(t *testing.T, terminal *turnTerminal, p *responsePipeline, attempt *attemptSession) *pendingCompletion {
	t.Helper()
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)
	require.NoError(t, terminal.stagePendingCompletion(context.Background(), attempt, p,
		pendingDrainFacts().terminalFacts(),
		pendingPublication{
			prepared: prep.prepared, facts: pendingDrainFacts(),
			callerCtx: context.Background(), endALeg: true,
		}), "the fixture must really stage its own batch")
	staged, _, _, _ := pendingLifetimeState(p)
	require.True(t, staged, "the fixture must really hold a staged batch")
	return prep.prepared
}

// pendingLifetimeBarrierCounter blocks the REAL token-accounting reconstruction so
// a case can hold the accepted terminal open at a deterministic point between
// publication preparation and the publication claim.
type pendingLifetimeBarrierCounter struct {
	callCount accountingapp.CountResult
	outCount  accountingapp.CountResult

	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPendingLifetimeBarrierCounter(
	callCount, outCount accountingapp.CountResult,
) *pendingLifetimeBarrierCounter {
	return &pendingLifetimeBarrierCounter{
		callCount: callCount, outCount: outCount,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *pendingLifetimeBarrierCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.callCount, nil
}

func (c *pendingLifetimeBarrierCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return c.outCount, nil
}

// awaitEntryOrRecvDone waits for the real token-accounting barrier and fails
// promptly when the receive under test returned first, so a case can never
// mistake "the seam was never reached" for a successful barrier.
func (c *pendingLifetimeBarrierCounter) awaitEntryOrRecvDone(t *testing.T, recvDone <-chan pendingRecvOutcome) {
	t.Helper()
	select {
	case <-c.entered:
	case got := <-recvDone:
		t.Fatalf("the receive finished before the real token-accounting barrier: ev=%q err=%v", got.ev.Kind, got.err)
	case <-time.After(pendingLifetimeBarrierWait):
		t.Fatal("the real token-accounting barrier was never reached")
	}
}

func (c *pendingLifetimeBarrierCounter) unblock() { close(c.release) }

// pendingRecvOutcome is one asynchronous receive result. Carrying the event and
// the error together lets a case order a channel barrier against a receive that
// returned early instead of losing that fact.
type pendingRecvOutcome struct {
	ev  aipapi.Event
	err error
}

// pendingRealFinishEvents is the passive backend stream every real publication
// preflight case replays: the two leading lifecycle frames, then the accepted
// finish. Nothing else may be queued, so every private event these cases observe
// came from the publication under test.
func pendingRealFinishEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	}
}

// pendingRealReceiveLeadingLifecycle receives the two leading backend lifecycle
// events SYNCHRONOUSLY. They must be consumed before any barrier is armed:
// the first receive of a real stream returns response_started, so a case that
// armed a publication barrier on the first receive would wait for a seam the
// backend frames never reach.
func pendingRealReceiveLeadingLifecycle(t *testing.T, stream *retryRecvStream) []aipapi.Event {
	t.Helper()
	received := make([]aipapi.Event, 0, 2)
	for _, want := range []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventMessageStarted} {
		ev, err := stream.Recv(context.Background())
		require.NoError(t, err, "a leading lifecycle event must be delivered; err=%v", err)
		require.Equal(t, want, ev.Kind, "the passive backend stream must really lead with its lifecycle frames")
		received = append(received, ev)
	}
	return received
}

// pendingRealRecvAsync starts ONE receive on its own goroutine. It is the receive
// that evaluates the accepted finish, so the caller can hold a publication
// preflight open and observe state that only exists inside the terminal call.
func pendingRealRecvAsync(stream *retryRecvStream) <-chan pendingRecvOutcome {
	return pendingRealRecvAsyncOn(context.Background(), stream)
}

// pendingRealRecvAsyncOn starts ONE receive under the SUPPLIED caller context.
// A case that cancels the original caller must reach the real Recv with that very
// context, because the publication fence reads the caller the receive was entered
// with; a detached or background context would never observe the cancellation.
func pendingRealRecvAsyncOn(ctx context.Context, stream *retryRecvStream) <-chan pendingRecvOutcome {
	out := make(chan pendingRecvOutcome, 1)
	go func() {
		ev, err := stream.Recv(ctx)
		out <- pendingRecvOutcome{ev: ev, err: err}
	}()
	return out
}

// pendingLifetimeCloseDecider is a real terminal-decision provider that lets a
// case observe WHICH candidates the chokepoint actually asked about. It exists
// because an authoritative candidate (a real Close publishes
// CandidateCauseCancellation) is decided by core as a typed pass-through and the
// provider is deliberately BYPASSED for it, so only a non-authoritative
// candidate can ever reach Decide. It decides nothing else: every candidate it
// sees is allowed to stop.
type pendingLifetimeCloseDecider struct {
	seen   atomic.Int32
	causes chan terminaldecision.CandidateCause
}

func newPendingLifetimeCloseDecider() *pendingLifetimeCloseDecider {
	return &pendingLifetimeCloseDecider{causes: make(chan terminaldecision.CandidateCause, 8)}
}

func (d *pendingLifetimeCloseDecider) ID() string { return "pending-lifetime-close-decider" }

func (d *pendingLifetimeCloseDecider) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	d.seen.Add(1)
	select {
	case d.causes <- in.Candidate.Cause:
	default:
	}
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "pending_lifetime_allow_stop"}, nil
}

// pendingRealBillingCapture is the REAL terminal call-closure sink a case
// installs on a real receive fixture, over the same valid stamped billing facts
// the accepted path uses.
//
// The record the runtime supplies is retained WHOLE: its outcome, its call
// identity, and its frozen correlation are exactly the evidence these cases
// assert on, so substituting a schema-only shell here would make every billing
// assertion vacuous. Snapshots clone the one slice field so a case can inspect
// the record after the sink returned without racing the terminal.
type pendingRealBillingCapture struct {
	mu      sync.Mutex
	records []usageevidence.CallUsageRecord
}

// pendingRealInstallBilling installs the real call-closure sink on a real fixture
// and stamps the request with the valid billing facts the terminal requires
// before it will seal and hand off a record at all. An unstamped or unvalidated
// request makes the closure seam a silent no-op, so this fixture deliberately
// supplies everything the production seal path demands.
func pendingRealInstallBilling(t *testing.T, f *pendingRealStream) *pendingRealBillingCapture {
	t.Helper()
	callID, err := usageevidence.NewBillingCallID()
	require.NoError(t, err, "the billing sink must really carry a valid billing call id")
	capture := &pendingRealBillingCapture{}
	f.term.appendBillingCall = func(_ context.Context, record usageevidence.CallUsageRecord) error {
		// The record arrives already sealed by the production terminal, so
		// capturing it verbatim IS capturing the real billing evidence.
		record.ExpectedBLegIDs = append([]string(nil), record.ExpectedBLegIDs...)
		capture.mu.Lock()
		defer capture.mu.Unlock()
		capture.records = append(capture.records, record)
		return nil
	}
	// The workload identity seam is installed directly so the closure does not
	// depend on a request-authority carrier the bare receive context lacks; the
	// zero identity is a valid legacy primary workload.
	f.term.billingWorkload = func(context.Context, string) usageevidence.WorkloadIdentity {
		return usageevidence.WorkloadIdentity{}
	}
	withTestRecvFacts(f.stream, func(f recvTurnFacts) recvTurnFacts {
		f.billingCallID = callID
		f.billingCallState = newBillingCallState(callID)
		f.billingAccountID = "acct-pending-real"
		f.billingIdentityStamped = true
		f.billingCustomerPricing = usageevidence.VersionRef{ID: "pricing:test", Version: "1"}
		f.billingChargePolicy = usageevidence.VersionRef{ID: "policy:test", Version: "1"}
		return f
	})
	return capture
}

// sealed returns every record the real terminal actually handed to the sink.
func (c *pendingRealBillingCapture) sealed() []usageevidence.CallUsageRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]usageevidence.CallUsageRecord(nil), c.records...)
}

// completed returns only the records the runtime classified as a NORMAL-COMPLETED
// turn. An unsuccessful closure (a cancellation or a failure command) is legal in
// a withdrawal schedule; this normal-success outcome is the one that must never
// be admitted for a publication that will not be released.
func (c *pendingRealBillingCapture) completed() []usageevidence.CallUsageRecord {
	var out []usageevidence.CallUsageRecord
	for _, record := range c.sealed() {
		if record.Outcome == usageevidence.TurnOutcomeCompleted {
			out = append(out, record)
		}
	}
	return out
}

// pendingLifetimeSettlementBarrier blocks the REAL request-terminal settlement
// seam, which runs AFTER a successful private stage and BEFORE the normal-success
// billing handoff. It is the deterministic point a case can hold the winning
// request owner open at: a withdrawal that lands while it is blocked is already
// admitted for settlement but must still be refused for every LATER effect.
//
// It uses the existing pendingRealOptions.settleRequestHook seam, so no
// production test hook, getter, or receiver is added for it.
type pendingLifetimeSettlementBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPendingLifetimeSettlementBarrier() *pendingLifetimeSettlementBarrier {
	return &pendingLifetimeSettlementBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

// settle is the case-owned replacement for the request-terminal settlement seam.
func (b *pendingLifetimeSettlementBarrier) settle(context.Context, []metering.Fact) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

// awaitEntryOrRecvDone fails promptly when the receive under test returned before
// the settlement seam was reached, so a missing barrier can never be mistaken for
// a successful one.
func (b *pendingLifetimeSettlementBarrier) awaitEntryOrRecvDone(t *testing.T, recvDone <-chan pendingRecvOutcome) {
	t.Helper()
	select {
	case <-b.entered:
	case got := <-recvDone:
		t.Fatalf("the receive finished before the settlement barrier: ev=%q err=%v", got.ev.Kind, got.err)
	case <-time.After(pendingLifetimeBarrierWait):
		t.Fatal("the real request-terminal settlement seam was never reached")
	}
}

func (b *pendingLifetimeSettlementBarrier) unblock() { close(b.release) }

// pendingLifetimeStageTime and pendingLifetimeDeliveryTime are the two fixed
// phases of the test-only clock below. They are deliberately far apart and
// non-zero, so a client finish effect that ran while the batch was still a
// candidate is distinguishable from the one that ran at the physical delivery.
var (
	pendingLifetimeStageTime    = time.Unix(1_700_000_001, 0).UTC()
	pendingLifetimeDeliveryTime = time.Unix(1_700_000_002, 0).UTC()
)

// pendingLifetimeClock is a test-only fixed clock installed at the pipeline's
// existing now seam. Every client-event effect reads it, so a case can prove
// WHICH boundary stamped a completion timestamp instead of only proving that
// some timestamp exists.
type pendingLifetimeClock struct{ nanos atomic.Int64 }

func (c *pendingLifetimeClock) set(at time.Time) { c.nanos.Store(at.UnixNano()) }

func (c *pendingLifetimeClock) now() time.Time { return time.Unix(0, c.nanos.Load()).UTC() }

// TestPendingReal_stagedToActivationALegCancellationWithdrawsThePublication pins
// the full live fence at ACTIVATION: an authoritative A-leg cause that lands while
// the batch is staged but not yet deliverable must stop the release, so nothing is
// ever queued for a response whose A-leg already failed.
func TestPendingReal_stagedToActivationALegCancellationWithdrawsThePublication(t *testing.T) {
	t.Parallel()

	terminal, aLeg := pendingLifetimeALegTerminal(t, "aleg-pending-activation-1")
	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "activation answer")
	observer := pendingOpenObserver(t, attempt)
	pendingLifetimeStageOnce(t, terminal, p, attempt)

	stream := pendingLifetimeRecvStream(p, terminal, attempt)
	// The receive-side publication window is open and the caller is alive, so the
	// only withdrawal fact left is the authoritative A-leg cause below: the
	// activation seam must consult it ITSELF instead of trusting a callback that
	// reports the window open.
	hooks := recvFinishAuthorityInput(context.Background(), stream, attempt, p.pendingPreparedSnapshot(), true,
		func(*pendingCompletion, *attemptSession) bool { return recvPendingPublishable(stream) })
	// The A-leg carries an authoritative cause after staging and before the batch
	// is deliverable. Nothing else changes: the caller is alive, the window is
	// open, and the retained candidate still owns the live slot.
	require.NoError(t, aLeg.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}))
	require.Error(t, terminal.aLegErr(), "the fixture must really carry an authoritative A-leg cause")

	require.ErrorIs(t, terminal.activatePendingPublication(context.Background(), p, hooks),
		errPendingPublicationWithdrawn,
		"an authoritative A-leg cause after staging must stop activation instead of reporting success")
	assert.False(t, p.pendingPublicationActive(),
		"an A-leg cancellation between staging and activation must never make the batch deliverable")
	_, queued := p.pendingPublicationHead()
	assert.False(t, queued, "an A-leg cancellation between staging and activation must leave no queued event")
	assert.Nil(t, p.pendingPreparedSnapshot(),
		"an A-leg cancellation must dispose the retained candidate instead of stranding it")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn publication must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_activationMustNotReuseAnotherReservationsBatch pins the SAME
// reservation half of the activation fence: a staged publication can only be
// activated for the exact reservation that installed it, so a foreign attempt can
// never make another origin's batch deliverable.
func TestPendingReal_activationMustNotReuseAnotherReservationsBatch(t *testing.T) {
	t.Parallel()

	terminal, _ := pendingLifetimeALegTerminal(t, "aleg-pending-activation-2")
	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "reserved answer")
	prepared := pendingLifetimeStageOnce(t, terminal, p, attempt)
	other := pendingLifetimeOtherAttempt(t)

	stream := pendingLifetimeRecvStream(p, terminal, other)
	// EVERY live fence is deliberately open here: the publication window is open,
	// the caller is alive, and there is no A-leg cause. The refusal must therefore
	// come from the activation seam re-checking the EXPECTED candidate and origin
	// it was pinned to, not from a callback noticing a closed fence.
	hooks := recvFinishAuthorityInput(context.Background(), stream, other, prepared, true,
		func(*pendingCompletion, *attemptSession) bool { return recvPendingPublishable(stream) })
	require.ErrorIs(t, terminal.activatePendingPublication(context.Background(), p, hooks),
		errPendingPublicationWithdrawn,
		"a foreign origin must never activate another origin's staged batch")
	assert.False(t, p.pendingPublicationActive(),
		"a foreign origin must never make the real owner's batch deliverable")
	_, queued := p.pendingPublicationHead()
	assert.False(t, queued, "a foreign origin must leave no queued event")
	assert.Same(t, prepared, p.pendingPreparedSnapshot(),
		"a refused foreign activation must not dispose the real owner's candidate")
}

// TestPendingReal_closedFenceBeforeStagingWithdrawsTheExpectedPublication pins the
// earliest withdrawal boundary: an expected publication whose fence is already
// closed before the claim must report the withdrawal, never a success that lets
// settlement and billing handoff continue for a batch that will not be released.
func TestPendingReal_closedFenceBeforeStagingWithdrawsTheExpectedPublication(t *testing.T) {
	t.Parallel()

	p := newResponsePipeline()
	attempt := pendingDrainAttempt(t, "closed fence answer")
	observer := pendingOpenObserver(t, attempt)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	// The receive-side publication window is already closed, and nothing was staged.
	stageErr := newTurnTerminal().stagePendingCompletion(context.Background(), attempt, p,
		pendingDrainFacts().terminalFacts(),
		pendingPublication{
			prepared: prep.prepared, facts: pendingDrainFacts(),
			callerCtx: context.Background(), endALeg: true,
			publishable: func() bool { return false },
		})

	require.ErrorIs(t, stageErr, errPendingPublicationWithdrawn,
		"an expected publication whose fence is closed before the claim must report the withdrawal, not success")
	staged, reserved, _, activated := pendingLifetimeState(p)
	assert.False(t, staged, "a withdrawn publication must install no batch")
	assert.False(t, reserved, "a withdrawn publication must release its reservation")
	assert.False(t, activated, "a withdrawn publication must never become deliverable")
	_, queued := p.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn publication must leave no queued event")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn publication must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_claimFailureStopsCustomerSettlementAndBillingHandoff pins the
// claim boundary on the REAL terminal path with a deterministic barrier: a Close
// that wins while the accepted terminal is still inside its own authority
// preparation must leave no successful customer settlement, no normal-success
// billing handoff, and no deliverable queue.
//
// The barrier is the real token-accounting reconstruction of AuthorityPrepare,
// which runs BEFORE the winning request owner claims the publication, so the
// withdrawal lands exactly between the prepared candidate and its claim. A Close
// cannot be awaited synchronously here: the running attempt terminal winner owns
// the attempt and Close competes for it, so Close runs concurrently and the
// counter is released only after the deferred observer Finish proves the Close
// already closed the publication window and withdrew the expected candidate.
// A terminal-decision provider cannot order this: an authoritative candidate is a
// typed core pass-through and never reaches a provider.
func TestPendingReal_claimFailureStopsCustomerSettlementAndBillingHandoff(t *testing.T) {
	t.Parallel()

	counter := newPendingLifetimeBarrierCounter(
		accountingapp.CountResult{InputTokens: 2, TotalTokens: 2},
		accountingapp.CountResult{OutputTokens: 3, TotalTokens: 5},
	)
	decider := newPendingLifetimeCloseDecider()
	var settled atomic.Bool
	fixture := newPendingRealStream(t, pendingRealOptions{
		counter:  counter,
		decision: decider,
		observer: true,
		settleRequestHook: func(context.Context, []metering.Fact) error {
			settled.Store(true)
			return nil
		},
		events: pendingRealFinishEvents(),
	})
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")

	// The REAL terminal call-closure seam, with valid stamped facts, is what
	// proves billing. A settlement probe alone would only prove one effect; an
	// unsuccessful closure (a Close-owned cancellation) is legal here, a
	// normal-success handoff for a batch that is never released is not. The
	// record the terminal supplies is captured whole, so the outcome below is the
	// REAL one rather than an empty comparison target.
	capture := pendingRealInstallBilling(t, fixture)

	pendingRealReceiveLeadingLifecycle(t, fixture.stream)
	recvDone := pendingRealRecvAsync(fixture.stream)
	counter.awaitEntryOrRecvDone(t, recvDone)

	// A real Close wins while the accepted terminal is still preparing its own
	// authority. Close closes the publication window and withdraws the candidate
	// BEFORE it competes for the attempt terminal, so the deferred observer Finish
	// it performs on the way there is the deterministic proof the claim below finds
	// nothing to publish.
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.stream.Close() }()
	select {
	case <-observer.finished():
	case <-time.After(pendingLifetimeBarrierWait):
		t.Fatal("the real Close must close the deferred observer while the accepted terminal prepares its authority")
	}
	counter.unblock()

	require.NoError(t, <-closeDone, "a Close that wins the publication must still succeed")
	got := <-recvDone
	requirePendingPublicationCloseWon(t, got.err,
		"a publication withdrawn before its claim must fail the terminal instead of reporting success")
	assert.Positive(t, decider.seen.Load(),
		"the accepted normal candidate must really have been evaluated at the terminal-decision chokepoint")
	assert.False(t, settled.Load(),
		"a publication withdrawn before its claim must settle no customer authority")
	assert.Empty(t, capture.completed(),
		"a publication withdrawn before its claim must never hand off a NORMAL-COMPLETED billing call; sealed=%+v",
		capture.sealed())
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a publication withdrawn before its claim must never become deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a publication withdrawn before its claim must leave no queued event")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a publication withdrawn before its claim must leave no retained candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn publication must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_acceptedBaselineReallyClosesTheCall proves the billing
// evidence the withdrawal schedules below rely on is NOT vacuous: the SAME
// configured sink, on the SAME real fixture, really does receive at least one
// NORMAL-COMPLETED sealed record when the accepted path runs to its normal end.
//
// Without this control, "no completed closure" in a withdrawal schedule could be
// satisfied by a sink that never observes a completed call at all.
func TestPendingReal_acceptedBaselineReallyClosesTheCall(t *testing.T) {
	t.Parallel()

	fixture := newPendingRealStream(t, pendingRealOptions{
		observer: true,
		events:   pendingRealFinishEvents(),
	})
	capture := pendingRealInstallBilling(t, fixture)

	released, err := fixture.drain(t)
	require.ErrorIs(t, err, io.EOF,
		"the accepted baseline must end the stream at EOF; released=%v err=%v", pendingRealLabels(released), err)
	require.GreaterOrEqual(t, pendingRealIndexOf(released, aipapi.EventResponseFinished), 0,
		"the accepted baseline must really release its finish; released=%v", pendingRealLabels(released))

	completed := capture.completed()
	require.NotEmpty(t, completed,
		"this configured sink must really observe a NORMAL-COMPLETED call closure on the accepted path, "+
			"otherwise every withdrawal assertion against it is vacuous; sealed=%+v", capture.sealed())
	sealed := completed[0]
	assert.Equal(t, usageevidence.TurnOutcomeCompleted, sealed.Outcome,
		"the accepted baseline must really seal a normal-success outcome")
	assert.NotEmpty(t, sealed.AccountID, "the sealed closure must carry the stamped customer account")
	assert.NotEmpty(t, sealed.ALegID, "the sealed closure must carry the real A-leg correlation")
	assert.NotEmpty(t, sealed.Fingerprint,
		"the record must be the production-sealed one, never a substituted shell")
	assert.Equal(t, 1, fixture.observer.finishCount(),
		"the accepted baseline must close its observer exactly once")
}

// TestPendingReal_callerCancellationAfterSettlementStopsTheBillingHandoff is the
// real regression for the admission gap between a successful private stage and the
// later success effects of the SAME winning request owner.
//
// The schedule uses the existing real fixture and its real Recv: the two leading
// backend frames are consumed synchronously, the THIRD receive reaches the
// accepted terminal, stages its private batch successfully, and then blocks
// inside the real request-terminal settlement seam. The ORIGINAL caller context
// of that receive — the very context the publication fence reads — is cancelled
// while the settlement is blocked, and only then is the settlement released with
// a success.
//
// The already-admitted settlement may complete; its private effect cannot be
// undone and is not rejected for that. What must NOT happen is the NEXT
// not-yet-admitted success effect: the normal-success billing handoff. The
// publication was withdrawn, so it can never be released, and admitting a
// completed call closure for it is exactly the defect under test.
func TestPendingReal_callerCancellationAfterSettlementStopsTheBillingHandoff(t *testing.T) {
	t.Parallel()

	barrier := newPendingLifetimeSettlementBarrier()
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:          true,
		settleRequestHook: barrier.settle,
		events:            pendingRealFinishEvents(),
	})
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")
	capture := pendingRealInstallBilling(t, fixture)

	pendingRealReceiveLeadingLifecycle(t, fixture.stream)
	// The fence reads the caller the receive was ENTERED with, so the
	// cancellation must reach the real Recv through its own context.
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	recvDone := pendingRealRecvAsyncOn(callerCtx, fixture.stream)
	barrier.awaitEntryOrRecvDone(t, recvDone)

	// The private stage already succeeded and customer settlement is already
	// admitted; this is precisely the window in which the expected publication
	// fence can still close.
	cancelCaller()
	barrier.unblock()

	got := <-recvDone
	require.ErrorIs(t, got.err, errPendingPublicationWithdrawn,
		"a caller cancellation after a successful private stage must withdraw the publication, "+
			"never report a successful turn for a batch that will not be released")
	assert.Empty(t, capture.completed(),
		"a caller cancellation after settlement must never admit a NORMAL-COMPLETED billing handoff; sealed=%+v",
		capture.sealed())
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a withdrawn publication must never become deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn publication must leave no queued event")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a withdrawn publication must leave no retained candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn publication must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_realCloseAfterSettlementStopsTheBillingHandoff is the SAME
// admission boundary driven by a real Close instead of a caller cancellation.
//
// Close closes the publication window and disposes the retained candidate BEFORE
// it competes for the attempt terminal the winning request owner is still
// running, and the deferred observer Finish it performs on the way there is the
// deterministic ordering proof that the withdrawal already happened. Settlement
// is then released with a success, exactly as a real client that walked away
// mid-settlement would produce.
func TestPendingReal_realCloseAfterSettlementStopsTheBillingHandoff(t *testing.T) {
	t.Parallel()

	barrier := newPendingLifetimeSettlementBarrier()
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:          true,
		settleRequestHook: barrier.settle,
		events:            pendingRealFinishEvents(),
	})
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")
	capture := pendingRealInstallBilling(t, fixture)

	pendingRealReceiveLeadingLifecycle(t, fixture.stream)
	recvDone := pendingRealRecvAsync(fixture.stream)
	barrier.awaitEntryOrRecvDone(t, recvDone)

	// The real Close withdraws the expected publication and closes its deferred
	// observer before competing for the attempt terminal this winner owns, so the
	// observer Finish is the ordering barrier.
	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.stream.Close() }()
	select {
	case <-observer.finished():
	case <-time.After(pendingLifetimeBarrierWait):
		t.Fatal("the real Close must close the deferred observer while the winning request owner settles")
	}
	barrier.unblock()

	require.NoError(t, <-closeDone, "a Close that wins the publication window must still succeed")
	got := <-recvDone
	requirePendingPublicationCloseWon(t, got.err,
		"a real Close after a successful private stage must withdraw the publication")
	assert.Empty(t, capture.completed(),
		"a real Close after settlement must never admit a NORMAL-COMPLETED billing handoff; sealed=%+v",
		capture.sealed())
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a withdrawn publication must never become deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn publication must leave no queued event")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a withdrawn publication must leave no retained candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn publication must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_exactLostClaimWithdrawsTheExpectedPublication pins the claim
// boundary EXACTLY, on the seam itself: the real accepted-normal staging call
// with a real prepared candidate whose claim was already withdrawn by a real
// Close. There is no enclosing terminal winner here, so the result is the
// boundary's own verdict and nothing else can mask it.
func TestPendingReal_exactLostClaimWithdrawsTheExpectedPublication(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPendingRealStream(t, pendingRealOptions{events: pendingRealFinishEvents()})
	observer := pendingOpenObserver(t, fixture.attempt)
	prep, err := fixture.pipe.preparePendingCompletion(ctx, fixture.stream.facts, fixture.attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the real preparation must really own a candidate")

	hooks := recvFinishAuthorityInput(ctx, fixture.stream, fixture.attempt, prep.prepared, true, nil)
	require.NotNil(t, hooks.acceptedNormal, "an expected candidate must install the accepted-normal staging seam")

	// A real Close closes the publication window and disposes the expected
	// candidate before the claim is ever attempted.
	require.NoError(t, fixture.stream.Close())

	claimErr := fixture.term.stagePendingAcceptedNormal(ctx, hooks, fixture.attempt, fixture.pipe,
		aipapi.Event{}, false)
	require.ErrorIs(t, claimErr, errPendingPublicationWithdrawn,
		"a candidate whose claim was withdrawn must report the withdrawal, never a successful publication")
	assert.False(t, fixture.pipe.pendingPublicationAccepted(),
		"a lost claim must leave no live reservation")
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a lost claim must never make a publication deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a lost claim must leave no queued event")
	assert.Equal(t, 1, observer.finishCount(),
		"a lost claim must not reopen or re-close the observer the real Close already finished")
}

// TestPendingReal_closeAfterClaimBeforeReserveWithdrawsThePublication pins the
// OTHER exact boundary: the claim already happened, so the publication is
// reserved-by-claim but not yet reserved-for-delivery, and a real Close in that
// window must still withdraw instead of letting the returning callback recreate
// private state behind it.
//
// The window is opened by wrapping the staging seam in the test only. No
// production hook is added: the wrap runs at the entry of the accepted-normal
// staging callback, which is strictly AFTER takePendingPublication consumed the
// claim and strictly BEFORE reservePendingPublication records the reservation.
func TestPendingReal_closeAfterClaimBeforeReserveWithdrawsThePublication(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPendingRealStream(t, pendingRealOptions{events: pendingRealFinishEvents()})
	observer := pendingOpenObserver(t, fixture.attempt)
	prep, err := fixture.pipe.preparePendingCompletion(ctx, fixture.stream.facts, fixture.attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared, "the real preparation must really own a candidate")

	hooks := recvFinishAuthorityInput(ctx, fixture.stream, fixture.attempt, prep.prepared, true, nil)
	original := hooks.acceptedNormal
	require.NotNil(t, original, "an expected candidate must install the accepted-normal staging seam")
	entered := make(chan struct{})
	release := make(chan struct{})
	var wrappedOnce sync.Once
	hooks.acceptedNormal = func(ownerCtx context.Context, usage aipapi.Event, usageOK bool) error {
		wrappedOnce.Do(func() { close(entered) })
		<-release
		return original(ownerCtx, usage, usageOK)
	}

	staged := make(chan error, 1)
	go func() {
		staged <- fixture.term.stagePendingAcceptedNormal(ctx, hooks, fixture.attempt, fixture.pipe,
			aipapi.Event{}, false)
	}()
	<-entered
	require.True(t, fixture.pipe.pendingPublicationAccepted(),
		"the claim must really have been consumed before the window is opened")
	stagedBatch, reserved, deferred, activated := pendingLifetimeState(fixture.pipe)
	assert.False(t, stagedBatch, "nothing may be staged before the reservation")
	assert.True(t, reserved, "the publication is claimed once the claim flag is set")
	assert.False(t, deferred,
		"the delivery reservation and its deferred observer finish are not recorded yet")
	assert.False(t, activated, "a claimed-but-unreserved publication is not deliverable")

	// No enclosing terminal winner is blocked here, so a real Close runs to
	// completion and disposes the claimed candidate before the callback returns.
	require.NoError(t, fixture.stream.Close())
	close(release)

	require.ErrorIs(t, <-staged, errPendingPublicationWithdrawn,
		"a publication withdrawn between its claim and its reservation must report the withdrawal")
	stagedBatch, reserved, deferred, activated = pendingLifetimeState(fixture.pipe)
	assert.False(t, stagedBatch, "a withdrawn publication must not install a batch behind the Close")
	assert.False(t, reserved, "a withdrawn publication must not keep the claim")
	assert.False(t, deferred, "a withdrawn publication must not record a deferred observer finish")
	assert.False(t, activated, "a withdrawn publication must never become deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn publication must leave no queued event")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a withdrawn publication must not leave a retained candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"the real Close already closed the deferred observer, and the returning callback must not re-close it")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
}

// TestPendingReal_ordinaryFinishDefersClientEffectsUntilQueueDelivery pins the
// ordinary-dispatch half of the delivery-only contract: the raw accepted finish is
// a CANDIDATE until it is physically queued, so while the publication preflight is
// blocked the attempt's client accounting and the recovery policy's client
// observation must not have run, and only the physical delivery may stamp them.
//
// This is a real Recv barrier: the mandatory recorder blocks inside the ordinary
// dispatch's own staging preflight, reached on the THIRD receive because the first
// two deliver the backend's leading lifecycle frames.
//
// The policy therefore still reports pre-output at the barrier, and that is the
// production truth rather than a weak assertion: streamrecovery.Policy has no
// production caller for ObserveBackendEvent, so the client observation this
// candidate defers is the ONLY seam through which this policy could ever learn
// that the response finished. The client-only proof that a delivered finish turns
// the same policy into a pass-through lives in
// TestPendingLifetime_physicalDeliveryRunsTheOrdinaryClientEventEffectsOnce, whose
// policy is installed on a directly staged private queue.
func TestPendingReal_ordinaryFinishDefersClientEffectsUntilQueueDelivery(t *testing.T) {
	t.Parallel()

	policy := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	recorder := newPendingLifetimeBlockingRecorder(aipapi.EventTextDelta)
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:       true,
		recorder:       recorder,
		mandatory:      true,
		recoveryPolicy: policy,
		events:         pendingRealFinishEvents(),
	})
	attempt := fixture.attempt
	// A two-phase test-only clock: everything observed while the batch is still a
	// candidate is stamped T1, and the physical finish delivery is stamped T2. A
	// client finish effect that ran during staging would leave T1 here.
	clock := &pendingLifetimeClock{}
	clock.set(pendingLifetimeStageTime)
	fixture.pipe.now = clock.now

	pendingRealReceiveLeadingLifecycle(t, fixture.stream)
	recvDone := pendingRealRecvAsync(fixture.stream)
	recorder.awaitEntryOrRecvDone(t, recvDone)

	// The raw finish has been accepted as a candidate, but nothing from it has
	// been queued yet, so no client-side effect may have run.
	preflight := attempt.accountingSnapshot()
	assert.True(t, preflight.RemoteCompletedAt.IsZero(),
		"a staged-but-undelivered finish must not be accounted as a client finish")
	assert.True(t, preflight.ProxyCompletedAt.IsZero(),
		"a staged-but-undelivered finish must not complete the proxy")
	assert.Equal(t, streamrecovery.DecisionRecoverPreOutput,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"a staged-but-undelivered finish must not report client output to the recovery policy")
	staged, _, _, _ := pendingLifetimeState(fixture.pipe)
	assert.False(t, staged, "the fixture must be blocked BEFORE the batch is installed")
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a blocked preflight must never make the publication deliverable")

	recorder.unblock()
	// The unblocked receive returns the FIRST private event of the publication,
	// never the accepted finish, so the finish effects are only provable after
	// the drain is continued through the finish itself.
	first := <-recvDone
	require.NoError(t, first.err, "an accepted preflight must release the private batch; err=%v", first.err)
	require.NotEqual(t, aipapi.EventResponseFinished, first.ev.Kind,
		"the accepted finish must stay queued behind the published result")

	clock.set(pendingLifetimeDeliveryTime)
	var released []aipapi.Event
	for range 8 {
		ev, recvErr := fixture.stream.Recv(context.Background())
		if recvErr != nil {
			require.ErrorIs(t, recvErr, io.EOF,
				"a fully delivered publication ends the stream at EOF; err=%v", recvErr)
			break
		}
		released = append(released, ev)
		if ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}

	// The whole batch, including the accepted finish, is now physically delivered,
	// so the client-side effects ran exactly once through the real queue.
	require.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished),
		"the accepted finish must be delivered exactly once through the real queue; released=%v",
		pendingLifetimeLabels(released, ""))
	accounting := attempt.accountingSnapshot()
	assert.Equal(t, pendingLifetimeDeliveryTime, accounting.RemoteCompletedAt,
		"the client finish must be stamped at the physical delivery, never while staging")
	assert.Equal(t, pendingLifetimeDeliveryTime, accounting.ProxyCompletedAt,
		"the proxy completion must be stamped at the physical delivery, never while staging")
	assert.Equal(t, streamrecovery.DecisionPassThrough,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"the physically delivered finish must be reported as a finished response")
	assert.Equal(t, 1, fixture.observer.countOf(aipapi.EventResponseFinished),
		"the accepted finish must be delivered exactly once; observed=%v", fixture.observer.observedKinds())
	assert.Equal(t, 1, fixture.observer.finishCount(),
		"the observer must be finished exactly once")

	// The end of the stream is ONE separate receive, and it must not repeat the
	// client finish effects the delivery already ran.
	_, recvErr := fixture.stream.Recv(context.Background())
	require.ErrorIs(t, recvErr, io.EOF, "a fully delivered publication ends the stream at EOF; err=%v", recvErr)
	after := attempt.accountingSnapshot()
	assert.Equal(t, accounting.RemoteCompletedAt, after.RemoteCompletedAt,
		"the end of the stream must not re-run the client finish accounting")
	assert.Equal(t, accounting.ProxyCompletedAt, after.ProxyCompletedAt,
		"the end of the stream must not re-run the proxy completion")
}

// TestPendingReal_rejectedPreflightRunsNoClientFinishEffect pins the failure half of
// the same contract: a preflight the real ordinary dispatch REJECTS must leave no
// client finish behavior at all, because nothing from that candidate was delivered.
func TestPendingReal_rejectedPreflightRunsNoClientFinishEffect(t *testing.T) {
	t.Parallel()

	policy := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	recorder := newPendingLifetimeRejectingRecorder(aipapi.EventTextDelta, errPendingLifetimeBoom)
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:       true,
		recorder:       recorder,
		mandatory:      true,
		recoveryPolicy: policy,
		events:         pendingRealFinishEvents(),
	})
	attempt := fixture.attempt
	clock := &pendingLifetimeClock{}
	clock.set(pendingLifetimeStageTime)
	fixture.pipe.now = clock.now

	pendingRealReceiveLeadingLifecycle(t, fixture.stream)
	recvDone := pendingRealRecvAsync(fixture.stream)
	recorder.awaitEntryOrRecvDone(t, recvDone)
	preflight := attempt.accountingSnapshot()
	assert.True(t, preflight.RemoteCompletedAt.IsZero(),
		"a rejected preflight must not account a client finish")
	assert.True(t, preflight.ProxyCompletedAt.IsZero(),
		"a rejected preflight must not complete the proxy")
	recorder.unblock()

	got := <-recvDone
	require.ErrorIs(t, got.err, errPendingLifetimeBoom,
		"a rejected preflight must surface through the real receive loop")
	assert.Zero(t, pendingRealCountText([]aipapi.Event{got.ev}, pendingRealResult),
		"a rejected preflight must deliver no client result")
	rejected := attempt.accountingSnapshot()
	assert.True(t, rejected.RemoteCompletedAt.IsZero(),
		"a rejected preflight must never run a client finish effect")
	assert.True(t, rejected.ProxyCompletedAt.IsZero(),
		"a rejected preflight must never complete the proxy")
	assert.Equal(t, streamrecovery.DecisionRecoverPreOutput,
		policy.DecideEOF(errPendingLifetimeBoom, time.Unix(1, 0)).Kind,
		"a rejected preflight must never report client output to the recovery policy")
	assert.False(t, fixture.pipe.pendingPublicationActive(),
		"a rejected preflight must never make the publication deliverable")
	_, queued := fixture.pipe.pendingPublicationHead()
	assert.False(t, queued, "a rejected preflight must leave no queued event")
	assert.Nil(t, fixture.pipe.pendingPreparedSnapshot(),
		"a rejected preflight must leave no retained candidate")
	assert.Equal(t, 1, fixture.observer.finishCount(),
		"a rejected preflight must close the deferred observer conservatively exactly once")
	assert.Equal(t, response.OutcomeFailed, fixture.observer.lastOutcome(),
		"a rejected preflight must finish the observer conservatively, never successfully")
}

// TestPendingReal_samePointerFrozenIdentityMismatchStopsTheDrain pins the frozen
// identity half of the delivery fence on the REAL receive stream: the retained
// candidate's frozen B-leg identity must still match the live attempt, so a
// candidate whose recorded identity no longer matches the attempt it points at can
// never drain again.
//
// A same-owner mismatch is a PERMANENT withdrawal, never a pause. The captured
// candidate pointer and its ORIGINAL origin pointer are the owner's own facts, so
// the owner — and only the owner — clears the candidate, its queue, and its
// reservation, and closes the deferred observer conservatively exactly once.
// Restoring the identity afterwards must not resurrect anything.
//
// The mismatch is arranged on the REAL Recv path and ordered strictly BETWEEN two
// receives by the first private delivery itself, so the supposedly frozen value is
// never written while another goroutine can read it. Foreign-caller protection is
// pinned separately, where no replacement has been published.
func TestPendingReal_samePointerFrozenIdentityMismatchStopsTheDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer: true,
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
		result: "identity answer",
	})
	pipe, attempt, observer := fixture.pipe, fixture.attempt, fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real deferred observer")

	// The two leading lifecycle frames are ordinary, so the first PRIVATE event is
	// the third real receive. That delivery is the barrier ordering the mutation.
	var released []aipapi.Event
	for range 2 {
		ev, recvErr := fixture.stream.Recv(ctx)
		require.NoError(t, recvErr, "the leading lifecycle frames must be delivered; err=%v", recvErr)
		released = append(released, ev)
	}
	first, recvErr := fixture.stream.Recv(ctx)
	require.NoError(t, recvErr, "the first private event must be delivered; err=%v", recvErr)
	require.NotEqual(t, aipapi.EventKind(""), first.Kind)
	released = append(released, first)
	require.Same(t, attempt, pipe.pendingPublicationOriginSnapshot(),
		"the drained publication must still be owned by its original attempt")
	require.True(t, pipe.pendingPublicationActive(),
		"the first private event must come from the activated drain")

	// The barrier above ordered this mutation strictly between two receives, so the
	// frozen identity changes only while no other goroutine can read it. The attempt
	// POINTER is deliberately unchanged: only the recorded B-leg identity stops
	// matching, which is exactly the same-owner withdrawal this case pins.
	frozen := attempt.bleg
	attempt.bleg = b2bua.BLegRecord{ALegID: frozen.ALegID, BLegID: "bleg-pending-forged", Seq: frozen.Seq}

	// Zero further private releases: the remainder is withdrawn, never delivered.
	var withheld []aipapi.Event
	for range 8 {
		next, drainErr := fixture.stream.Recv(ctx)
		if drainErr != nil {
			break
		}
		if next.Kind != "" {
			withheld = append(withheld, next)
		}
	}
	assert.NotContains(t, pendingLifetimeLabels(withheld, ""), string(aipapi.EventResponseFinished),
		"a same-pointer frozen-identity mismatch must never release the accepted finish; delivered earlier=%v",
		pendingLifetimeLabels(released, ""))
	assert.Zero(t, pendingRealCountText(withheld, "identity answer"),
		"a same-pointer frozen-identity mismatch must never release the retained result text")
	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"a same-owner frozen-identity mismatch must permanently dispose the retained candidate")
	_, queued := pipe.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn candidate must leave no queued event")
	staged, reserved, _, activated := pendingLifetimeState(pipe)
	assert.False(t, staged, "a withdrawn candidate must leave no staged batch")
	assert.False(t, reserved, "a withdrawn candidate must release its reservation")
	assert.False(t, activated, "a withdrawn candidate must never become deliverable again")
	assert.Equal(t, 1, observer.finishCount(),
		"a withdrawn candidate must close its deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn candidate must finish its observer conservatively, never successfully")

	// The real identity is restored, and that must NOT resurrect the publication:
	// a withdrawal this owner already performed is permanent.
	attempt.bleg = frozen
	var revived []aipapi.Event
	for range 8 {
		next, reviveErr := fixture.stream.Recv(ctx)
		if reviveErr != nil {
			break
		}
		if next.Kind != "" {
			revived = append(revived, next)
		}
	}
	assert.NotContains(t, pendingLifetimeLabels(revived, ""), string(aipapi.EventResponseFinished),
		"restoring the identity must never resurrect a permanently withdrawn finish")
	assert.Zero(t, pendingRealCountText(revived, "identity answer"),
		"restoring the identity must never re-release the withdrawn result text")
	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"restoring the identity must never reinstall the withdrawn candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"a permanently withdrawn publication must never reopen or re-close its observer")
}

// pendingLifetimeBlockingRecorder is the mandatory secure-session recorder. It
// blocks on a channel barrier inside the publication preflight, so a case can
// hold the accepted publication open while a withdrawal or a cancellation wins.
// A recorder whose blockErr is set also REJECTS the batch it blocked on, which is
// how a case drives a refused preflight on the real ordinary dispatch path.
type pendingLifetimeBlockingRecorder struct {
	entered   chan struct{}
	release   chan struct{}
	blockKind aipapi.EventKind
	blockErr  error
	once      sync.Once
}

func newPendingLifetimeBlockingRecorder(blockKind aipapi.EventKind) *pendingLifetimeBlockingRecorder {
	return &pendingLifetimeBlockingRecorder{
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
		blockKind: blockKind,
	}
}

// newPendingLifetimeRejectingRecorder blocks on the given kind and then fails it,
// so the real ordinary dispatch path runs a preflight that never succeeds.
func newPendingLifetimeRejectingRecorder(blockKind aipapi.EventKind, err error) *pendingLifetimeBlockingRecorder {
	recorder := newPendingLifetimeBlockingRecorder(blockKind)
	recorder.blockErr = err
	return recorder
}

func (r *pendingLifetimeBlockingRecorder) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r *pendingLifetimeBlockingRecorder) RecordPostHookStreamEvent(_ context.Context, in secureapp.StreamEventRecordInput) error {
	kind := aipapi.EventKind(in.EventKind)
	if r.blockKind != "" && kind == r.blockKind {
		r.once.Do(func() { close(r.entered) })
		<-r.release
		if r.blockErr != nil {
			return r.blockErr
		}
	}
	return nil
}

// awaitEntry waits for the recorded preflight with the same bound these cases
// already used. A case that owns its receive as a channel uses
// awaitEntryOrRecvDone instead, so a returned receive fails instead of waiting.
func (r *pendingLifetimeBlockingRecorder) awaitEntry(t *testing.T) {
	t.Helper()
	select {
	case <-r.entered:
	case <-time.After(pendingRouteGuard):
		t.Fatal("the publication preflight was never reached")
	}
}

// awaitEntryOrRecvDone waits for the recorded preflight and fails promptly when
// the receive under test returned first, so a case can never read an unheld
// barrier as a held one.
func (r *pendingLifetimeBlockingRecorder) awaitEntryOrRecvDone(t *testing.T, recvDone <-chan pendingRecvOutcome) {
	t.Helper()
	select {
	case <-r.entered:
	case got := <-recvDone:
		t.Fatalf("the receive finished before the publication preflight was reached: ev=%q err=%v", got.ev.Kind, got.err)
	case <-time.After(pendingRouteGuard):
		t.Fatal("the publication preflight was never reached")
	}
}

func (r *pendingLifetimeBlockingRecorder) unblock() { close(r.release) }

// TestPendingReal_closeWhileRecorderBlockedNeverRecreatesThePublication pins the
// external-preflight race: a real Close that wins while the mandatory recorder is
// blocked must finish the already-deferred observer conservatively exactly once,
// must never let the returning callback re-stage the withdrawn batch, and must
// leave no deliverable queue.
func TestPendingReal_closeWhileRecorderBlockedNeverRecreatesThePublication(t *testing.T) {
	t.Parallel()

	recorder := newPendingLifetimeBlockingRecorder(aipapi.EventTextDelta)
	fixture := newPendingRealStream(t, pendingRealOptions{
		observer:  true,
		recorder:  recorder,
		mandatory: true,
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventResponseFinished},
		},
	})
	pipe := fixture.pipe
	observer := fixture.observer
	require.NotNil(t, observer, "the fixture must carry a real observer")

	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = fixture.drain(t)
	}()
	recorder.awaitEntry(t)

	closeErr := make(chan error, 1)
	go func() { closeErr <- fixture.stream.Close() }()
	// The observer Finish is the deterministic barrier: a Close that wins while
	// the preflight is blocked must close the already-deferred observer itself, so
	// waiting for that Finish orders the withdrawal strictly before the release.
	select {
	case <-observer.finished():
	case <-time.After(pendingLifetimeBarrierWait):
		t.Error("Close must close the already-deferred observer while the publication preflight is blocked")
	}
	recorder.unblock()

	require.NoError(t, <-closeErr, "Close must succeed while the publication preflight is blocked")
	<-recvDone

	assert.Equal(t, 1, observer.finishCount(),
		"a Close that wins during the preflight must close the deferred observer exactly once")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a withdrawn publication must finish the observer conservatively, never successfully")
	assert.Nil(t, pipe.pendingPreparedSnapshot(),
		"a withdrawn publication must not leave a retained candidate")
	staged, reserved, _, activated := pendingLifetimeState(pipe)
	assert.False(t, staged, "the returning callback must not re-stage a withdrawn publication")
	assert.False(t, reserved, "a withdrawn publication must not keep its reservation")
	assert.False(t, activated, "a withdrawn publication must never become deliverable")
	_, queued := pipe.pendingPublicationHead()
	assert.False(t, queued, "a withdrawn publication must leave no queued event")
}

// TestPendingDrain_cancelledCallerDuringBlockedPreflightStagesNothing pins the
// post-external fence of staging: a client cancellation that wins while the
// mandatory recorder is blocked must be re-checked after that external work, so
// the returning callback installs no batch and no deliverable queue.
func TestPendingDrain_cancelledCallerDuringBlockedPreflightStagesNothing(t *testing.T) {
	t.Parallel()

	recorder := newPendingLifetimeBlockingRecorder(aipapi.EventTextDelta)
	p := newResponsePipeline()
	p.secureSessionRecorder = recorder
	p.secureRecordingMandatory = true
	attempt := pendingDrainAttempt(t, "blocked answer")
	observer := pendingOpenObserver(t, attempt)
	prep, err := p.preparePendingCompletion(context.Background(), pendingDrainFacts(), attempt,
		aipapi.Event{Kind: aipapi.EventResponseFinished}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, prep.prepared)
	claimPublication(t, p, prep.prepared)

	callerCtx, cancel := context.WithCancel(context.Background())
	terminal := newTurnTerminal()
	staged := make(chan error, 1)
	go func() {
		staged <- terminal.stagePendingCompletion(context.Background(), attempt, p,
			pendingDrainFacts().terminalFacts(),
			pendingPublication{prepared: prep.prepared, facts: pendingDrainFacts(), callerCtx: callerCtx})
	}()
	recorder.awaitEntry(t)
	// The cancellation lands while the recorder is still blocked, so the callback's
	// re-check after the external preflight is what must observe it. The ordering is
	// established by the barrier, never by a sleep.
	cancel()
	recorder.unblock()
	// A cancellation that wins while the preflight is blocked stops the owning
	// effects, so the callback reports the withdrawal rather than a success that
	// would settle customer authority and hand off billing for a batch that is
	// never released.
	require.ErrorIs(t, <-staged, errPendingPublicationWithdrawn,
		"a cancellation that wins during the preflight must stop the owning effects")

	stagedBatch, reserved, _, activated := pendingLifetimeState(p)
	assert.False(t, stagedBatch, "a cancelled caller must install no batch after the preflight")
	assert.False(t, reserved, "a cancelled caller must not keep its reservation")
	assert.False(t, activated, "a cancelled caller must never make a publication deliverable")
	_, queued := p.pendingPublicationHead()
	assert.False(t, queued, "a cancelled caller must leave no queued event")
	assert.Nil(t, p.pendingPreparedSnapshot(), "a cancelled caller must leave no retained candidate")
	assert.Equal(t, 1, observer.finishCount(),
		"the deferred observer must be finished exactly once, conservatively")
	assert.Equal(t, response.OutcomeFailed, observer.lastOutcome(),
		"a cancelled publication finishes the observer conservatively")
}

// The real gate holds the lifecycle too: these failures belong to the staged
// private sequence, rather than an unrelated raw backend response_started.
func TestPendingReal_privatePreflightFailureMatrix(t *testing.T) {
	for _, source := range []string{"recorder", "observer"} {
		kinds := []aipapi.EventKind{aipapi.EventTextDelta, aipapi.EventUsageDelta, aipapi.EventResponseFinished}
		if source == "recorder" {
			kinds = append([]aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventMessageStarted}, kinds...)
		}
		for _, kind := range kinds {
			t.Run(source+"/"+string(kind), func(t *testing.T) {
				recorder := &pendingFailingRecorder{}
				if source == "recorder" {
					recorder.failOn = kind
				}
				settlements := 0
				traffic := &pendingCustomerTraffic{}
				customer := &pendingLifetimeUsageProbe{}
				policy := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
				f := newPendingRealStream(t, pendingRealOptions{
					events: pendingRealFinishEvents(), observer: true, recorder: recorder, mandatory: true,
					gates:           []completion.Gate{&pendingRealGateFence{}},
					trafficObserver: traffic, usageObserver: customer, recoveryPolicy: policy,
					collector:         &pendingRealCollector{callCount: accountingapp.CountResult{InputTokens: 13, TotalTokens: 13}, outCount: accountingapp.CountResult{OutputTokens: 5, TotalTokens: 18}},
					settleRequestHook: func(context.Context, []metering.Fact) error { settlements++; return nil },
				})
				if source == "observer" {
					f.observer.failOn = kind
				}
				bills := pendingRealInstallBilling(t, f)
				released, err := f.drain(t)
				if source == "recorder" {
					require.ErrorIs(t, err, errPendingRecorderMandatory)
				} else {
					require.ErrorIs(t, err, errPendingLifetimeBoom)
				}
				assert.Empty(t, traffic.ptc)
				assert.Empty(t, customer.seen())
				assert.Equal(t, streamrecovery.DecisionRecoverPreOutput, policy.DecideEOF(err, time.Unix(1, 0)).Kind)
				assert.Empty(t, released, "no staged private frame can be released before the complete preflight succeeds")
				assert.Zero(t, settlements)
				assert.Empty(t, bills.completed())
				assert.Equal(t, 1, f.observer.finishCount())
				assert.Equal(t, response.OutcomeFailed, f.observer.lastOutcome())
				pending3CAssertWithdrawn(t, f)
				account := f.attempt.accountingSnapshot()
				assert.True(t, account.RemoteCompletedAt.IsZero())
				assert.True(t, account.ProxyCompletedAt.IsZero())
				if source == "recorder" {
					require.Contains(t, recorder.recordedKinds(), kind)
				} else {
					require.Contains(t, f.observer.observedKinds(), kind)
				}
			})
		}
	}
}

func pending3CAssertWithdrawn(t *testing.T, f *pendingRealStream) {
	t.Helper()
	assert.Nil(t, f.pipe.pendingPreparedSnapshot())
	_, queued := f.pipe.pendingPublicationHead()
	assert.False(t, queued)
	staged, reserved, _, active := pendingLifetimeState(f.pipe)
	assert.False(t, staged)
	assert.False(t, reserved)
	assert.False(t, active)
}

func TestPendingReal_privateSettlementAndBillingFailures(t *testing.T) {
	for _, source := range []string{"request_settlement", "billing_handoff"} {
		t.Run(source, func(t *testing.T) {
			opts := pendingRealOptions{events: pendingRealFinishEvents(), observer: true}
			if source == "request_settlement" {
				opts.settleRequestErr = errPendingLifetimeBoom
			}
			f := newPendingRealStream(t, opts)
			bills := pendingRealInstallBilling(t, f)
			if source == "billing_handoff" {
				sink := f.term.appendBillingCall
				f.term.appendBillingCall = func(ctx context.Context, record usageevidence.CallUsageRecord) error {
					require.NoError(t, sink(ctx, record))
					return errPendingLifetimeBoom
				}
			}
			released, err := f.drain(t)
			require.ErrorIs(t, err, errPendingLifetimeBoom)
			assert.Zero(t, pendingRealCountText(released, pendingRealResult))
			assert.Zero(t, pendingRealCountKind(released, aipapi.EventResponseFinished))
			if source == "request_settlement" {
				assert.Empty(t, bills.completed())
			} else {
				records := bills.completed()
				require.Len(t, records, 1, "the already-admitted handoff receives an actual sealed completed record even though persistence rejects it")
				assert.NotEmpty(t, records[0].Fingerprint)
			}
			pending3CAssertWithdrawn(t, f)
			assert.Equal(t, 1, f.observer.finishCount())
			assert.Equal(t, response.OutcomeFailed, f.observer.lastOutcome())
		})
	}
}

func TestPendingReal_withdrawBetweenActualPrivateFrames(t *testing.T) {
	for _, after := range []aipapi.EventKind{aipapi.EventTextDelta, aipapi.EventUsageDelta} {
		for _, port := range []string{"Close", "caller", "shared_aleg"} {
			t.Run(string(after)+"/"+port, func(t *testing.T) {
				f := newPendingRealStream(t, pendingRealOptions{
					events: pendingRealFinishEvents(), observer: true,
					collector: &pendingRealCollector{callCount: accountingapp.CountResult{InputTokens: 13, TotalTokens: 13}, outCount: accountingapp.CountResult{OutputTokens: 5, TotalTokens: 18}},
				})
				bills := pendingRealInstallBilling(t, f)
				if port == "shared_aleg" {
					owner, _ := pendingLifetimeALegTerminal(t, f.stream.facts.aLegID)
					f.term.aLegEndAuthority = owner.aLegEndAuthority
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				for range 12 {
					ev, err := f.stream.Recv(ctx)
					require.NoError(t, err)
					if ev.Kind == after {
						break
					}
				}
				require.True(t, f.pipe.pendingPublicationActive(), "the withdrawal must occur during the actual active private drain")
				head, ok := f.pipe.pendingPublicationHead()
				require.True(t, ok)
				if after == aipapi.EventTextDelta {
					require.Equal(t, aipapi.EventUsageDelta, head.Kind)
				} else {
					require.Equal(t, aipapi.EventResponseFinished, head.Kind)
				}
				// Normal settlement and billing were legitimately admitted before frame1.
				require.Len(t, bills.completed(), 1)
				switch port {
				case "Close":
					require.NoError(t, f.stream.Close())
				case "caller":
					cancel()
				case "shared_aleg":
					require.NoError(t, f.term.cancelALeg(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}))
					require.Error(t, f.term.aLegErr())
				}
				ev, err := f.stream.Recv(ctx)
				require.Error(t, err, "ev=%+v alegErr=%v", ev, f.term.aLegErr())
				assert.Empty(t, ev.Kind, "no remaining private frame may escape the withdrawal fence")
				pending3CAssertWithdrawn(t, f)
				assert.Equal(t, 1, f.observer.finishCount())
				assert.Equal(t, response.OutcomeFailed, f.observer.lastOutcome())
				assert.True(t, f.attempt.accountingSnapshot().RemoteCompletedAt.IsZero())
				assert.Len(t, bills.completed(), 1, "withdrawal does not roll back previously admitted billing")
			})
		}
	}
}

func TestPendingReal_observerAbsentAcceptedPublication(t *testing.T) {
	f := newPendingRealStream(t, pendingRealOptions{events: pendingRealFinishEvents()})
	require.Nil(t, f.observer, "no observer participant is installed")
	bills := pendingRealInstallBilling(t, f)
	released, err := f.drain(t)
	require.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 1, pendingRealCountText(released, pendingRealResult))
	assert.Equal(t, 1, pendingRealCountKind(released, aipapi.EventResponseFinished))
	require.Len(t, bills.completed(), 1)
	pending3CAssertWithdrawn(t, f)
}

// This recorder waits for the real winning request owner's cleanup deadline,
// rather than manufacturing a timeout or shortening the production budget.
type pending3CDeadlineRecorder struct {
	deadline time.Time
	expired  error
	entered  chan struct{}
}

func (*pending3CDeadlineRecorder) RecordClientTurnAfterGate(context.Context, secureapp.ClientTurnRecordInput) error {
	return nil
}

func (r *pending3CDeadlineRecorder) RecordPostHookStreamEvent(ctx context.Context, in secureapp.StreamEventRecordInput) error {
	if aipapi.EventKind(in.EventKind) != aipapi.EventTextDelta {
		return nil
	}
	var ok bool
	r.deadline, ok = ctx.Deadline()
	close(r.entered)
	if !ok {
		return errors.New("mandatory recorder did not inherit bounded cleanup context")
	}
	<-ctx.Done()
	r.expired = ctx.Err()
	return r.expired
}

func TestPendingReal_mandatoryRecorderInheritsActualCleanupDeadline(t *testing.T) {
	r := &pending3CDeadlineRecorder{entered: make(chan struct{})}
	settlements := 0
	f := newPendingRealStream(t, pendingRealOptions{
		events: pendingRealFinishEvents(), observer: true, recorder: r, mandatory: true,
		settleRequestHook: func(context.Context, []metering.Fact) error { settlements++; return nil },
	})
	bills := pendingRealInstallBilling(t, f)
	pendingRealReceiveLeadingLifecycle(t, f.stream)
	done := pendingRealRecvAsync(f.stream)
	select {
	case <-r.entered:
	case got := <-done:
		t.Fatalf("receive returned before mandatory recorder: %v", got.err)
	case <-time.After(pendingLifetimeBarrierWait):
		t.Fatal("mandatory recorder not entered")
	}
	require.False(t, r.deadline.IsZero())
	select {
	case got := <-done:
		require.ErrorIs(t, got.err, context.DeadlineExceeded)
		assert.Empty(t, got.ev.Kind)
	case <-time.After(time.Until(r.deadline) + pendingLifetimeBarrierWait):
		t.Fatal("recorder exceeded its actual cleanup deadline")
	}
	assert.ErrorIs(t, r.expired, context.DeadlineExceeded)
	assert.Zero(t, settlements)
	assert.Empty(t, bills.completed())
	pending3CAssertWithdrawn(t, f)
	assert.Equal(t, 1, f.observer.finishCount())
	assert.Equal(t, response.OutcomeFailed, f.observer.lastOutcome())
}

type pending3CUsageBarrier struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *pending3CUsageBarrier) OnUsage(_ context.Context, ev usage.Event) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return nil
}

func TestPendingReal_withdrawWhileActualObserverOrReleaseTailBlocked(t *testing.T) {
	for _, seam := range []string{"final_observer", "release_tail"} {
		for _, port := range []string{"Close", "caller", "shared_aleg"} {
			t.Run(seam+"/"+port, func(t *testing.T) {
				b := &pending3CUsageBarrier{entered: make(chan struct{}), release: make(chan struct{})}
				t.Cleanup(func() {
					select {
					case <-b.release:
					default:
						close(b.release)
					}
				})
				opts := pendingRealOptions{
					events: pendingRealFinishEvents(), observer: true,
					collector: &pendingRealCollector{callCount: accountingapp.CountResult{InputTokens: 13, TotalTokens: 13}, outCount: accountingapp.CountResult{OutputTokens: 5, TotalTokens: 18}},
				}
				if seam == "release_tail" {
					opts.usageObserver = b
				}
				f := newPendingRealStream(t, opts)
				if port == "shared_aleg" {
					owner, _ := pendingLifetimeALegTerminal(t, f.stream.facts.aLegID)
					f.term.aLegEndAuthority = owner.aLegEndAuthority
				}
				bills := pendingRealInstallBilling(t, f)
				if seam == "final_observer" {
					f.observer.onObserve = func(_ context.Context, ev aipapi.Event) {
						if ev.Kind == aipapi.EventTextDelta {
							close(b.entered)
							<-b.release
						}
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pendingRealReceiveLeadingLifecycle(t, f.stream)
				if seam == "release_tail" {
					ev, err := f.stream.Recv(ctx)
					require.NoError(t, err)
					require.Equal(t, aipapi.EventTextDelta, ev.Kind)
				}
				done := pendingRealRecvAsyncOn(ctx, f.stream)
				select {
				case <-b.entered:
				case got := <-done:
					t.Fatalf("receive returned before %s barrier: %v", seam, got.err)
				case <-time.After(pendingLifetimeBarrierWait):
					t.Fatal("callback not entered")
				}
				var closeDone chan error
				switch port {
				case "caller":
					cancel()
				case "shared_aleg":
					require.NoError(t, f.term.cancelALeg(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}))
					require.Error(t, f.term.aLegErr())
				case "Close":
					closeDone = make(chan error, 1)
					go func() { closeDone <- f.stream.Close() }()
					// Close publishes its window withdrawal before waiting for an in-flight observer.
					deadline := time.NewTimer(pendingLifetimeBarrierWait)
					defer deadline.Stop()
					for !f.stream.attempt.publicationIsClosed() {
						select {
						case <-deadline.C:
							t.Fatal("Close did not withdraw publication")
						default:
							runtime.Gosched()
						}
					}
				}
				close(b.release)
				got := <-done
				require.Error(t, got.err)
				assert.Empty(t, got.ev.Kind)
				if closeDone != nil {
					require.NoError(t, <-closeDone)
				}
				pending3CAssertWithdrawn(t, f)
				assert.Equal(t, 1, f.observer.finishCount())
				assert.Equal(t, response.OutcomeFailed, f.observer.lastOutcome())
				if seam == "final_observer" {
					assert.Empty(t, bills.completed())
				} else {
					require.Len(t, bills.completed(), 1)
				}
			})
		}
	}
}
