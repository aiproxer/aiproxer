package featurehost

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// bindBootstrapAuthority certifies the actual continuity engine, never inferring
// allocation history from attempt records or from an empty conversation view.
// The existing process retirement binding remains the sole memory cleanup owner.
func (r *Runtime) bindBootstrapAuthority(store b2bua.Store) {
	var underlying any = store
	for depth := 0; depth < 16 && underlying != nil; depth++ {
		// Only the established pass-through decorator is certified. A custom
		// Unwrap/DB claim cannot prove that allocation uses that same authority.
		if u, ok := underlying.(*observers.B2BUAStoreDecorator); ok {
			underlying = u.Unwrap()
		} else {
			break
		}
	}
	view := r.conversationStore
	if wrapped, ok := view.(*autoRegisteringBootstrapStore); ok {
		view = wrapped.inner
	}
	if wrapped, ok := view.(*autoRegisteringConversationStore); ok {
		view = wrapped.inner
	}
	switch continuity := underlying.(type) {
	case *b2bua.MemoryStore:
		ref, ok := view.(*conversationview.ReferenceStore)
		authority, supported := store.(b2bua.BLegAllocationAuthority)
		if continuity != nil && ok && supported {
			r.conversationStore = wrapConversationStore(conversationview.NewAuthorizedReferenceStore(ref, authority))
			r.bootstrapAuthority = true
		}
	case *bunstore.Store:
		conv, ok := view.(*conversationview.BunStore)
		r.bootstrapAuthority = continuity != nil && ok && conv.DB() == continuity.DB()
	}
}

func (r *Runtime) modelSystemPrompt(in GenerationInput) (runtime.ConversationBootstrap, error) {
	for _, registration := range in.Registrations {
		if registration.Kind != aipsdk.PluginKindFeature || !registration.Enabled || (registration.ID != modelsystemprompt.ID && registration.FactoryKind != modelsystemprompt.ID) {
			continue
		}
		if r == nil || !r.bootstrapAuthority || r.ConversationReader() == nil {
			return nil, fmt.Errorf("featurehost: model-system-prompt requires paired conversation and continuity authority")
		}
		store, ok := r.conversationStore.(conversationview.BootstrapStore)
		if !ok {
			return nil, fmt.Errorf("featurehost: model-system-prompt requires atomic conversation store")
		}
		cfg, err := modelsystemprompt.DecodeConfig(registration.Config.Node)
		if err != nil {
			return nil, err
		}
		matcher, err := modelsystemprompt.New(cfg)
		if err != nil {
			return nil, err
		}
		observer := conversationview.SafeObserver(in.ConversationObserver)
		return func(ctx context.Context, aLegID string, _ *aipapi.Call, resolve func() (runtime.InitialModelIntent, error)) (string, error) {
			if r.logger != nil {
				r.logger.DebugContext(ctx, "conversation bootstrap", "result", "attempted")
			}
			result, err := store.BootstrapSteering(ctx, aLegID, modelsystemprompt.ID, func() (conversationview.BootstrapDecision, error) {
				intent, err := resolve()
				if err != nil {
					return conversationview.BootstrapDecision{}, err
				}
				if intent.Ambiguous {
					return conversationview.BootstrapDecision{Outcome: conversationview.BootstrapAmbiguousSkip}, nil
				}
				decision := conversationview.BootstrapDecision{Outcome: conversationview.BootstrapNoMatch, Model: intent.Model}
				for _, req := range matcher.Match(intent.Model) {
					decision.Overlays = append(decision.Overlays, conversationview.PutSteeringRequest{
						OverlayID:           string(req.OverlayID),
						Message:             conversationview.StoredMessageV1{Role: req.Message.Role, Text: req.Message.Text},
						Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
						AnchorMissingPolicy: conversationview.AnchorMissingPolicy(req.AnchorMissingPolicy),
						Reason:              conversationview.ReasonCode(req.Reason),
					})
				}
				if len(decision.Overlays) > 0 {
					decision.Outcome = conversationview.BootstrapMatched
				}
				return decision, nil
			})
			if err != nil {
				if r.logger != nil {
					r.logger.DebugContext(ctx, "conversation bootstrap", "result", "failed")
				}
				return "", fmt.Errorf("featurehost: model-system-prompt bootstrap: %w", err)
			}
			for _, mutation := range result.Mutations {
				observer.OnSteeringMutation(mutation.CacheDiscontinuityKind, mutation.CacheDiscontinuityPlacement)
			}
			if r.logger != nil {
				status := string(result.Completion.Outcome)
				if result.Reused {
					status = "reused"
				}
				r.logger.DebugContext(ctx, "conversation bootstrap", "result", status, "outcome", result.Completion.Outcome, "matched_count", result.Completion.MatchedCount, "logical_model", result.Completion.Model)
			}
			// Only a newly committed batch changed the effective instructions;
			// a reused, no-match, ambiguous or preexisting decision changed
			// nothing and records no cause.
			if len(result.Mutations) > 0 {
				return modelsystemprompt.CauseSystemPrompt, nil
			}
			return "", nil
		}, nil
	}
	return nil, nil
}
