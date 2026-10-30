package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// PromptCacheCommittedTurn carries attempt-local prompt-cache observations and
// committed tool evidence using canonical/SDK DTOs only (design §6, Requirement 6.3).
type PromptCacheCommittedTurn struct {
	ALegID              string
	BLegID              string
	CommittedSuccessful bool
	ToolEvents          []aipapi.ToolEvent
	Observations        []promptcache.Observation
	BackendInstanceID   string
	CanonicalModelID    string
	Controller          promptcache.Controller
}

// PromptCacheMaintenance is the narrow core runtime consumer port for lifecycle
// facts that only core authoritatively emits (Requirement 6.3, design §6).
type PromptCacheMaintenance interface {
	BeginRealTurn(aLegID string)
	EndSession(aLegID string)
	ArmCommittedTurn(turn PromptCacheCommittedTurn)
}

func promptCacheObservationSource(stream aipapi.ManagedEventStream) promptcache.ObservationSource {
	source, _ := stream.(promptcache.ObservationSource)
	return source
}

type backendPromptCacheController struct {
	backend execbackend.Backend
}

func promptCacheControllerFor(backend execbackend.Backend) promptcache.Controller {
	// A renewable target is only safe when both halves of its lifecycle seam are
	// present. A release-only or renew-only backend remains observation-only;
	// otherwise the scheduler could arm a handle it cannot control or forget.
	if backend.RenewPromptCache == nil || backend.ReleasePromptCache == nil {
		return nil
	}
	return backendPromptCacheController{backend: backend}
}

func (c backendPromptCacheController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	return c.backend.RenewPromptCache(ctx, req)
}

func (c backendPromptCacheController) Release(ctx context.Context, req promptcache.ReleaseRequest) error {
	return c.backend.ReleasePromptCache(ctx, req)
}

// commitSuccessfulTurn is the single response-to-maintenance boundary. The
// terminal handlers only announce that the response_finished event committed;
// this method snapshots attempt-local prompt-cache observations and logical
// response tool evidence exactly once.
func (p *responsePipeline) commitSuccessfulTurn(facts recvTurnFacts, attempt *attemptSession, committed bool) {
	if p == nil || p.promptCacheMaintenance == nil || attempt == nil {
		return
	}
	p.keepwarmArmOnce.Do(func() {
		source, controller := attempt.promptCacheSideband()
		if source == nil || controller == nil {
			return
		}
		observations := source.DrainPromptCacheObservations()
		if len(observations) == 0 {
			return
		}
		// The observation sideband is host-only but producer-declared. Validate
		// each observation against the trusted attempt lineage before the
		// keep-warm owner verifies eligibility: an empty identity field is
		// filled from the attempt, while a non-empty conflicting A-leg/B-leg/
		// backend claim is rejected (and only the exact transport pseudonym the
		// host supplied is mapped to the real B-leg). Target/handle/evidence
		// identity stays untouched and a foreign observation is never rewritten
		// into trusted facts.
		lineage := execbackend.PromptCacheObservationLineage{
			ALegID:                facts.aLegID,
			BLegID:                attempt.bleg.BLegID,
			BackendInstanceID:     attempt.cand.Primary.Backend,
			ConnectorLegPseudonym: execbackend.ConnectorLegPseudonym(attempt.cand.Key, attempt.cand.Primary.String()),
		}
		bound := make([]promptcache.Observation, 0, len(observations))
		for _, observation := range observations {
			reconciled, err := execbackend.ReconcilePromptCacheObservationLineage(observation, lineage)
			if err != nil {
				if p.log != nil {
					// Stable conflict category only: producer-declared identity
					// strings are untrusted and may carry credential text, so
					// neither the error message nor its fields are logged raw.
					reason := "lineage_conflict"
					if class, ok := execbackend.PromptCacheLineageConflictClassOf(err); ok {
						reason = string(class) + "_conflict"
					}
					p.log.Warn("prompt cache observation lineage rejected", "reason", reason)
				}
				continue
			}
			bound = append(bound, reconciled)
		}
		if len(bound) == 0 {
			return
		}
		p.promptCacheMaintenance.ArmCommittedTurn(PromptCacheCommittedTurn{
			ALegID:              facts.aLegID,
			BLegID:              attempt.bleg.BLegID,
			CommittedSuccessful: committed,
			ToolEvents:          p.committedToolEventsSnapshot(),
			Observations:        bound,
			BackendInstanceID:   attempt.cand.Primary.Backend,
			CanonicalModelID:    attempt.cand.Primary.Model,
			Controller:          controller,
		})
	})
}
