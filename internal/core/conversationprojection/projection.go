package conversationprojection

import (
	"fmt"
	"math"
	"slices"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func boolPtr(b bool) *bool { v := b; return &v }
func intPtr(i int) *int    { v := i; return &v }

func checkedCapacity(parts ...int) (int, error) {
	total := 0
	for _, part := range parts {
		if part < 0 || total > math.MaxInt-part {
			return 0, ErrProjectionFailed
		}
		total += part
	}
	return total, nil
}

// FallbackEvidence records a deterministic stable-prefix fallback for a missing anchor.
type FallbackEvidence struct {
	OverlayID string        `json:"overlay_id"`
	Anchor    MessageAnchor `json:"anchor"`
}

// OverlayProvenance is deterministic request-local provenance for one injected overlay.
// It is derived solely from Snapshot + input call and is used by a later final-reassertion
// stage to recognize/rebuild/remove projection-owned steering instances without string heuristics.
// It does not affect MessageIdentityOf values or model-visible content.
// Injected* fields record the exact trajectory position of the injected copy for
// placement-aware reassertion without broad identity sweeps.
type OverlayProvenance struct {
	OverlayID        string          `json:"overlay_id"`
	Revision         uint64          `json:"revision"`
	SlotOrdinal      uint64          `json:"slot_ordinal"`
	ResolvedKind     PlacementKind   `json:"resolved_kind"`
	ResolvedAnchor   *MessageAnchor  `json:"resolved_anchor,omitempty"`
	InjectedIdentity MessageIdentity `json:"injected_identity"`
	// Exact injected position for placement-aware reassertion.
	InjectedAuthority           string `json:"injected_authority"`            // "item" or "legacy"
	InjectedItemIndex           int    `json:"injected_item_index,omitempty"` // valid when authority == "item"
	InjectedLegacyIsInstruction *bool  `json:"injected_legacy_is_instruction,omitempty"`
	InjectedLegacyIndex         *int   `json:"injected_legacy_index,omitempty"`
	InjectedTrajectoryIndex     int    `json:"injected_trajectory_index"`
}

// MatchesMessage reports whether msg is the projection-owned copy for this provenance entry
// by semantic identity comparison. It is pure and uses no string-marker heuristics.
func (p OverlayProvenance) MatchesMessage(msg aipapi.Message) bool {
	id, err := MessageIdentityOf(msg)
	if err != nil {
		return false
	}
	return id == p.InjectedIdentity
}

// MatchesItem reports whether item is the projection-owned copy for this provenance entry.
func (p OverlayProvenance) MatchesItem(item aipapi.Item) bool {
	if item.Kind != aipapi.ItemKindMessage {
		return false
	}
	id, err := ItemIdentityOf(item)
	if err != nil {
		return false
	}
	return id == p.InjectedIdentity
}

// ProjectionEvidence captures bounded diagnostics for a successful projection.
type ProjectionEvidence struct {
	FilteredCount int                 `json:"filtered_count"`
	InjectedCount int                 `json:"injected_count"`
	Fallbacks     []FallbackEvidence  `json:"fallbacks,omitempty"`
	Provenance    []OverlayProvenance `json:"provenance,omitempty"`
}

// Project derives the backend-effective call from call and snap.
// It is pure, deterministic and never mutates the input call.
// Exclusion is applied first, then steering injection; result is validated.
//
// Project is the early-projection authority: it always resolves persisted anchors
// by exact identity and always applies the configured anchor-missing policy. It
// never carries a placement forward across an identity drift; only the final
// reassertion seam may do that, and only through projectWithLineage.
func Project(call aipapi.Call, snap Snapshot) (aipapi.Call, *ProjectionEvidence, error) {
	// Derive every trajectory identity once for the whole projection. Exclusion,
	// anchor resolution and the carried-anchor pass all consult the same
	// trajectory, so one derivation per element serves the entire projection.
	return projectWithLineage(call, identifyCall(call), snap, nil)
}

// projectWithLineage is Project with an optional request-local set of carried
// anchors. A carried anchor is consulted ONLY for an after-message overlay whose
// stored anchor failed to resolve exactly, so exact identity-based resolution
// stays the normal path and the anchor-missing policy stays authoritative
// whenever lineage is unavailable or the carried anchor does not resolve either.
func projectWithLineage(call aipapi.Call, in projectionInput, snap Snapshot, carried carriedAnchors) (aipapi.Call, *ProjectionEvidence, error) {
	// in carries every trajectory identity the caller already derived, so the
	// exclusion, anchor-resolution and carried-anchor passes below look them up
	// instead of re-hashing the same trajectory three more times.
	if in.itemAuthority {
		return projectItems(call, in.items, snap, carried)
	}
	return projectLegacy(call, in.legacy, snap, carried)
}

// ResolveAfterIngressTailAnchor resolves the terminal forwardable user message
// in call (filtered by snap exclusions) to a MessageAnchor.
// It rejects if the terminal message is absent, not user, or not safe.
// It first establishes the concrete original terminal complete message boundary
// and rejects if that terminal's identity is in snapshot exclusions (never falls
// back to an earlier forwardable user).
func ResolveAfterIngressTailAnchor(call aipapi.Call, snap Snapshot) (MessageAnchor, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	if call.HasItemAuthority() {
		// Establish concrete terminal message boundary before filtering.
		termIdx := -1
		for i, item := range slices.Backward(call.Items) {
			if item.Kind == aipapi.ItemKindMessage {
				termIdx = i
				break
			}
		}
		if termIdx < 0 {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable message", ErrTerminalUserNotFound)
		}
		terminal := call.Items[termIdx]
		termID, err := ItemIdentityOf(terminal)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[termID]; excluded {
			return MessageAnchor{}, fmt.Errorf("%w: terminal message is never_backend", ErrTerminalUserNotFound)
		}
		if terminal.Role != aipapi.RoleUser {
			return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, terminal.Role)
		}
		// Derive the surviving (backend-effective) trajectory with the same
		// semantics as projection: excluded complete messages are removed and
		// item_reference entries targeting removed message IDs are dropped.
		// The FINAL surviving item must be the terminal complete user message;
		// any surviving non-message item after it makes the boundary unsafe.
		removedIDs := make(map[string]struct{})
		for _, it := range call.Items {
			if it.Kind != aipapi.ItemKindMessage {
				continue
			}
			id, err := ItemIdentityOf(it)
			if err != nil {
				return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
			}
			if _, excluded := exclusion[id]; excluded && it.ID != "" {
				removedIDs[it.ID] = struct{}{}
			}
		}
		lastSurvivorIdx := -1
		occ := uint32(0)
		for i, it := range call.Items {
			if it.Kind == aipapi.ItemKindMessage {
				id, err := ItemIdentityOf(it)
				if err != nil {
					return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
				}
				if _, excluded := exclusion[id]; excluded {
					continue
				}
				lastSurvivorIdx = i
				if id == termID {
					occ++
				}
				continue
			}
			if it.Kind == aipapi.ItemKindItemReference && it.Reference != nil {
				if _, removed := removedIDs[it.Reference.ID]; removed {
					continue
				}
			}
			lastSurvivorIdx = i
		}
		if lastSurvivorIdx < 0 {
			return MessageAnchor{}, fmt.Errorf("%w: no forwardable message", ErrTerminalUserNotFound)
		}
		if lastSurvivorIdx != termIdx {
			surviving := call.Items[lastSurvivorIdx]
			detail := string(surviving.Kind)
			if surviving.Kind == aipapi.ItemKindMessage {
				detail = string(surviving.Role)
			}
			return MessageAnchor{}, fmt.Errorf("%w: terminal %q item is not a safe user-message boundary", ErrTerminalNotUser, detail)
		}
		anchor := MessageAnchor{Identity: termID, Occurrence: occ}
		if err := anchor.Validate(); err != nil {
			return MessageAnchor{}, err
		}
		return anchor, nil
	}
	// Legacy authority: establish concrete terminal boundary over original trajectory.
	origCombined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	if len(origCombined) == 0 {
		return MessageAnchor{}, fmt.Errorf("%w: empty forwardable trajectory", ErrTerminalUserNotFound)
	}
	origLast := origCombined[len(origCombined)-1]
	origID, err := MessageIdentityOf(origLast)
	if err != nil {
		return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
	}
	if _, excluded := exclusion[origID]; excluded {
		return MessageAnchor{}, fmt.Errorf("%w: terminal message is never_backend", ErrTerminalUserNotFound)
	}
	if origLast.Role != aipapi.RoleUser {
		return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, origLast.Role)
	}
	var fwdInstr []aipapi.Message
	var fwdMsgs []aipapi.Message
	for _, m := range call.Instructions {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			continue
		}
		fwdInstr = append(fwdInstr, m)
	}
	for _, m := range call.Messages {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return MessageAnchor{}, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			continue
		}
		fwdMsgs = append(fwdMsgs, m)
	}
	// Combined trajectory for occurrence counting.
	combined := append(append([]aipapi.Message(nil), fwdInstr...), fwdMsgs...)
	if len(combined) == 0 {
		return MessageAnchor{}, fmt.Errorf("%w: empty forwardable trajectory", ErrTerminalUserNotFound)
	}
	// Check if any forwardable user exists.
	hasUser := false
	for _, m := range combined {
		if m.Role == aipapi.RoleUser {
			hasUser = true
			break
		}
	}
	if !hasUser {
		return MessageAnchor{}, fmt.Errorf("%w: no forwardable user message", ErrTerminalUserNotFound)
	}
	last := combined[len(combined)-1]
	if last.Role != aipapi.RoleUser {
		return MessageAnchor{}, fmt.Errorf("%w: terminal is %q", ErrTerminalNotUser, last.Role)
	}
	id, err := MessageIdentityOf(last)
	if err != nil {
		return MessageAnchor{}, err
	}
	var occ uint32
	for _, m := range combined {
		cid, _ := MessageIdentityOf(m)
		if cid == id {
			occ++
		}
	}
	anchor := MessageAnchor{Identity: id, Occurrence: occ}
	if err := anchor.Validate(); err != nil {
		return MessageAnchor{}, err
	}
	return anchor, nil
}

// ---------------------------------------------------------------------------
// item authority projection
// ---------------------------------------------------------------------------

func projectItems(call aipapi.Call, in itemTrajectory, snap Snapshot, carried carriedAnchors) (aipapi.Call, *ProjectionEvidence, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	// Phase 1: exclusion of complete messages. Identities come from the
	// trajectory table derived once by the caller, so this and every later pass
	// of the projection reuse one derivation per item instead of re-hashing it.
	filteredCount := 0
	removedIDs := make(map[string]struct{})
	kept := make([]bool, in.len())
	for i := range in.elems {
		it := &in.elems[i]
		if it.Kind != aipapi.ItemKindMessage {
			kept[i] = true
			continue
		}
		id, err := in.identity(i)
		if err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: invalid message item: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			filteredCount++
			if it.ID != "" {
				removedIDs[it.ID] = struct{}{}
			}
			continue
		}
		kept[i] = true
	}
	// Phase 2: dependency cleanup – drop item_reference targeting removed concrete IDs.
	filtered := in.retained(kept)
	keptRef := make([]bool, filtered.len())
	for i := range filtered.elems {
		it := &filtered.elems[i]
		keptRef[i] = true
		if it.Kind == aipapi.ItemKindItemReference && it.Reference != nil {
			if _, removed := removedIDs[it.Reference.ID]; removed {
				keptRef[i] = false
			}
		}
	}
	filtered = filtered.retained(keptRef)

	// Phase 3: placements
	var stable []Overlay
	var after []Overlay
	for _, ov := range snap.Steering {
		if !ov.Active {
			continue
		}
		switch ov.Placement.Kind {
		case PlacementStablePrefix:
			stable = append(stable, ov)
		case PlacementAfterMessage:
			after = append(after, ov)
		default:
			return aipapi.Call{}, nil, fmt.Errorf("%w: unknown placement %q", ErrProjectionFailed, ov.Placement.Kind)
		}
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	// Resolve after anchors
	type resolved struct {
		ov  Overlay
		idx int
		// anchor is the anchor this projection actually resolved for ov. It is the
		// stored anchor unless proven lineage supplies a missing or different boundary.
		// The stored anchor is never mutated.
		anchor *MessageAnchor
	}
	var resolvedAnchors []resolved
	var fallbackOverlays []Overlay
	var fallbacks []FallbackEvidence
	// Anchors resolve by lookup against the identities derived above, so each
	// overlay costs one map probe instead of a full re-derivation of the
	// trajectory.
	anchors := newItemAnchorIndex(filtered)
	for _, ov := range after {
		if ov.Placement.Anchor == nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s missing anchor", ErrProjectionFailed, ov.OverlayID)
		}
		idx, found, err := anchors.resolveItems(*ov.Placement.Anchor)
		if err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %w", ErrProjectionFailed, ov.OverlayID, err)
		}
		effective := ov.Placement.Anchor
		// A carried anchor already proves the distinguishable original boundary.
		// A stored occurrence can still resolve at a different duplicate after a
		// rewrite; prefer the proof only for that conflict or a missing exact match.
		if carriedAnchor, ok := carried[ov.OverlayID]; ok {
			carriedIdx, carriedFound, carriedErr := anchors.resolveItems(carriedAnchor)
			if carriedErr != nil {
				return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %w", ErrProjectionFailed, ov.OverlayID, carriedErr)
			}
			if carriedFound && (!found || carriedIdx != idx) {
				idx, found = carriedIdx, true
				cp := carriedAnchor
				effective = &cp
			}
		}
		if !found {
			switch ov.AnchorMissingPolicy {
			case AnchorStablePrefixFallback:
				fallbackOverlays = append(fallbackOverlays, ov)
				fallbacks = append(fallbacks, FallbackEvidence{OverlayID: ov.OverlayID, Anchor: *ov.Placement.Anchor})
			case AnchorFailClosed:
				// Both sentinels stay in the chain: ErrAnchorMissing marks the
				// fail-closed policy and ErrAnchorNotFound names the unresolvable
				// anchor, which runtime observers classify separately.
				return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s anchor %s missing: %w", ErrAnchorMissing, ov.OverlayID, ov.Placement.Anchor.String(), ErrAnchorNotFound)
			default:
				return aipapi.Call{}, nil, fmt.Errorf("%w: unknown policy for overlay %s", ErrProjectionFailed, ov.OverlayID)
			}
		} else {
			// No tool-adjacency adjustment exists here by construction: an item
			// message cannot carry tool calls (calls and results are their own item
			// kinds), so a message-bound insertion can never split a call/result
			// pair. Legacy messages can carry calls; projectLegacy owns that case.
			resolvedAnchors = append(resolvedAnchors, resolved{ov: ov, idx: idx, anchor: effective})
		}
	}
	// Merge fallbacks into stable and re-sort.
	if len(fallbackOverlays) > 0 {
		stable = append(stable, fallbackOverlays...)
		sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	}
	// Sort resolved by target index asc, then slot ordinal asc
	sort.Slice(resolvedAnchors, func(i, j int) bool {
		if resolvedAnchors[i].idx != resolvedAnchors[j].idx {
			return resolvedAnchors[i].idx < resolvedAnchors[j].idx
		}
		return resolvedAnchors[i].ov.SlotOrdinal < resolvedAnchors[j].ov.SlotOrdinal
	})

	leading := leadingInstructionItemCount(filtered.elems)
	// Build map from idx to list of overlays (in order)
	afterByIdx := make(map[int][]Overlay)
	for _, r := range resolvedAnchors {
		afterByIdx[r.idx] = append(afterByIdx[r.idx], r.ov)
	}
	// Assemble final items deterministically and record exact injected positions
	// without relying on synthetic ID string markers. Provenance indices are
	// derived solely from placement logic (Snapshot + input) and are not
	// part of the model-visible content identity.
	finalCapacity, err := checkedCapacity(filtered.len(), len(stable), len(resolvedAnchors))
	if err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: projected item capacity overflow", err)
	}
	provenanceCapacity, err := checkedCapacity(len(stable), len(resolvedAnchors))
	if err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: projection provenance capacity overflow", err)
	}
	final := make([]aipapi.Item, 0, finalCapacity)
	provIndex := make(map[string]int, provenanceCapacity)
	// prefix region with interleaved after anchors that fall inside prefix
	for i := range leading {
		final = append(final, filtered.elems[i])
		if ovs, ok := afterByIdx[i]; ok {
			for _, ov := range ovs {
				provIndex[ov.OverlayID] = len(final)
				final = append(final, overlayToItem(ov))
			}
		}
	}
	// stable prefix region (deterministic SlotOrdinal order)
	for _, ov := range stable {
		provIndex[ov.OverlayID] = len(final)
		final = append(final, overlayToItem(ov))
	}
	// history region with after injections
	for i := leading; i < filtered.len(); i++ {
		final = append(final, filtered.elems[i])
		if ovs, ok := afterByIdx[i]; ok {
			for _, ov := range ovs {
				provIndex[ov.OverlayID] = len(final)
				final = append(final, overlayToItem(ov))
			}
		}
	}

	out := aipapi.CloneCall(call)
	out.Items = final

	if err := out.Validate(); err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
	}
	// Deterministic provenance: derived solely from Snapshot + input, with exact injected positions.
	provenance := make([]OverlayProvenance, 0, provenanceCapacity)
	for _, ov := range stable {
		idx := provIndex[ov.OverlayID]
		// provIndex always present for stable; fallback to -1 only on invariant breach
		if _, ok := provIndex[ov.OverlayID]; !ok {
			idx = -1
		}
		provenance = append(provenance, OverlayProvenance{
			OverlayID:               ov.OverlayID,
			Revision:                ov.Revision,
			SlotOrdinal:             ov.SlotOrdinal,
			ResolvedKind:            PlacementStablePrefix,
			ResolvedAnchor:          nil,
			InjectedIdentity:        overlayInjectedIdentity(ov),
			InjectedAuthority:       "item",
			InjectedItemIndex:       idx,
			InjectedTrajectoryIndex: idx,
		})
	}
	for _, r := range resolvedAnchors {
		idx := provIndex[r.ov.OverlayID]
		if _, ok := provIndex[r.ov.OverlayID]; !ok {
			idx = -1
		}
		var cp *MessageAnchor
		if r.anchor != nil {
			tmp := *r.anchor
			cp = &tmp
		}
		provenance = append(provenance, OverlayProvenance{
			OverlayID:               r.ov.OverlayID,
			Revision:                r.ov.Revision,
			SlotOrdinal:             r.ov.SlotOrdinal,
			ResolvedKind:            PlacementAfterMessage,
			ResolvedAnchor:          cp,
			InjectedIdentity:        overlayInjectedIdentity(r.ov),
			InjectedAuthority:       "item",
			InjectedItemIndex:       idx,
			InjectedTrajectoryIndex: idx,
		})
	}
	sort.Slice(provenance, func(i, j int) bool { return provenance[i].SlotOrdinal < provenance[j].SlotOrdinal })
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: provenanceCapacity,
		Fallbacks:     fallbacks,
		Provenance:    provenance,
	}
	return out, evidence, nil
}

// ---------------------------------------------------------------------------
// legacy authority projection
// ---------------------------------------------------------------------------

func projectLegacy(call aipapi.Call, in legacyTrajectory, snap Snapshot, carried carriedAnchors) (aipapi.Call, *ProjectionEvidence, error) {
	exclusion := make(map[MessageIdentity]struct{}, len(snap.NeverBackend))
	for _, t := range snap.NeverBackend {
		exclusion[t.Identity] = struct{}{}
	}
	// Exclusion reads the identities derived once by the caller, so the anchor
	// resolution and carried-anchor passes below reuse the same derivation.
	filteredCount := 0
	keptInstr := make([]bool, in.instr.len())
	for i := range in.instr.elems {
		id, err := in.instr.identity(i)
		if err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: invalid instruction message: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			filteredCount++
			continue
		}
		keptInstr[i] = true
	}
	keptMsgs := make([]bool, in.msgs.len())
	for i := range in.msgs.elems {
		id, err := in.msgs.identity(i)
		if err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: invalid message: %w", ErrProjectionFailed, err)
		}
		if _, excluded := exclusion[id]; excluded {
			filteredCount++
			continue
		}
		keptMsgs[i] = true
	}
	filteredInstr := in.instr.retained(keptInstr)
	filteredMsgs := in.msgs.retained(keptMsgs)

	var stable []Overlay
	var after []Overlay
	for _, ov := range snap.Steering {
		if !ov.Active {
			continue
		}
		switch ov.Placement.Kind {
		case PlacementStablePrefix:
			stable = append(stable, ov)
		case PlacementAfterMessage:
			after = append(after, ov)
		default:
			return aipapi.Call{}, nil, fmt.Errorf("%w: unknown placement", ErrProjectionFailed)
		}
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })

	type resolved struct {
		ov      Overlay
		isInstr bool
		idx     int
		// anchor is the anchor this projection actually resolved for ov. It is the
		// stored anchor unless proven lineage supplies a missing or different boundary.
		// The stored anchor is never mutated.
		anchor *MessageAnchor
	}
	var resolvedAnchors []resolved
	var fallbackOverlays []Overlay
	var fallbacks []FallbackEvidence
	// Anchors resolve by lookup against the identities derived above. A legacy
	// trajectory keeps instructions and messages as separate regions, so the
	// index spans instructions first and answers with the region as well.
	anchors := newLegacyAnchorIndex(filteredInstr, filteredMsgs)
	for _, ov := range after {
		if ov.Placement.Anchor == nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s missing anchor", ErrProjectionFailed, ov.OverlayID)
		}
		isInstr, idx, found, err := anchors.resolveLegacy(filteredInstr.len(), *ov.Placement.Anchor)
		if err != nil {
			return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %w", ErrProjectionFailed, ov.OverlayID, err)
		}
		effective := ov.Placement.Anchor
		// Compare the complete boundary, including the instruction/message region.
		// Without proven carry-forward, exact resolution and missing-anchor policy
		// remain authoritative; matching boundaries retain the stored evidence.
		if carriedAnchor, ok := carried[ov.OverlayID]; ok {
			carriedIsInstr, carriedIdx, carriedFound, carriedErr := anchors.resolveLegacy(filteredInstr.len(), carriedAnchor)
			if carriedErr != nil {
				return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s: %w", ErrProjectionFailed, ov.OverlayID, carriedErr)
			}
			if carriedFound && (!found || carriedIsInstr != isInstr || carriedIdx != idx) {
				isInstr, idx, found = carriedIsInstr, carriedIdx, true
				cp := carriedAnchor
				effective = &cp
			}
		}
		if !found {
			switch ov.AnchorMissingPolicy {
			case AnchorStablePrefixFallback:
				fallbackOverlays = append(fallbackOverlays, ov)
				fallbacks = append(fallbacks, FallbackEvidence{OverlayID: ov.OverlayID, Anchor: *ov.Placement.Anchor})
			case AnchorFailClosed:
				// Same denial contract as projectItems: the policy sentinel and the
				// unresolvable-anchor sentinel both stay in the chain so runtime
				// observers classify the failure without parsing text.
				return aipapi.Call{}, nil, fmt.Errorf("%w: overlay %s anchor %s missing: %w", ErrAnchorMissing, ov.OverlayID, ov.Placement.Anchor.String(), ErrAnchorNotFound)
			default:
				return aipapi.Call{}, nil, fmt.Errorf("%w: unknown policy", ErrProjectionFailed)
			}
		} else {
			if !isInstr {
				// Tool-adjacency invariant: when the anchored message carries tool
				// calls, the overlay lands after the contiguous tool-result messages
				// answering those calls, never between a call and its result, which
				// Chat-family, Anthropic-messages and strict-Responses upstreams
				// reject. Only the overlay moves; client messages keep their order.
				idx = afterMessageToolResultIndex(filteredMsgs.elems, idx)
			}
			resolvedAnchors = append(resolvedAnchors, resolved{ov: ov, isInstr: isInstr, idx: idx, anchor: effective})
		}
	}
	if len(fallbackOverlays) > 0 {
		stable = append(stable, fallbackOverlays...)
		sort.Slice(stable, func(i, j int) bool { return stable[i].SlotOrdinal < stable[j].SlotOrdinal })
	}
	// Sort resolved: instructions first, then messages by idx, then slot.
	sort.Slice(resolvedAnchors, func(i, j int) bool {
		if resolvedAnchors[i].isInstr != resolvedAnchors[j].isInstr {
			return resolvedAnchors[i].isInstr && !resolvedAnchors[j].isInstr
		}
		if resolvedAnchors[i].idx != resolvedAnchors[j].idx {
			return resolvedAnchors[i].idx < resolvedAnchors[j].idx
		}
		return resolvedAnchors[i].ov.SlotOrdinal < resolvedAnchors[j].ov.SlotOrdinal
	})

	// Assemble instructions and messages with placement-aware provenance.
	// effectiveAnchors records the anchor each resolved overlay was actually placed
	// against: the stored anchor unless proven lineage supplies a missing or different
	// boundary. Downstream stages verify
	// placement against this value, so it must describe the position in THIS call.
	effectiveAnchors := make(map[string]*MessageAnchor, len(resolvedAnchors))
	for _, r := range resolvedAnchors {
		if r.anchor != nil {
			effectiveAnchors[r.ov.OverlayID] = r.anchor
		}
	}
	provenanceCapacity, err := checkedCapacity(len(stable), len(resolvedAnchors))
	if err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: projection provenance capacity overflow", err)
	}
	provenanceLegacy := make([]OverlayProvenance, 0, provenanceCapacity)
	// Build finalInstr with after injections and stable, recording provenance indices.
	finalInstructionCapacity, err := checkedCapacity(filteredInstr.len(), len(stable), len(resolvedAnchors))
	if err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: projected instruction capacity overflow", err)
	}
	finalInstr := make([]aipapi.Message, 0, finalInstructionCapacity)
	afterInstrByIdx := make(map[int][]Overlay)
	for _, r := range resolvedAnchors {
		if r.isInstr {
			afterInstrByIdx[r.idx] = append(afterInstrByIdx[r.idx], r.ov)
		}
	}
	for i, m := range filteredInstr.elems {
		finalInstr = append(finalInstr, m)
		if ovs, ok := afterInstrByIdx[i]; ok {
			for _, ov := range ovs {
				idx := len(finalInstr)
				finalInstr = append(finalInstr, overlayToMessage(ov))
				var cp *MessageAnchor
				if anchor := effectiveAnchors[ov.OverlayID]; anchor != nil {
					tmp := *anchor
					cp = &tmp
				}
				provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
					OverlayID:                   ov.OverlayID,
					Revision:                    ov.Revision,
					SlotOrdinal:                 ov.SlotOrdinal,
					ResolvedKind:                PlacementAfterMessage,
					ResolvedAnchor:              cp,
					InjectedIdentity:            overlayInjectedIdentity(ov),
					InjectedAuthority:           "legacy",
					InjectedLegacyIsInstruction: boolPtr(true),
					InjectedLegacyIndex:         intPtr(idx),
					InjectedTrajectoryIndex:     idx,
				})
			}
		}
	}
	// Append stable overlays at end of instruction region (after all instruction messages and their after injections).
	for pos, ov := range stable {
		idx := len(finalInstr)
		finalInstr = append(finalInstr, overlayToMessage(ov))
		_ = pos
		provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
			OverlayID:                   ov.OverlayID,
			Revision:                    ov.Revision,
			SlotOrdinal:                 ov.SlotOrdinal,
			ResolvedKind:                PlacementStablePrefix,
			ResolvedAnchor:              nil,
			InjectedIdentity:            overlayInjectedIdentity(ov),
			InjectedAuthority:           "legacy",
			InjectedLegacyIsInstruction: boolPtr(true),
			InjectedLegacyIndex:         intPtr(idx),
			InjectedTrajectoryIndex:     idx,
		})
	}

	// Assemble messages final with after injections.
	finalMessageCapacity, err := checkedCapacity(filteredMsgs.len(), len(resolvedAnchors))
	if err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: projected message capacity overflow", err)
	}
	finalMsgs := make([]aipapi.Message, 0, finalMessageCapacity)
	afterMsgByIdx := make(map[int][]Overlay)
	for _, r := range resolvedAnchors {
		if !r.isInstr {
			afterMsgByIdx[r.idx] = append(afterMsgByIdx[r.idx], r.ov)
		}
	}
	// We need base offset for trajectory index: instructions length.
	instrLen := len(finalInstr)
	for i, m := range filteredMsgs.elems {
		finalMsgs = append(finalMsgs, m)
		if ovs, ok := afterMsgByIdx[i]; ok {
			for _, ov := range ovs {
				idxInMsgs := len(finalMsgs)
				finalMsgs = append(finalMsgs, overlayToMessage(ov))
				var cp *MessageAnchor
				if anchor := effectiveAnchors[ov.OverlayID]; anchor != nil {
					tmp := *anchor
					cp = &tmp
				}
				provenanceLegacy = append(provenanceLegacy, OverlayProvenance{
					OverlayID:                   ov.OverlayID,
					Revision:                    ov.Revision,
					SlotOrdinal:                 ov.SlotOrdinal,
					ResolvedKind:                PlacementAfterMessage,
					ResolvedAnchor:              cp,
					InjectedIdentity:            overlayInjectedIdentity(ov),
					InjectedAuthority:           "legacy",
					InjectedLegacyIsInstruction: boolPtr(false),
					InjectedLegacyIndex:         intPtr(idxInMsgs),
					InjectedTrajectoryIndex:     instrLen + idxInMsgs,
				})
			}
		}
	}

	out := aipapi.CloneCall(call)
	out.Instructions = finalInstr
	out.Messages = finalMsgs

	if err := out.Validate(); err != nil {
		return aipapi.Call{}, nil, fmt.Errorf("%w: %w", ErrProjectionFailed, err)
	}
	// Stable already in provenanceLegacy, but we added stable above; ensure all stable added.
	// The above already added stable; no extra stable loop needed.
	sort.Slice(provenanceLegacy, func(i, j int) bool { return provenanceLegacy[i].SlotOrdinal < provenanceLegacy[j].SlotOrdinal })
	evidence := &ProjectionEvidence{
		FilteredCount: filteredCount,
		InjectedCount: provenanceCapacity,
		Fallbacks:     fallbacks,
		Provenance:    provenanceLegacy,
	}
	return out, evidence, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func leadingInstructionItemCount(items []aipapi.Item) int {
	count := 0
	for _, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			break
		}
		if it.Role == aipapi.RoleSystem || it.Role == aipapi.RoleDeveloper {
			count++
		} else {
			break
		}
	}
	return count
}

func overlayToItem(ov Overlay) aipapi.Item {
	text := ov.Message.Text
	role := ov.Message.Role
	if role == "" {
		role = aipapi.RoleSystem
	}
	return aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     "aip-steering-" + ov.OverlayID,
		Status: aipapi.ItemStatusCompleted,
		Role:   role,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: text},
		},
	}
}

func overlayToMessage(ov Overlay) aipapi.Message {
	text := ov.Message.Text
	role := ov.Message.Role
	if role == "" {
		role = aipapi.RoleSystem
	}
	return aipapi.Message{
		Role:  role,
		Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}},
	}
}

func overlayInjectedIdentity(ov Overlay) MessageIdentity {
	msg := overlayToMessage(ov)
	id, err := MessageIdentityOf(msg)
	if err != nil {
		// Overlay validation ensures valid role/text; panic on invariant breach.
		panic(fmt.Sprintf("conversationview: overlay %s identity: %v", ov.OverlayID, err))
	}
	return id
}

// toolCallIDsCarried returns the stable tool-call IDs a legacy message carries:
// PartJSON parts with a non-empty ToolCallID, the canonical assistant tool-call
// shape used by the frontends and by capability derivation. A message without
// such a part carries no calls and needs no adjacency protection.
func toolCallIDsCarried(msg aipapi.Message) []string {
	var ids []string
	for i := range msg.Parts {
		if msg.Parts[i].Kind == aipapi.PartJSON && msg.Parts[i].ToolCallID != "" {
			ids = append(ids, msg.Parts[i].ToolCallID)
		}
	}
	return ids
}

// messageAnswersAnyCall reports whether msg is a tool-result message answering
// any of want: it carries a PartToolResult part whose ToolCallID is in want.
func messageAnswersAnyCall(msg aipapi.Message, want map[string]struct{}) bool {
	for i := range msg.Parts {
		p := msg.Parts[i]
		if p.Kind != aipapi.PartToolResult || p.ToolCallID == "" {
			continue
		}
		if _, ok := want[p.ToolCallID]; ok {
			return true
		}
	}
	return false
}

// afterMessageToolResultIndex returns the message-region index an AfterMessage
// overlay anchored at idx must be injected after. When the anchored message
// carries tool calls, the insertion point advances past the contiguous
// tool-result messages answering those calls so a call is never separated from
// its result on the backend wire. Incomplete results place the overlay after
// the last present result; a message answering none of the anchor's calls ends
// the run. The result is a pure function of msgs and idx, so identical input
// produces identical placement across projections and prompt-cache bytes stay
// stable.
func afterMessageToolResultIndex(msgs []aipapi.Message, idx int) int {
	if idx < 0 || idx >= len(msgs) {
		return idx
	}
	callIDs := toolCallIDsCarried(msgs[idx])
	if len(callIDs) == 0 {
		return idx
	}
	want := make(map[string]struct{}, len(callIDs))
	for _, id := range callIDs {
		want[id] = struct{}{}
	}
	last := idx
	for j := idx + 1; j < len(msgs); j++ {
		if !messageAnswersAnyCall(msgs[j], want) {
			break
		}
		last = j
	}
	return last
}

// adaptedToolAnchorPlacementIndex maps a legacy assistant tool-call message to
// its placement point in an item-authority trajectory. Authority adaptation
// splits the anchored message into tool_call items, so its message identity
// cannot resolve there; the stable call IDs it carries can, because adaptation
// preserves them one-to-one onto tool_call items. The placement point is the
// last contiguous tool_result item answering those calls, or the last anchor
// tool_call item when no result is present - the item-space mirror of
// afterMessageToolResultIndex, so the same overlay verifies at the same
// logical boundary on both sides of adaptation.
//
// Each stable ID must resolve to exactly one adapted tool_call item. A missing
// ID means the adapted trajectory lost the anchor's span, and a duplicated ID
// means the span cannot be identified; both keep the existing fail-closed
// outcome instead of placing by guess.
func adaptedToolAnchorPlacementIndex(items []aipapi.Item, anchor aipapi.Message) (int, bool) {
	callIDs := toolCallIDsCarried(anchor)
	if len(callIDs) == 0 {
		return -1, false
	}
	want := make(map[string]struct{}, len(callIDs))
	for _, id := range callIDs {
		want[id] = struct{}{}
	}
	counts := make(map[string]int, len(want))
	lastCall := -1
	for i := range items {
		it := items[i]
		if it.Kind != aipapi.ItemKindToolCall || it.ToolCall == nil {
			continue
		}
		if _, ok := want[it.ToolCall.CallID]; !ok {
			continue
		}
		counts[it.ToolCall.CallID]++
		lastCall = i
	}
	if lastCall < 0 || len(counts) != len(want) {
		return -1, false
	}
	for _, n := range counts {
		if n != 1 {
			return -1, false
		}
	}
	last := lastCall
	for j := lastCall + 1; j < len(items); j++ {
		it := items[j]
		if it.Kind != aipapi.ItemKindToolResult || it.ToolResult == nil {
			break
		}
		if _, ok := want[it.ToolResult.CallID]; !ok {
			break
		}
		last = j
	}
	return last, true
}

func resolveAnchorInItems(items []aipapi.Item, anchor MessageAnchor) (int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return -1, false, err
	}
	counts := make(map[MessageIdentity]uint32)
	for i, it := range items {
		if it.Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := ItemIdentityOf(it)
		if err != nil {
			return -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return i, true, nil
		}
	}
	return -1, false, nil
}

func resolveAnchorLegacy(instr []aipapi.Message, msgs []aipapi.Message, anchor MessageAnchor) (bool, int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return false, -1, false, err
	}
	counts := make(map[MessageIdentity]uint32)
	for i, m := range instr {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return false, -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return true, i, true, nil
		}
	}
	for i, m := range msgs {
		id, err := MessageIdentityOf(m)
		if err != nil {
			return false, -1, false, err
		}
		counts[id]++
		if id == anchor.Identity && counts[id] == anchor.Occurrence {
			return false, i, true, nil
		}
	}
	return false, -1, false, nil
}

// FilterNeverBackend returns a deep clone of call with all never_backend messages removed
// (including dangling item_reference cleanup) but without injecting steering.
// It is used to derive the filtered baseline for placement-aware reassertion.
func FilterNeverBackend(call aipapi.Call, snap Snapshot) (aipapi.Call, error) {
	filteredSnap := Snapshot{
		StateRevision: snap.StateRevision,
		NeverBackend:  snap.NeverBackend,
		Steering:      nil,
	}
	out, _, err := Project(call, filteredSnap)
	return out, err
}
