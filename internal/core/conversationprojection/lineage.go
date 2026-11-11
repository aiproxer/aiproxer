package conversationprojection

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// ---------------------------------------------------------------------------
// Transform-stable reassertion: request-local trajectory lineage.
//
// A backend-only shaping stage may change the payload bytes of complete messages
// after early projection already resolved the persisted after-message anchors
// against A-leg/client truth. Message identity is a hash of normalized semantic
// content, so such a rewrite can change the identity a frozen anchor names even
// though the complete-message trajectory is unchanged. Re-resolving that frozen
// anchor against the backend-shaped content would then report ErrAnchorMissing
// and deny an otherwise ordinary turn.
//
// This file decides whether the trajectory really was preserved. It establishes a
// one-to-one structural correspondence between the FROZEN filtered baseline and
// the cleaned, backend-shaped trajectory, using only request-local evidence: no
// conversation-view store read, no awareness of which stage rewrote what, and no
// dependency on any concrete feature.
//
// IDENTITY EQUALITY IS NOT THE PROOF, in either direction.
//
//   - It is not a usable substitute: ANY rewrite of the anchored payload changes that
//     message's content-derived identity, so an identity-equality proof would refuse
//     EVERY legitimate rewrite and the carry-forward this file implements could never
//     fire. A legacy PartToolResult structured-Content rewrite is the extreme case:
//     it changes model-visible bytes with ZERO identity drift.
//   - It is also strictly WEAKER than the structural proof: a legacy PartToolResult
//     projects from its Text field only, and a legacy PartJSON atom likewise omits the
//     part's ToolName, ToolCallID, and reference fields. Two messages differing only
//     there share one identity, so identity cannot see those differences at all.
//
// The structural comparison below is therefore the authority. It compares the
// canonical authority form, the trajectory cardinality and order, the
// instruction/message or item partition, the roles and kinds, the ordered part
// kinds, and the stable canonical identities/references/tool identities. Payload
// bytes - part text, JSON documents, opaque blobs, reasoning text and signatures -
// are deliberately absent, because those are exactly what a backend-only content
// rewrite is allowed to change. MessageIdentityOf is never consulted to decide
// compatibility; it is used only to describe a position the structural proof has
// already accepted.
//
// Every incompatible shape fails closed: an insertion, a deletion, a distinguishable reorder, a
// role change, a kind change, a stable-ID or reference change, a partition
// change, or a cardinality change all fail the proof, so the existing
// exact-resolution / anchor-missing-policy outcome stays authoritative and no
// placement is ever relocated by ordinal.
//
// EXCLUDING PAYLOAD IS ALSO WHAT LEAVES A RESIDUAL AMBIGUITY, and it is closed
// separately, per placement, in [trajectoryLineage.slotDistinguishable]: two
// legacy messages or ID-less items that share a structural descriptor are interchangeable under
// every comparison above, so the slot identity alone cannot say which of them a
// frozen anchor's slot stands for. The ambiguity is undecidable from the
// descriptors - any same-shaped pair is compatible under SOME correspondence,
// because payload is exactly what a rewrite may change - so the answer is to
// decline that placement rather than to guess. It is a per-placement refusal and
// NOT a further condition on the proof: a trajectory may legitimately hold many
// same-shaped messages as long as the ANCHORED slot is distinguishable, and
// demanding global uniqueness would refuse requirement 5.9's own carry-forward.
// ---------------------------------------------------------------------------

// carriedAnchors maps an overlay ID to the request-local anchor that a proven
// trajectory lineage derived for that overlay's already-resolved after-message
// placement. It fills a missing exact match or overrides an exact occurrence that
// now selects a different boundary. Matching boundaries retain stored evidence.
type carriedAnchors map[string]MessageAnchor

// trajectoryLineage records that a cleaned backend-shaped trajectory is a
// one-to-one, structure-preserving rewrite of the frozen filtered baseline.
//
// The correspondence is the identity on the canonical trajectory slot order:
// slot i of the cleaned trajectory corresponds to slot i of the frozen baseline,
// and the lineage exists only because every slot's structural descriptor
// matched. Slots are enumerated instructions-then-messages for legacy authority
// and by index for item authority, which keeps the instruction/message partition
// and the item partition part of the proof rather than something inferred from
// the content.
type trajectoryLineage struct {
	itemAuthority bool
	slots         int
}

// proven reports whether the lineage was established.
func (l trajectoryLineage) proven() bool { return l.slots > 0 }

// proveTrajectoryLineage establishes the one-to-one structural correspondence
// between the frozen filtered baseline and the cleaned backend-shaped
// trajectory, or reports that it cannot be established.
//
// The comparison is deliberately all-or-nothing: a single incompatible slot
// invalidates the whole proof, because a partial proof is exactly what would
// degrade into relocating the remaining placements by ordinal.
func proveTrajectoryLineage(frozenIn, cleanedIn projectionInput) (trajectoryLineage, bool) {
	if frozenIn.itemAuthority != cleanedIn.itemAuthority {
		return trajectoryLineage{}, false
	}
	if cleanedIn.itemAuthority {
		frozen, cleaned := frozenIn.items, cleanedIn.items
		if frozen.len() != cleaned.len() {
			return trajectoryLineage{}, false
		}
		for i := range frozen.elems {
			if !itemLineageCompatible(frozen.elems[i], cleaned.elems[i]) {
				return trajectoryLineage{}, false
			}
		}
		return trajectoryLineage{itemAuthority: true, slots: frozen.len()}, true
	}
	frozen, cleaned := frozenIn.legacy, cleanedIn.legacy
	if frozen.instr.len() != cleaned.instr.len() || frozen.msgs.len() != cleaned.msgs.len() {
		return trajectoryLineage{}, false
	}
	for i := range frozen.instr.elems {
		if !messageLineageCompatible(frozen.instr.elems[i], cleaned.instr.elems[i]) {
			return trajectoryLineage{}, false
		}
	}
	for i := range frozen.msgs.elems {
		if !messageLineageCompatible(frozen.msgs.elems[i], cleaned.msgs.elems[i]) {
			return trajectoryLineage{}, false
		}
	}
	return trajectoryLineage{slots: frozen.instr.len() + frozen.msgs.len()}, true
}

// frozenSlot resolves a frozen after-message anchor to its slot in the frozen
// filtered baseline. An anchor the baseline does not resolve has no frozen
// position, so there is nothing to carry forward; neither has one whose frozen
// slot the structural descriptors cannot single out (see
// [trajectoryLineage.slotDistinguishable]), because a placement carried onto an
// interchangeable slot is a relocation, not a lineage.
func (l trajectoryLineage) frozenSlot(frozenIn projectionInput, anchor MessageAnchor) (int, bool) {
	if l.itemAuthority {
		frozen := frozenIn.items
		slots := newItemAnchorIndex(frozen)
		idx, found, err := slots.resolveItems(anchor)
		if err != nil || !found {
			return 0, false
		}
		if !l.slotDistinguishable(frozenIn, idx) {
			return 0, false
		}
		return idx, true
	}
	frozen := frozenIn.legacy
	slots := newLegacyAnchorIndex(frozen.instr, frozen.msgs)
	isInstr, idx, found, err := slots.resolveLegacy(frozen.instr.len(), anchor)
	if err != nil || !found {
		return 0, false
	}
	slot := idx
	if !isInstr {
		slot = frozen.instr.len() + idx
	}
	if !l.slotDistinguishable(frozenIn, slot) {
		return 0, false
	}
	return slot, true
}

// slotDistinguishable reports whether one canonical trajectory slot is the ONLY
// slot of its own region whose structural descriptor matches it, which is what
// makes the slot identity a usable correspondence rather than a guess.
//
// WHY THIS IS NEEDED. [proveTrajectoryLineage] establishes the correspondence as
// the identity on slot order and deliberately excludes payload bytes, because a
// backend-only content rewrite is allowed to change them. Two legacy messages or items
// that share a structural descriptor are therefore interchangeable: exchanging
// them leaves every slot-wise comparison satisfied while moving the logical
// message each slot stands for, so a frozen anchor naming one of them would be
// carried onto the other and the steering overlay would silently follow the
// ordinal instead of the anchored message.
//
// WHY PAYLOAD CANNOT SETTLE IT. Any same-shaped pair is compatible under SOME
// correspondence, because a difference in payload is exactly what a rewrite is
// permitted to produce and is therefore never evidence against a pairing. The
// tie is genuinely undecidable from the descriptors, and declining is the
// required answer rather than a loss: the placement falls back to the untouched
// exact-resolution / anchor-missing-policy path, which for an anchored message
// whose identity drifted is the configured AnchorFailClosed denial.
//
// WHY IT IS PER PLACEMENT, NOT PER TRAJECTORY. A trajectory may legitimately
// contain many same-shaped messages; requirement 5.9's carry-forward only needs
// the ANCHORED slot to be pinned. Refusing every trajectory that holds a pair
// would refuse legitimate rewrites, so the check runs once per placement, over
// the frozen baseline, in the region that placement's slot belongs to.
//
// Instructions and messages are compared within their own region because the
// proof already established the partition, so a message can never be the
// counterpart of an instruction. Items share one region. A nonempty unique item
// ID distinguishes its descriptor, but IDs are optional: ID-less items must also
// be compared using the same payload-free structural comparator as the proof.
func (l trajectoryLineage) slotDistinguishable(frozenIn projectionInput, slot int) bool {
	if l.itemAuthority {
		frozen := frozenIn.items
		if slot < 0 || slot >= frozen.len() {
			return false
		}
		for j := range frozen.elems {
			if j != slot && itemLineageCompatible(frozen.elems[slot], frozen.elems[j]) {
				return false
			}
		}
		return true
	}
	if slot < 0 {
		return false
	}
	frozen := frozenIn.legacy
	if slot < frozen.instr.len() {
		for j := range frozen.instr.elems {
			if j != slot && messageLineageCompatible(frozen.instr.elems[slot], frozen.instr.elems[j]) {
				return false
			}
		}
		return true
	}
	msg := slot - frozen.instr.len()
	if msg >= frozen.msgs.len() {
		return false
	}
	for j := range frozen.msgs.elems {
		if j != msg && messageLineageCompatible(frozen.msgs.elems[msg], frozen.msgs.elems[j]) {
			return false
		}
	}
	return true
}

// currentAnchorAt derives the anchor that names the cleaned trajectory message
// occupying the same slot. Because the lineage is the identity on slot order, the
// derived occurrence is the count of that message's own identity over the cleaned
// prefix, which is precisely what exact resolution needs to land on the same
// slot again. The identities come from the table the caller already derived.
func (l trajectoryLineage) currentAnchorAt(cleanedIn projectionInput, slot int) (MessageAnchor, bool) {
	if l.itemAuthority {
		cleaned := cleanedIn.items
		if slot < 0 || slot >= cleaned.len() {
			return MessageAnchor{}, false
		}
		id, err := cleaned.identity(slot)
		if err != nil {
			return MessageAnchor{}, false
		}
		occ := uint32(0)
		for i := 0; i <= slot; i++ {
			if cleaned.elems[i].Kind != aipapi.ItemKindMessage {
				continue
			}
			cid, err := cleaned.identity(i)
			if err != nil {
				return MessageAnchor{}, false
			}
			if cid == id {
				occ++
			}
		}
		return validatedAnchor(id, occ)
	}
	cleaned := cleanedIn.legacy
	slotCount := cleaned.instr.len() + cleaned.msgs.len()
	if slot < 0 || slot >= slotCount {
		return MessageAnchor{}, false
	}
	var id MessageIdentity
	if slot < cleaned.instr.len() {
		var err error
		id, err = cleaned.instr.identity(slot)
		if err != nil {
			return MessageAnchor{}, false
		}
	} else {
		var err error
		id, err = cleaned.msgs.identity(slot - cleaned.instr.len())
		if err != nil {
			return MessageAnchor{}, false
		}
	}
	occ := uint32(0)
	for i := 0; i <= slot; i++ {
		var cid MessageIdentity
		if i < cleaned.instr.len() {
			var err error
			cid, err = cleaned.instr.identity(i)
			if err != nil {
				return MessageAnchor{}, false
			}
		} else {
			var err error
			cid, err = cleaned.msgs.identity(i - cleaned.instr.len())
			if err != nil {
				return MessageAnchor{}, false
			}
		}
		if cid == id {
			occ++
		}
	}
	return validatedAnchor(id, occ)
}

func validatedAnchor(id MessageIdentity, occ uint32) (MessageAnchor, bool) {
	anchor := MessageAnchor{Identity: id, Occurrence: occ}
	return anchor, anchor.Validate() == nil
}

// deriveCarriedAnchors returns the request-local anchors a proven trajectory
// lineage yields for the after-message placements early projection already
// resolved, or nil when no placement may be carried forward.
//
// The only admissible basis is the frozen filtered baseline plus the early
// projection evidence. Nothing here reads the conversation-view store, and
// nothing here is aware of which stage rewrote which payload. Every failure to
// prove a placement - no frozen evidence, an anchor the baseline cannot resolve,
// no structural bijection, an authority or partition change - yields nil for that
// overlay so the existing exact-resolution / anchor-missing-policy outcome stands.
func deriveCarriedAnchors(snap Snapshot, provenance []OverlayProvenance, frozenIn, cleanedIn projectionInput) carriedAnchors {
	if len(provenance) == 0 {
		return nil
	}
	lineage, ok := proveTrajectoryLineage(frozenIn, cleanedIn)
	if !ok || !lineage.proven() {
		return nil
	}
	early := make(map[string]MessageAnchor, len(provenance))
	for _, p := range provenance {
		if p.ResolvedKind != PlacementAfterMessage || p.ResolvedAnchor == nil {
			continue
		}
		early[p.OverlayID] = *p.ResolvedAnchor
	}
	out := carriedAnchors{}
	for _, ov := range snap.Steering {
		if !ov.Active || ov.Placement.Kind != PlacementAfterMessage || ov.Placement.Anchor == nil {
			continue
		}
		stored := *ov.Placement.Anchor
		// The early projection is the only authority allowed to have resolved this
		// placement. Without its frozen evidence there is nothing to carry forward.
		resolved, ok := early[ov.OverlayID]
		if !ok || resolved != stored {
			continue
		}
		slot, ok := lineage.frozenSlot(frozenIn, stored)
		if !ok {
			continue
		}
		carried, ok := lineage.currentAnchorAt(cleanedIn, slot)
		if !ok || carried == stored {
			// A carried anchor identical to the stored one is already covered by
			// exact resolution, which stays the normal path.
			continue
		}
		out[ov.OverlayID] = carried
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// Structural compatibility. Payload bytes are absent from every comparison below.
// ---------------------------------------------------------------------------

// messageLineageCompatible reports whether two legacy complete messages occupy the
// same logical trajectory slot.
//
// Like every comparator in this section it is an equivalence relation over the
// fields it compares, so two messages it accepts are interchangeable for
// [trajectoryLineage.slotDistinguishable]'s purposes - which is precisely why an
// accepted pair there means "ambiguous" and not "corresponds".
func messageLineageCompatible(frozen, current aipapi.Message) bool {
	if frozen.Role != current.Role || len(frozen.Parts) != len(current.Parts) {
		return false
	}
	for i := range frozen.Parts {
		if !partLineageCompatible(frozen.Parts[i], current.Parts[i]) {
			return false
		}
	}
	return true
}

// partLineageCompatible compares one ordered part's kind, cardinality-compatible
// reference fields, and tool identities. Text and the JSON Content document are
// payload and are not compared, because a backend-only rewrite may change them.
//
// Excluding them is what leaves a part indistinguishable from another part of
// the same shape, and [trajectoryLineage.slotDistinguishable] is where that
// residual ambiguity is turned into a refusal instead of a carried placement.
// Note also that this comparator is an equivalence relation over exactly these
// fields - every clause is an equality - which is why slotDistinguishable can
// reuse it directly to ask "does any other slot share this descriptor?" without
// introducing a second, drift-prone descriptor definition.
func partLineageCompatible(frozen, current aipapi.Part) bool {
	return frozen.Kind == current.Kind &&
		frozen.ToolCallID == current.ToolCallID &&
		frozen.ToolName == current.ToolName &&
		frozen.ImageRef == current.ImageRef &&
		frozen.ImageMIME == current.ImageMIME &&
		frozen.FileRef == current.FileRef &&
		frozen.FileMIME == current.FileMIME &&
		frozen.FileName == current.FileName &&
		reasoningLineageCompatible(frozen.Reasoning, current.Reasoning)
}

// itemLineageCompatible reports whether two items occupy the same logical
// trajectory slot, across every canonical item kind.
func itemLineageCompatible(frozen, current aipapi.Item) bool {
	if frozen.Kind != current.Kind ||
		frozen.ID != current.ID ||
		frozen.Status != current.Status ||
		frozen.Phase != current.Phase ||
		frozen.Role != current.Role {
		return false
	}
	switch frozen.Kind {
	case aipapi.ItemKindMessage:
		return contentPartsLineageCompatible(frozen.Content, current.Content)
	case aipapi.ItemKindItemReference:
		return frozen.Reference != nil && current.Reference != nil && *frozen.Reference == *current.Reference
	case aipapi.ItemKindToolCall:
		if frozen.ToolCall == nil || current.ToolCall == nil {
			return frozen.ToolCall == nil && current.ToolCall == nil
		}
		return frozen.ToolCall.CallID == current.ToolCall.CallID &&
			frozen.ToolCall.Name == current.ToolCall.Name
	case aipapi.ItemKindToolResult:
		if frozen.ToolResult == nil || current.ToolResult == nil {
			return frozen.ToolResult == nil && current.ToolResult == nil
		}
		return frozen.ToolResult.CallID == current.ToolResult.CallID &&
			frozen.ToolResult.Name == current.ToolResult.Name &&
			contentPartsLineageCompatible(frozen.ToolResult.Parts, current.ToolResult.Parts)
	case aipapi.ItemKindReasoning:
		return reasoningLineageCompatible(reasoningItemPart(frozen.Reasoning), reasoningItemPart(current.Reasoning))
	case aipapi.ItemKindCompaction:
		if frozen.Compaction == nil || current.Compaction == nil {
			return frozen.Compaction == nil && current.Compaction == nil
		}
		return frozen.Compaction.EncapsulatedID == current.Compaction.EncapsulatedID &&
			frozen.Compaction.Dialect == current.Compaction.Dialect
	default:
		return itemExtensionLineageCompatible(frozen.Extension, current.Extension)
	}
}

// contentPartsLineageCompatible compares an ordered content-part sequence by kind,
// reference fields, stable references, and reasoning/extension shape. Text, file
// data, refusal, summary, annotation payload, and JSON documents are payload.
func contentPartsLineageCompatible(frozen, current []aipapi.ContentPart) bool {
	if len(frozen) != len(current) {
		return false
	}
	for i := range frozen {
		f, c := frozen[i], current[i]
		if f.Kind != c.Kind ||
			f.ImageRef != c.ImageRef ||
			f.ImageMIME != c.ImageMIME ||
			f.FileRef != c.FileRef ||
			f.FileMIME != c.FileMIME ||
			f.FileName != c.FileName ||
			f.VideoRef != c.VideoRef ||
			f.VideoMIME != c.VideoMIME ||
			f.AssistantRef != c.AssistantRef {
			return false
		}
		if !reasoningLineageCompatible(f.Reasoning, c.Reasoning) {
			return false
		}
		if (f.Extension == nil) != (c.Extension == nil) {
			return false
		}
		if f.Extension != nil &&
			(f.Extension.Namespace != c.Extension.Namespace ||
				f.Extension.Type != c.Extension.Type ||
				f.Extension.Implementor != c.Extension.Implementor) {
			return false
		}
	}
	return true
}

// reasoningLineageCompatible compares only the shape of a historical reasoning
// record: presence, dialect, and encrypted-content presence. Text, signature, and
// opaque metadata are payload.
func reasoningLineageCompatible(frozen, current *aipapi.ReasoningPart) bool {
	if (frozen == nil) != (current == nil) {
		return false
	}
	if frozen == nil {
		return true
	}
	return aipapi.NormalizeReasoningDialect(frozen.Dialect) == aipapi.NormalizeReasoningDialect(current.Dialect) &&
		frozen.EncryptedContentPresent == current.EncryptedContentPresent
}

func reasoningItemPart(item *aipapi.ReasoningItem) *aipapi.ReasoningPart {
	if item == nil {
		return nil
	}
	return item.Reasoning
}

func itemExtensionLineageCompatible(frozen, current *aipapi.OpaqueExtension) bool {
	if (frozen == nil) != (current == nil) {
		return false
	}
	if frozen == nil {
		return true
	}
	return frozen.Namespace == current.Namespace &&
		frozen.Type == current.Type &&
		frozen.Implementor == current.Implementor &&
		frozen.Direction == current.Direction
}
