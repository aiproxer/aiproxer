package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// postTerminalWorker is the explicit public post-terminal worker seam for
// the external binding. It drains durably-acknowledged terminal envelopes
// from the binding terminal in acknowledgement order and rates
// evidence-bearing ones with the custom per-submission/credit rater.
// Observation-less closures are acknowledged-but-unrateable and are skipped
// without inventing evidence. Draining is explicit and idempotent: repeated
// drains consume nothing new.
type postTerminalWorker struct {
	rater    economics.Rater
	terminal *terminalLog
	scope    billing.BindingScope

	mu         sync.Mutex
	cursor     uint64
	valuations []economics.Valuation
	rated      int
	skipped    int
}

// newPostTerminalWorker binds the seam to one assembled offer fixture. The
// terminal must be the same port instance wired into the host binding so
// host-delivered envelopes flow through it.
func newPostTerminalWorker(fix *OfferFixture) *postTerminalWorker {
	return &postTerminalWorker{
		rater:    fix.Rater,
		terminal: fix.Terminal,
		scope:    offerScope(),
	}
}

// Drain consumes newly acknowledged envelopes in order. It returns the
// first rating error without advancing past the failed envelope.
func (w *postTerminalWorker) Drain() error {
	envs, revisions := w.terminal.drainSince(w.cursorValue())
	for i, env := range envs {
		if len(env.Observations) == 0 {
			w.countSkipped()
			w.setCursor(revisions[i])
			continue
		}
		valuation, err := w.rater.Rate(context.Background(), ratingInputForEnvelope(w.scope, env))
		if err != nil {
			return fmt.Errorf("post-terminal rate: %w", err)
		}
		w.countRated(valuation)
		w.setCursor(revisions[i])
	}
	return nil
}

// Counts reports drained outcomes: rated valuations and skipped
// observation-less closures.
func (w *postTerminalWorker) Counts() (rated, skipped int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rated, w.skipped
}

// Valuations returns independent copies of rated valuations in drain order.
func (w *postTerminalWorker) Valuations() []economics.Valuation {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]economics.Valuation, len(w.valuations))
	for i, valuation := range w.valuations {
		out[i] = valuation.Clone()
	}
	return out
}

func (w *postTerminalWorker) cursorValue() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cursor
}

func (w *postTerminalWorker) setCursor(revision uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if revision > w.cursor {
		w.cursor = revision
	}
}

func (w *postTerminalWorker) countRated(valuation economics.Valuation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.valuations = append(w.valuations, valuation)
	w.rated++
}

func (w *postTerminalWorker) countSkipped() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.skipped++
}

// ratingInputForEnvelope builds the customer-policy rating input for the
// evidence carried by one terminal envelope.
func ratingInputForEnvelope(scope billing.BindingScope, env billing.TerminalEnvelope) economics.RatingInput {
	return economics.RatingInput{
		Version:     1,
		Perspective: metering.PerspectiveCustomer,
		Basis:       economics.BasisCustomerPolicy,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBillingCall,
			StoreID:       env.StoreID,
			BillingCallID: env.Subject.BillingCallID,
		},
		Observations: append([]metering.Observation(nil), env.Observations...),
		Rater: economics.RatingSnapshotRef{
			VersionRef: economics.VersionRef{ID: "rater-external", Version: "v1"},
			RaterID:    perSubmissionRaterID,
		},
		RaterContent:         testContentRefPtr("rater"),
		Policy:               scope.Policy,
		PolicyContent:        testContentRefPtr("policy"),
		QualifierSnapshotRef: testContentRefPtr("qualifiers"),
	}
}
