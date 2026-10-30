package conversationview

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// BootstrapOutcome is bounded completion evidence, not a diagnostic payload.
type BootstrapOutcome string

const (
	BootstrapMatched          BootstrapOutcome = "matched"
	BootstrapNoMatch          BootstrapOutcome = "no_match"
	BootstrapAmbiguousSkip    BootstrapOutcome = "ambiguous_skip"
	BootstrapPreexistingSkip  BootstrapOutcome = "preexisting_skip"
	MaxBootstrapModelBytes                     = 128
	MaxBootstrapProducerBytes                  = 64
)

var (
	ErrBootstrapUnsupported = errors.New("conversationview: bootstrap authority unsupported")
	ErrBootstrapInvalid     = errors.New("conversationview: invalid bootstrap decision")
	ErrBootstrapCollision   = errors.New("conversationview: bootstrap overlay collision")
	ErrBootstrapDecision    = errors.New("conversationview: bootstrap decision failed")
	ErrBootstrapStorage     = errors.New("conversationview: bootstrap storage failed")
)

// BootstrapDecision is a synchronous, bounded, pure decision. Overlays are in
// configuration order. Model is logical diagnostic evidence, never a metric key.
type BootstrapDecision struct {
	Outcome  BootstrapOutcome
	Model    string
	Overlays []PutSteeringRequest
}

type BootstrapCompletion struct {
	Outcome      BootstrapOutcome
	MatchedCount int
	Model        string
}

// BootstrapResult contains mutation evidence only on a newly committed batch.
// A reused completion never fabricates mutations for observers.
type BootstrapResult struct {
	Completion BootstrapCompletion
	Reused     bool
	Mutations  []SteeringState
}

type BootstrapDecide func() (BootstrapDecision, error)

// BootstrapStore is an optional internal capability. The trusted producer ID
// is generation-independent and owns the prefix producerID + ".".
type BootstrapStore interface {
	BootstrapSteering(context.Context, string, string, BootstrapDecide) (BootstrapResult, error)
}

func validateBootstrapScope(aLegID, producerID string) error {
	if validateALegID(aLegID) != nil || len(producerID) > MaxBootstrapProducerBytes || steering.OverlayID(producerID).Validate() != nil {
		return ErrBootstrapInvalid
	}
	return nil
}

func bootstrapDecision(hasAllocated bool, decide BootstrapDecide) (BootstrapDecision, error) {
	if hasAllocated {
		return BootstrapDecision{Outcome: BootstrapPreexistingSkip}, nil
	}
	if decide == nil {
		return BootstrapDecision{}, ErrBootstrapInvalid
	}
	decision, err := decide()
	if err != nil {
		return BootstrapDecision{}, ErrBootstrapDecision
	}
	switch decision.Outcome {
	case BootstrapMatched:
		if len(decision.Overlays) == 0 {
			return BootstrapDecision{}, ErrBootstrapInvalid
		}
	case BootstrapNoMatch, BootstrapAmbiguousSkip:
		if len(decision.Overlays) != 0 {
			return BootstrapDecision{}, ErrBootstrapInvalid
		}
	default:
		return BootstrapDecision{}, ErrBootstrapInvalid
	}
	return decision, nil
}

func bootstrapCompletion(decision BootstrapDecision) BootstrapCompletion {
	model := decision.Model
	if !utf8.ValidString(model) || strings.ContainsRune(model, 0) {
		model = ""
	}
	if len(model) > MaxBootstrapModelBytes {
		model = model[:MaxBootstrapModelBytes]
		for !utf8.ValidString(model) {
			model = model[:len(model)-1]
		}
	}
	return BootstrapCompletion{Outcome: decision.Outcome, MatchedCount: len(decision.Overlays), Model: model}
}

// stageBootstrap owns all candidate state. Failure cannot consume live slots or
// revisions. Durable callers use the same validation with their integer ceiling.
func stageBootstrap(live *legView, producerID string, decision BootstrapDecision, now time.Time, ceiling uint64) (*legView, []SteeringState, error) {
	if len(decision.Overlays) > MaxActiveOverlays {
		return nil, nil, ErrSteeringLimitExceeded
	}
	staged := *live
	staged.steering = make(map[string]*SteeringOverlay, len(live.steering)+len(decision.Overlays))
	count, total := 0, 0
	for id, ov := range live.steering {
		// Reject orphaned producer state even if today's batch is empty/different.
		if strings.HasPrefix(id, producerID+".") {
			return nil, nil, ErrBootstrapCollision
		}
		staged.steering[id] = ov
		if ov.Active {
			count++
			total += len(ov.Message.Text)
		}
	}
	mutations := make([]SteeringState, 0, len(decision.Overlays))
	for _, req := range decision.Overlays {
		if req.Validate() != nil || steering.OverlayID(req.OverlayID).Validate() != nil || steering.ReasonCode(req.Reason).Validate() != nil || (steering.Message{Role: req.Message.Role, Text: req.Message.Text}).Validate() != nil || !strings.HasPrefix(req.OverlayID, producerID+".") {
			return nil, nil, ErrBootstrapInvalid
		}
		if _, exists := staged.steering[req.OverlayID]; exists {
			return nil, nil, ErrBootstrapCollision
		}
		if RegistersNewAfterMessageAnchor(req, false, true) {
			if _, excluded := live.tags[req.Placement.Anchor.Identity]; excluded {
				return nil, nil, ErrSteeringAnchorExcluded
			}
		}
		count++
		total += len(req.Message.Text)
		if count > MaxActiveOverlays || total > MaxTotalSteeringBytes {
			return nil, nil, ErrSteeringLimitExceeded
		}
		if staged.nextSlot >= ceiling || staged.revision >= ceiling {
			return nil, nil, ErrRevisionExhausted
		}
		ov := (SteeringOverlay{OverlayID: req.OverlayID, Revision: 1, SlotOrdinal: staged.nextSlot, Active: true, Message: req.Message, Placement: req.Placement, AnchorMissingPolicy: req.AnchorMissingPolicy, Reason: req.Reason, CreatedAt: now, UpdatedAt: now}).Clone()
		staged.steering[req.OverlayID] = &ov
		staged.nextSlot++
		staged.revision++
		mutations = append(mutations, SteeringState{OverlayID: ov.OverlayID, Revision: 1, SlotOrdinal: ov.SlotOrdinal, Active: true, StateRevision: staged.revision, CacheDiscontinuityKind: CacheDiscontinuityCreate, CacheDiscontinuityPlacement: ov.Placement.Kind})
	}
	return &staged, mutations, nil
}
