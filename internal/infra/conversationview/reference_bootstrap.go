package conversationview

import (
	"context"
	"maps"
	"math"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
)

// AuthorizedReferenceStore pairs a memory conversation store with its actual
// continuity allocation authority. Construction does not create authoritative legs.
// The process must bind the existing ALegRetirementObserver to DeleteALeg; the
// adapter does not install a second retirement observer or cleanup authority.
type AuthorizedReferenceStore struct {
	*ReferenceStore
	authority b2bua.BLegAllocationAuthority
}

var _ BootstrapStore = (*AuthorizedReferenceStore)(nil)

func NewAuthorizedReferenceStore(store *ReferenceStore, authority b2bua.BLegAllocationAuthority) *AuthorizedReferenceStore {
	return &AuthorizedReferenceStore{ReferenceStore: store, authority: authority}
}

func (s *AuthorizedReferenceStore) BootstrapSteering(ctx context.Context, aLegID, producerID string, decide BootstrapDecide) (BootstrapResult, error) {
	if err := ctx.Err(); err != nil {
		return BootstrapResult{}, err
	}
	if err := validateBootstrapScope(aLegID, producerID); err != nil {
		return BootstrapResult{}, err
	}
	if s == nil || s.ReferenceStore == nil || s.authority == nil {
		return BootstrapResult{}, ErrBootstrapUnsupported
	}
	var out BootstrapResult
	err := s.authority.WithBLegAllocationAuthority(ctx, aLegID, func(hasAllocated bool) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		live, err := s.getLeg(aLegID)
		if err != nil {
			return err
		}
		if completion, ok := live.bootstrap[producerID]; ok {
			out = BootstrapResult{Completion: completion, Reused: true}
			return nil
		}
		decision, err := bootstrapDecision(hasAllocated, decide)
		if err != nil {
			return err
		}
		staged, mutations, err := stageBootstrap(live, producerID, decision, s.now().UTC(), math.MaxUint64)
		if err != nil {
			return err
		}
		staged.bootstrap = maps.Clone(live.bootstrap)
		if staged.bootstrap == nil {
			staged.bootstrap = make(map[string]BootstrapCompletion)
		}
		completion := bootstrapCompletion(decision)
		staged.bootstrap[producerID] = completion
		if err := ctx.Err(); err != nil {
			return err
		}
		s.legs[aLegID] = staged
		out = BootstrapResult{Completion: completion, Mutations: mutations}
		return nil
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	return out, nil
}
