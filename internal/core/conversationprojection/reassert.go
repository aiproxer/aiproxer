package conversationprojection

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Reassert restores the frozen conversation view after late attempt/candidate/interleaved transforms.
// It is pure and uses no store read. It removes any reintroduced never_backend messages and
// ensures each active overlay in snap is present exactly once at its frozen placement
// (rebuild via Project). It handles late calls that are already based on the projected baseline
// by stripping existing projection-owned copies (identified via placement-aware provenance) before
// re-projecting, to avoid duplicate injection. Identity collisions are handled by using
// exact injected trajectory positions (InjectedTrajectoryIndex / InjectedItemIndex / InjectedLegacy*)
// plus anchor/slot order, preserving legitimate same role/text messages. If ambiguity remains
// (multiple identical copies indistinguishable), it fails closed rather than silently deleting.
// FilteredBaseline is the frozen filtered call (without never_backend, without steering) carried
// from early projection; when provided it is used to distinguish legitimate user collisions from
// duplicate steering. If empty, it is derived on the fly.
//
// Transform-stable carry-forward. A backend-only content rewrite performed after early
// projection can change the identity the frozen after-message anchor names even though the
// complete-message trajectory is unchanged, because MessageIdentityOf hashes normalized
// semantic content. When that happens Reassert proves a one-to-one structural lineage from the
// supplied frozen FilteredBaseline to the cleaned call and re-injects the overlay at the same
// logical boundary. A stored occurrence may still resolve at a different duplicate
// after the rewrite; the proven boundary takes precedence only in that conflict or
// when exact resolution fails. Matching boundaries keep stored evidence.
// The proof never reads the conversation-view store, and identity equality is
// deliberately not part of it: a rewrite of the anchored payload always drifts that identity, so
// an identity-equality proof would refuse every legitimate rewrite, while identity is also weaker
// than the structural proof because the normalized atom drops several stable identities. Any
// insertion, deletion, reorder, role/kind change, stable-ID or reference change, or
// authority/partition change refuses the proof outright; in those cases the stored anchor still
// drives exact resolution and the configured anchor-missing policy remains authoritative.
// MessageIdentityOf, the stored MessageAnchor{Identity, Occurrence}, persistence, overlay
// lifecycle, and the anchor-missing/fallback policy are all unchanged by this path.
func Reassert(call aipapi.Call, snap Snapshot, provenance []OverlayProvenance, filteredBaseline aipapi.Call) (aipapi.Call, *ProjectionEvidence, error) {
	// Derive provenance if empty but snap has steering (fallback).
	if len(provenance) == 0 && len(snap.Steering) > 0 {
		// Projection reads its input and returns a fresh call, so the frozen
		// baseline can be projected without cloning it first.
		var base aipapi.Call
		if filteredBaseline.Messages != nil || filteredBaseline.Instructions != nil || filteredBaseline.Items != nil {
			base = filteredBaseline
		} else {
			fb, err := FilterNeverBackend(call, snap)
			if err != nil {
				return aipapi.Call{}, nil, err
			}
			base = fb
		}
		_, ev2, err := Project(base, snap)
		if err != nil {
			return aipapi.Call{}, nil, err
		}
		provenance = ev2.Provenance
	}
	if len(snap.NeverBackend) == 0 && len(provenance) == 0 {
		if err := call.Validate(); err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
		}
		return call, &ProjectionEvidence{}, nil
	}
	cleaned := aipapi.CloneCall(call)
	// Every pass below compares the same two trajectories against the same
	// provisioning identities. Derive each element's identity once here and carry
	// the table through the passes; re-deriving it per pass is what made this one
	// Reassert hash the whole history up to seven times.
	cleanedIn := identifyCall(cleaned)
	// If filteredBaseline not provided, derive it for collision disambiguation.
	hasFiltered := false
	// hasFrozenBaseline records that the caller supplied the request-local frozen
	// filtered baseline it froze at early projection. Only that pre-rewrite evidence
	// can support transform-stable carry-forward; a baseline derived on the fly from
	// the already-rewritten call carries no lineage information at all.
	hasFrozenBaseline := len(filteredBaseline.Instructions) > 0 || len(filteredBaseline.Messages) > 0 || len(filteredBaseline.Items) > 0
	var frozenIn projectionInput
	if hasFrozenBaseline {
		// The baseline is only read: no pass mutates it, and Project/A are pure.
		frozenIn = identifyCall(filteredBaseline)
		hasFiltered = true
	} else {
		fb, err := FilterNeverBackend(call, snap)
		if err == nil {
			frozenIn = identifyCall(fb)
			hasFiltered = true
		}
	}
	// Build maps for quick lookup.
	neverSet := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		neverSet[t.Identity] = struct{}{}
	}
	// Fail-closed on same-slice, same-identity collision with extra duplicates
	// that would require ambiguous position heuristic. If a prov identity collides
	// with legitimate (filteredCount>0) and late contains more than the expected
	// total (filtered + prov), the position heuristic could select legitimate
	// instead of the owned copy. Fail closed rather than risk user data loss.
	if hasFiltered && len(provenance) > 0 {
		if err := failClosedOnAmbiguousSameSliceCollision(cleanedIn, frozenIn, provenance); err != nil {
			return aipapi.Call{}, nil, err
		}
	}
	// Placement-aware removal of provenance-owned steering.
	if len(provenance) > 0 {
		cleanedIn = reassertRemoveProvenance(cleanedIn, frozenIn, provenance, hasFiltered)
	}
	// Remove reintroduced never_backend (any remaining with that identity).
	if len(neverSet) > 0 {
		cleanedIn = filterByNeverSet(cleanedIn, neverSet)
	}
	// After expected provenance removal, check for remaining duplicates vs legitimate user.
	// For each distinct prov identity, compare remaining count in cleaned vs expected user count in filtered.
	if hasFiltered && len(provenance) > 0 {
		if err := checkRemainingProvIdentities(cleanedIn, frozenIn, provenance); err != nil {
			return aipapi.Call{}, nil, err
		}
		// Remove any remaining extra prov identities that are duplicates (beyond legitimate user count).
		cleanedIn = removeExtraProvIdentities(cleanedIn, frozenIn, provenance)
	}
	cleanedIn.applyTo(&cleaned)
	// Only the request-local frozen baseline can prove lineage: a baseline derived
	// above from the already-rewritten call carries no pre-rewrite evidence.
	lineageBase := projectionInput{}
	if hasFrozenBaseline {
		lineageBase = frozenIn
	}
	// Transform-stable carry-forward supplies a request-local anchor when the stored
	// anchor stopped resolving or resolves at a different duplicate after a rewrite.
	// Exact resolution remains authoritative without proof or on a matching boundary.
	// The carried anchor exists
	// only when the FROZEN filtered baseline and the cleaned trajectory prove a
	// one-to-one structural lineage, and it is never derived from a conversation-view
	// store read. never_backend filtering and projection-owned overlay removal above
	// are already applied and are not bypassed here.
	out, ev, err := projectWithLineage(cleaned, cleanedIn, snap, carriedAnchorsForReassert(snap, provenance, lineageBase, cleanedIn))
	if err != nil {
		return aipapi.Call{}, nil, err
	}
	if err := out.Validate(); err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
	}
	return out, ev, nil
}

func carriedAnchorsForReassert(snap Snapshot, provenance []OverlayProvenance, frozenIn, cleanedIn projectionInput) carriedAnchors {
	if !frozenIn.traversable() || !cleanedIn.traversable() {
		return nil
	}
	return deriveCarriedAnchors(snap, provenance, frozenIn, cleanedIn)
}

// VerifyAdaptationPreservesProjection checks that adaptedCall still satisfies full projection:
// - never_backend absent
// - steering exact count per identity, SlotOrdinal order, and same resolved placement (stable prefix / after anchor)
// - moved-tail/reordered steering rejects.
func VerifyAdaptationPreservesProjection(reasserted, adapted aipapi.Call, snap Snapshot, provenance []OverlayProvenance) error {
	_, err := VerifyAdaptationPreservesProjectionWithPositions(reasserted, adapted, snap, provenance)
	return err
}

// VerifyAdaptationPreservesProjectionWithPositions performs the same checks as
// VerifyAdaptationPreservesProjection and returns the adapted trajectory
// positions of the injected entries. Callers that need positional evidence use
// this single pass instead of verifying and then rescanning the call.
func VerifyAdaptationPreservesProjectionWithPositions(reasserted, adapted aipapi.Call, snap Snapshot, provenance []OverlayProvenance) ([]InjectedPosition, error) {
	if len(provenance) == 0 && len(snap.NeverBackend) == 0 && len(snap.Steering) == 0 {
		return nil, nil
	}
	// Check never_backend absent
	neverSet := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		neverSet[t.Identity] = struct{}{}
	}
	if adapted.HasItemAuthority() {
		for _, it := range adapted.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				continue
			}
			if _, ok := neverSet[id]; ok {
				return nil, fmt.Errorf("%w: adapted still contains never_backend %s", ErrProjectionFailed, id)
			}
		}
	} else {
		for _, m := range adapted.Instructions {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := neverSet[id]; ok {
					return nil, fmt.Errorf("%w: adapted instructions still contains never_backend %s", ErrProjectionFailed, id)
				}
			}
		}
		for _, m := range adapted.Messages {
			if id, err := MessageIdentityOf(m); err == nil {
				if _, ok := neverSet[id]; ok {
					return nil, fmt.Errorf("%w: adapted messages still contains never_backend %s", ErrProjectionFailed, id)
				}
			}
		}
	}
	return AdaptedInjectedPositions(reasserted, adapted, provenance)
}
