package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// InitialModelIntent is logical route intent, before any candidate/native binding.
type InitialModelIntent struct {
	Model     string
	Ambiguous bool
}

// ConversationBootstrap runs before the normal snapshot. resolve must only be
// invoked inside the store's undecided callback; reused decisions need no routing.
// accepted is the admitted call; producers must treat it as read-only.
//
// The returned label is the bounded, content-free structural cause the
// bootstrap recorded for this request when it changed the effective system or
// developer instructions, or "" when it changed nothing. The runtime carries
// the label opaquely onto the live call and never interprets it: the
// prompt-cache collapse diagnostic owns the vocabulary and ignores a label it
// does not know.
type ConversationBootstrap func(context.Context, string, *aipapi.Call, func() (InitialModelIntent, error)) (string, error)

func (e *Executor) initialModelIntent(raw string) (InitialModelIntent, error) {
	sel, err := routing.CompileSelector(raw, e.SelectorAliases, e.DefaultBackend)
	if err != nil {
		return InitialModelIntent{}, err
	}
	var model string
	var ambiguous bool
	add := func(p routing.Primary) {
		next := strings.TrimSpace(p.Model)
		if model == "" {
			model = next
		} else if next != model {
			ambiguous = true
		}
	}
	parallel := func(p *routing.Parallel) {
		for _, branch := range p.Branches {
			add(branch.Target)
		}
	}
	for _, alt := range sel.Alternatives {
		switch {
		case alt.Primary != nil:
			add(*alt.Primary)
		case alt.Parallel != nil:
			parallel(alt.Parallel)
		case alt.Weighted != nil:
			for _, branch := range alt.Weighted.Branches {
				if branch.Parallel != nil {
					parallel(branch.Parallel)
				} else {
					add(branch.Target)
				}
			}
		}
	}
	if model == "" {
		return InitialModelIntent{}, fmt.Errorf("executor: empty initial model intent")
	}
	if ambiguous {
		return InitialModelIntent{Ambiguous: true}, nil
	}
	return InitialModelIntent{Model: model}, nil
}

func (e *Executor) selectLocalAndBootstrap(ctx context.Context, ibt *identityBoundTurn, accepted *aipapi.Call) error {
	ctx = ibt.projectContext(ctx)
	selection, err := e.selectLocalTurn(ctx, *ibt.ingressCall, ibt.aLeg.ALegID, ibt.traceID)
	if err != nil {
		return err
	}
	ibt.localSelection = selection
	if selection.handler != nil || e.ConversationBootstrap == nil {
		return nil
	}
	selector := accepted.Route.Selector
	if ibt.routeAuth.active() {
		selector = ibt.routeAuth.State.Selector
	}
	cause, err := e.ConversationBootstrap(ctx, ibt.aLeg.ALegID, accepted, func() (InitialModelIntent, error) {
		return e.initialModelIntent(selector)
	})
	if err != nil {
		return err
	}
	// The request the bootstrap changed carries the cause it recorded, exactly
	// like a request mutation does: the label reaches the provider-bound call
	// the bootstrap's committed instructions were projected into.
	noteStructuralCause(accepted, cause)
	return nil
}
