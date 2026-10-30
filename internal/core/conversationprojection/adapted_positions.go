package conversationprojection

import (
	"fmt"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// InjectedPosition is one projection-owned steering emission resolved to its
// trajectory position in a call.
type InjectedPosition struct {
	OverlayID       string
	TrajectoryIndex int
}

// AdaptedInjectedPositions resolves every provenance entry to the trajectory
// position of its emission in adapted, in adapted trajectory order.
//
// Provenance describes positions in the call that produced it. A later authority
// adaptation (legacy messages <-> ordered items) can change the trajectory shape
// even though it preserves the projection, so pre-adaptation indexes must never
// be reused blindly. This resolves each entry's placement in adapted with the
// same anchor rules VerifyAdaptationPreservesProjection applies, and fails closed
// when an entry is missing, duplicated, reordered, or no longer at its resolved
// placement.
func AdaptedInjectedPositions(reasserted, adapted aipapi.Call, provenance []OverlayProvenance) ([]InjectedPosition, error) {
	if len(provenance) == 0 {
		return nil, nil
	}
	expectedPerID := make(map[MessageIdentity]int)
	for _, p := range provenance {
		expectedPerID[p.InjectedIdentity]++
	}
	type found struct {
		id      MessageIdentity
		trajIdx int
		isInstr bool
	}
	// Collect adapted steering occurrences in trajectory order.
	var adaptedSteering []found
	if adapted.HasItemAuthority() {
		for idx, it := range adapted.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				continue
			}
			if _, ok := expectedPerID[id]; ok {
				adaptedSteering = append(adaptedSteering, found{id: id, trajIdx: idx})
			}
		}
	} else {
		combined := append(append([]aipapi.Message(nil), adapted.Instructions...), adapted.Messages...)
		for idx, m := range combined {
			id, err := MessageIdentityOf(m)
			if err != nil {
				continue
			}
			if _, ok := expectedPerID[id]; ok {
				isInstr := idx < len(adapted.Instructions)
				adaptedSteering = append(adaptedSteering, found{id: id, trajIdx: idx, isInstr: isInstr})
			}
		}
	}
	// Check count per identity.
	countPerID := make(map[MessageIdentity]int)
	for _, f := range adaptedSteering {
		countPerID[f.id]++
	}
	for id, exp := range expectedPerID {
		if countPerID[id] != exp {
			return nil, fmt.Errorf("%w: steering identity %s expected %d occurrences after adaptation, got %d", ErrProjectionFailed, id, exp, countPerID[id])
		}
	}
	// Match each provenance entry to its adapted occurrence. Provenance is
	// ordered by pre-adaptation position; the adapted sequence preserves that
	// order, so both sort to the same pairing.
	sortedProv := append([]OverlayProvenance(nil), provenance...)
	sort.Slice(sortedProv, func(i, j int) bool {
		return sortedProv[i].InjectedTrajectoryIndex < sortedProv[j].InjectedTrajectoryIndex
	})
	sort.Slice(adaptedSteering, func(i, j int) bool { return adaptedSteering[i].trajIdx < adaptedSteering[j].trajIdx })
	if len(sortedProv) != len(adaptedSteering) {
		return nil, fmt.Errorf("%w: steering count mismatch after sort", ErrProjectionFailed)
	}
	out := make([]InjectedPosition, 0, len(sortedProv))
	for i, p := range sortedProv {
		f := adaptedSteering[i]
		if f.id != p.InjectedIdentity {
			return nil, fmt.Errorf("%w: steering order mismatch at position %d: expected %s got %s", ErrProjectionFailed, i, p.InjectedIdentity, f.id)
		}
		// Verify placement matches provenance ResolvedKind/Anchor.
		switch p.ResolvedKind {
		case PlacementStablePrefix:
			// Item authority keeps the stable prefix among leading items; the
			// verifier historically accepts any item position, so nothing
			// stricter is proven here. Legacy authority must keep it in the
			// instruction region.
			if !reasserted.HasItemAuthority() && !f.isInstr {
				return nil, fmt.Errorf("%w: stable_prefix steering should be in instructions, got messages", ErrProjectionFailed)
			}
		case PlacementAfterMessage:
			// Should be immediately after its resolved placement point: the anchor
			// itself, or - when the anchor message carries tool calls - the last
			// contiguous tool-result message answering those calls, so a call is
			// never split from its result by the injected copy.
			if p.ResolvedAnchor == nil {
				return nil, fmt.Errorf("%w: after_message missing anchor", ErrProjectionFailed)
			}
			// Find anchor index in adapted trajectory.
			var anchorIdx int
			var foundAnchor bool
			if adapted.HasItemAuthority() {
				anchorIdx, foundAnchor, _ = resolveAnchorInItems(adapted.Items, *p.ResolvedAnchor)
				if !foundAnchor && !reasserted.HasItemAuthority() {
					// A legacy assistant tool-call message projects to tool_call
					// items across authority adaptation, so its message identity
					// cannot resolve in item space. Map the anchor - resolved in
					// the reasserted call where it must exist - to its adapted
					// tool_call span via stable call IDs and check the same
					// result-side placement point there. Anything unmappable
					// keeps the fail-closed outcome below.
					if isInstr, idx, found, _ := resolveAnchorLegacy(reasserted.Instructions, reasserted.Messages, *p.ResolvedAnchor); found && !isInstr {
						anchorIdx, foundAnchor = adaptedToolAnchorPlacementIndex(adapted.Items, reasserted.Messages[idx])
					}
				}
			} else {
				isInstr, idx, found, _ := resolveAnchorLegacy(adapted.Instructions, adapted.Messages, *p.ResolvedAnchor)
				if found {
					if isInstr {
						anchorIdx = idx
					} else {
						anchorIdx = len(adapted.Instructions) + afterMessageToolResultIndex(adapted.Messages, idx)
					}
					foundAnchor = found
				}
			}
			if !foundAnchor {
				return nil, fmt.Errorf("%w: anchor %s not found in adapted call", ErrProjectionFailed, p.ResolvedAnchor.String())
			}
			// Steering should be at the placement point index+1 plus offset for
			// SlotOrdinal among overlays sharing the same anchor.
			if f.trajIdx != anchorIdx+1 {
				if f.trajIdx <= anchorIdx {
					return nil, fmt.Errorf("%w: steering %s not after its placement point %s (anchor %d, steering %d)", ErrProjectionFailed, p.OverlayID, p.ResolvedAnchor.String(), anchorIdx, f.trajIdx)
				}
				offset := 0
				for _, q := range sortedProv {
					if q.ResolvedKind == PlacementAfterMessage && q.ResolvedAnchor != nil && *q.ResolvedAnchor == *p.ResolvedAnchor && q.SlotOrdinal < p.SlotOrdinal {
						offset++
					}
				}
				expectedIdx := anchorIdx + 1 + offset
				if f.trajIdx != expectedIdx {
					return nil, fmt.Errorf("%w: steering %s placement mismatch: expected %d got %d (anchor %d offset %d)", ErrProjectionFailed, p.OverlayID, expectedIdx, f.trajIdx, anchorIdx, offset)
				}
			}
		}
		out = append(out, InjectedPosition{OverlayID: p.OverlayID, TrajectoryIndex: f.trajIdx})
	}
	return out, nil
}
