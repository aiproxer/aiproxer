package runtime

import (
	"context"
	"errors"
	"log/slog"
	"reflect"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The thinker memo reaches executor context exclusively as a persistent
// backend-only steering overlay in the conversation-view store (#391). The
// runtime projects it as a standalone synthetic user message at its fixed
// activation anchor; there is no per-attempt injection code. Publishing is
// best-effort with bounded diagnostics: when steering cannot be persisted, the
// memo is not linked for presentation and the turn continues without it,
// mirroring the tolerant missing-memo semantics of the shaping contract.
//
// Memo steering policy (rendering, overlay identity, placement/fallback
// selection, memo filtering) is feature-owned by
// internal/plugins/features/interleavedthinking and reaches this orchestration
// only through the runtime.InterleavedProcessor port. This file keeps the
// authoritative output commitment and B-leg continuation sequencing.

// publishMemoSteeringOverlay persists or replaces the thinker memo steering
// overlay for the A-leg. The mutation itself is feature-owned (rendering,
// overlay identity, placement, fallback, reason); this orchestration only
// resolves the writer and commits it authoritatively.
func (e *Executor) publishMemoSteeringOverlay(
	ctx context.Context,
	aLegID string,
	ingress aipapi.Call,
	snap conversationprojection.Snapshot,
	memo string,
) error {
	if e == nil || ctx == nil {
		return errors.New("executor: invalid memo steering publish arguments")
	}
	if e.InterleavedProcessor == nil {
		return errors.New("executor: interleaved processor unavailable")
	}
	if e.SteeringWriterFactory == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	writer, err := e.SteeringWriterFactory(ctx, aLegID, func(_ context.Context) (aipapi.Call, conversationprojection.Snapshot, error) {
		return ingress, snap, nil
	})
	if err != nil {
		return err
	}
	_, err = writer.Put(ctx, e.InterleavedProcessor.MemoSteeringPutRequest(memo))
	return err
}

// deactivateMemoSteeringOverlay removes the memo steering overlay once the memo
// budget is exhausted or the memo bookkeeping can no longer guarantee bounded
// presentation. Best-effort and idempotent: a missing overlay or A-leg is not
// an error.
func (e *Executor) deactivateMemoSteeringOverlay(ctx context.Context, aLegID string) error {
	if e == nil || ctx == nil || aLegID == "" {
		return nil
	}
	if e.InterleavedProcessor == nil {
		return nil
	}
	if e.SteeringWriterFactory == nil {
		return errors.New("executor: conversation-view steering capability unavailable")
	}
	writer, err := e.SteeringWriterFactory(ctx, aLegID, nil)
	if err != nil {
		return err
	}
	_, err = writer.Deactivate(ctx, e.InterleavedProcessor.MemoSteeringOverlayID())
	if err != nil && !errors.Is(err, conversationprojection.ErrOverlayNotFound) && !errors.Is(err, conversationprojection.ErrALegNotFound) {
		e.logMemoSteeringDeactivateFailed(ctx, aLegID, err)
		return err
	}
	return nil
}

// refreshMemoSteeringFacts rebuilds the request facts for the same-turn
// interleaved executor continuation so the just-published memo overlay is part
// of the projected baseline, snapshot, provenance, and filtered view handed to
// planning and final reassertion. When the visible-mode immediate-continuation
// suppression applies, the memo overlay is stripped from this turn's snapshot
// copy only; the persisted overlay stays active for later turns.
//
// Soft-fail: any reader/projector error keeps the pre-capture facts so the
// continuation proceeds on its coherent frozen view without the newest overlay;
// the next logical turn picks it up from the authoritative store.
func (e *Executor) refreshMemoSteeringFacts(
	ctx context.Context,
	facts recvTurnFacts,
	state interleavedstate.State,
	suppressVisibleMemo bool,
) (recvTurnFacts, bool) {
	if e == nil || ctx == nil {
		return facts, false
	}
	if e.InterleavedProcessor == nil {
		return facts, false
	}
	reader := e.conversationViewReader()
	if reader == nil {
		return facts, false
	}
	snap, err := reader.Snapshot(ctx, facts.aLegID)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "snapshot", err)
		return facts, false
	}
	memoVisibleSuppressed := suppressVisibleMemo && e.memoStateVisibleToClient(ctx, facts.aLegID, state)
	if memoVisibleSuppressed {
		snap = withoutSteeringOverlay(snap, e.InterleavedProcessor.IsMemoSteeringOverlay)
	}
	if snap.StateRevision == conversationRevision(facts) && !memoVisibleSuppressed {
		return facts, true
	}
	ingress := memoProjectionIngress(facts)
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = memoProjectionBaseline(facts)
	}
	projected, ev, err := conversationprojection.Project(ingress, snap)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "projection", err)
		return facts, false
	}
	filtered, err := conversationprojection.FilterNeverBackend(ingress, snap)
	if err != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "filter", err)
		return facts, false
	}
	updated := cloneRefreshedMemoFacts(facts)
	// Projection owns conversation authority only. Apply its trajectory to the
	// already-frozen baseline so route overrides and all other admission fields
	// remain identical across thinker/executor legs.
	baseline := memoProjectionBaseline(facts)
	baseline.Instructions, baseline.Messages, baseline.Items = projected.Instructions, projected.Messages, projected.Items
	// Only a demonstrated change to the effective provider-bound system or
	// developer instructions records the bounded cause. Both sides are
	// projected from the same ingress, so unrelated request-wide
	// transformations already applied to the live baseline cannot leak into
	// the decision; any residual difference comes from the snapshot change
	// itself. Each side is evaluated with the placement evidence generated by
	// its own projection: the frozen provenance belongs to the prepared backend
	// call and can index a different trajectory. Ordinary user-role overlay
	// emissions are removed by that evidence, NeverBackend-excluded ingress
	// content is removed symmetrically, and only the boolean result leaves this
	// path: no instruction text, content, identifier, revision, reason, or
	// exclusion is published or retained. Memo suppression removes only the
	// memo overlay before projection, so it cannot mask a separate instruction
	// change.
	oldProjected, oldEv, oldErr := conversationprojection.Project(ingress, facts.conversationSnapshot)
	if oldErr != nil {
		e.logMemoSteeringRefreshFailure(ctx, facts.traceID, "resnapshot", oldErr)
	} else if providerInstructionsChanged(oldProjected, provenanceOf(oldEv), facts.conversationSnapshot, projected, provenanceOf(ev), snap) {
		noteStructuralCause(&baseline, causeSystemPrompt)
	}
	updated.baseline = baseline
	updated.conversationSnapshot = snap
	if ev != nil {
		updated.conversationProvenance = ev.Provenance
	}
	updated.conversationFilteredBaseline = filtered
	return updated, true
}

func cloneRefreshedMemoFacts(source recvTurnFacts) recvTurnFacts {
	return source.clone()
}

func conversationRevision(source recvTurnFacts) uint64 {
	return source.conversationSnapshot.StateRevision
}

// providerInstructionsChanged reports whether the effective provider-bound
// system/developer instructions differ between two canonical calls. It
// compares only that instruction content, for both legacy and item authority,
// and additionally honors genuine placement changes that move the retained
// instructions within the projected trajectory. The boolean result is the
// only thing that leaves this helper: no instruction text, content,
// identifier, revision, reason, or exclusion is published or retained.
func providerInstructionsChanged(
	before aipapi.Call, beforeProv []conversationprojection.OverlayProvenance, beforeSnap conversationprojection.Snapshot,
	after aipapi.Call, afterProv []conversationprojection.OverlayProvenance, afterSnap conversationprojection.Snapshot,
) bool {
	if !reflect.DeepEqual(providerInstructions(before), providerInstructions(after)) {
		return true
	}
	return effectiveTrajectoryChanged(before, beforeProv, beforeSnap, after, afterProv, afterSnap)
}

// provenanceOf returns the overlay placement evidence of a projection, or nil
// when the projection produced none.
func provenanceOf(ev *conversationprojection.ProjectionEvidence) []conversationprojection.OverlayProvenance {
	if ev == nil {
		return nil
	}
	return ev.Provenance
}

// effectiveTrajectoryChanged reports whether the provider-bound trajectory
// changed in a way attributable to system/developer placement. It compares
// the normalized full trajectories of the two projections after removing
// volatile elements: emissions of steering overlays that carry conversational
// content (any role other than system or developer) are removed by their exact
// projected positions, so a client duplicate sharing an overlay's text keeps
// its place; ingress elements excluded by a NeverBackend tag on either side are
// removed symmetrically without removing any projection-owned overlay emission
// that shares their identity, and item references dropped with an excluded
// message are dropped too. What remains is client-authored content plus
// system/developer overlay emissions, so any residual difference is a genuine
// move of an instruction within the trajectory — including moves whose
// resolved placement description changed without effect (which compare equal)
// and moves that only reorder identical content (which compare different
// exactly when the wire positions differ). The boolean result is the only thing
// that leaves this helper.
func effectiveTrajectoryChanged(
	before aipapi.Call, beforeProv []conversationprojection.OverlayProvenance, beforeSnap conversationprojection.Snapshot,
	after aipapi.Call, afterProv []conversationprojection.OverlayProvenance, afterSnap conversationprojection.Snapshot,
) bool {
	excluded := unionExclusionIdentities(beforeSnap, afterSnap)
	beforeSeq := cleanedTrajectory(before, beforeProv, beforeSnap, excluded)
	afterSeq := cleanedTrajectory(after, afterProv, afterSnap, excluded)
	return !reflect.DeepEqual(beforeSeq, afterSeq)
}

// trajectorySlot is one normalized element of the provider-bound trajectory.
// Region preserves whether legacy content reaches the instruction section,
// the message section, or the item stream; positions within the sequence
// capture effective placement.
type trajectorySlot struct {
	region  int // 0: legacy instructions, 1: legacy messages, 2: items
	message aipapi.Message
	item    aipapi.Item
	isItem  bool
}

// cleanedTrajectory returns the normalized provider-bound trajectory with
// volatile elements removed. Emissions of steering overlays that carry
// conversational content (any role other than system or developer, for example
// a thinker memo or an assistant note) are removed by their exact projected
// positions from the projection's own provenance — never by semantic identity —
// so a client message that happens to share an overlay's text is retained with
// its trajectory position intact. Elements carrying an identity excluded by
// either snapshot are removed symmetrically (their absence on one side is the
// exclusion itself, already handled by the instruction-content comparison
// when it removes an instruction), but only outside the projection's own
// overlay emissions: exclusion applies to the input trajectory before
// injection, so an emitted overlay is never itself excluded even when an
// ingress message shares its role and text. Item references that the
// projection would drop with an excluded message are dropped here too, so the
// dependency cleanup of a user-content exclusion is never mistaken for an
// instruction change. Message metadata is proxy-owned traceability and never
// reaches a provider payload, so it is excluded.
func cleanedTrajectory(call aipapi.Call, prov []conversationprojection.OverlayProvenance, snap conversationprojection.Snapshot, excluded map[conversationprojection.MessageIdentity]struct{}) []trajectorySlot {
	volatile := volatileOverlayEmissionIndices(prov, snap)
	overlayIndices := overlayEmissionIndices(prov)
	if call.HasItemAuthority() {
		return cleanedItemTrajectory(call.Items, volatile, overlayIndices, excluded)
	}
	excludedMessage := func(message aipapi.Message) bool {
		id, err := conversationprojection.MessageIdentityOf(message)
		if err != nil {
			return false
		}
		_, ok := excluded[id]
		return ok
	}
	slots := make([]trajectorySlot, 0, len(call.Instructions)+len(call.Messages))
	appendLegacy := func(region int, messages []aipapi.Message, offset *int) {
		for _, message := range messages {
			index := *offset
			*offset++
			_, volatileDropped := volatile[index]
			_, overlayOwned := overlayIndices[index]
			if volatileDropped || (!overlayOwned && excludedMessage(message)) {
				continue
			}
			message.Metadata = nil
			slots = append(slots, trajectorySlot{region: region, message: message})
		}
	}
	offset := 0
	appendLegacy(0, call.Instructions, &offset)
	appendLegacy(1, call.Messages, &offset)
	return slots
}

// cleanedItemTrajectory normalizes item authority. Besides the volatile and
// exclusion rules, it reproduces the projection's dependency cleanup: when a
// message item is excluded, every item_reference targeting its ID is removed as
// well. Only projection-cleaned ingress content is affected; overlay emissions
// are handled by their own positions.
func cleanedItemTrajectory(items []aipapi.Item, volatile, overlayOwned map[int]struct{}, excluded map[conversationprojection.MessageIdentity]struct{}) []trajectorySlot {
	excludedMessageID := func(item aipapi.Item) (string, bool) {
		if item.Kind != aipapi.ItemKindMessage {
			return "", false
		}
		id, err := conversationprojection.ItemIdentityOf(item)
		if err != nil {
			return "", false
		}
		if _, ok := excluded[id]; !ok {
			return "", false
		}
		return item.ID, true
	}
	droppedIDs := make(map[string]struct{})
	for i, item := range items {
		if _, skipped := volatile[i]; skipped {
			continue
		}
		if _, overlay := overlayOwned[i]; overlay {
			continue
		}
		if id, dropped := excludedMessageID(item); dropped && id != "" {
			droppedIDs[id] = struct{}{}
		}
	}
	slots := make([]trajectorySlot, 0, len(items))
	for i, item := range items {
		if _, skipped := volatile[i]; skipped {
			continue
		}
		if _, overlay := overlayOwned[i]; !overlay {
			if _, dropped := excludedMessageID(item); dropped {
				continue
			}
		}
		if item.Kind == aipapi.ItemKindItemReference && item.Reference != nil {
			if _, dropped := droppedIDs[item.Reference.ID]; dropped {
				continue
			}
		}
		slots = append(slots, trajectorySlot{region: 2, item: item, isItem: true})
	}
	return slots
}

// overlayEmissionIndices returns the projected trajectory positions of every
// projection-owned steering emission. Exclusion applies to the input trajectory
// before injection, so an injected overlay is recognizably proxy-owned content:
// its identity may equal a NeverBackend-tagged input message without the
// emission itself being excluded.
func overlayEmissionIndices(prov []conversationprojection.OverlayProvenance) map[int]struct{} {
	out := make(map[int]struct{}, len(prov))
	for _, p := range prov {
		out[p.InjectedTrajectoryIndex] = struct{}{}
	}
	return out
}

// volatileOverlayEmissionIndices returns the projected trajectory positions of
// emissions from active steering overlays that carry conversational content:
// every role other than system or developer. An empty role defaults to system,
// matching the projection. Positions come from the projection's own
// provenance, so removal targets the exact injected occurrence: a
// client-authored message with the same role or content keeps its place.
func volatileOverlayEmissionIndices(prov []conversationprojection.OverlayProvenance, snap conversationprojection.Snapshot) map[int]struct{} {
	volatileOverlays := make(map[string]struct{})
	for _, overlay := range snap.Steering {
		if !overlay.Active {
			continue
		}
		role := overlay.Message.Role
		if role == "" {
			role = aipapi.RoleSystem
		}
		if role == aipapi.RoleSystem || role == aipapi.RoleDeveloper {
			continue
		}
		volatileOverlays[overlay.OverlayID] = struct{}{}
	}
	out := make(map[int]struct{}, len(prov))
	for _, p := range prov {
		if _, ok := volatileOverlays[p.OverlayID]; !ok {
			continue
		}
		out[p.InjectedTrajectoryIndex] = struct{}{}
	}
	return out
}

// unionExclusionIdentities returns the NeverBackend identities tagged by
// either snapshot. Excluding them symmetrically keeps an exclusion from
// looking like a placement change; an exclusion that removes an instruction
// is already reported by the instruction-content comparison.
func unionExclusionIdentities(before, after conversationprojection.Snapshot) map[conversationprojection.MessageIdentity]struct{} {
	out := make(map[conversationprojection.MessageIdentity]struct{})
	for _, snap := range []conversationprojection.Snapshot{before, after} {
		for _, tag := range snap.NeverBackend {
			out[tag.Identity] = struct{}{}
		}
	}
	return out
}

// providerInstruction is one ordered system/developer instruction in the
// provider-bound trajectory. Region preserves whether legacy content reaches
// the instruction or message section; item authority uses its own region.
type providerInstruction struct {
	region  int
	message aipapi.Message
	item    aipapi.Item
	isItem  bool
}

// providerInstructions extracts the ordered system/developer instruction
// content from a projected canonical call. User-role material is ignored, even
// when projection places it in the instruction region. Message metadata is
// proxy-owned traceability and never reaches a provider payload, so it is
// excluded from the comparison. Item identity and lifecycle status identify
// the message envelope rather than its instruction content, so they are also
// excluded; the payload itself is compared exactly.
func providerInstructions(call aipapi.Call) []providerInstruction {
	instructions := make([]providerInstruction, 0, len(call.Instructions)+len(call.Messages)+len(call.Items))
	if call.HasItemAuthority() {
		for _, item := range call.Items {
			if item.Kind != aipapi.ItemKindMessage {
				continue
			}
			switch item.Role {
			case aipapi.RoleSystem, aipapi.RoleDeveloper:
				instructions = append(instructions, providerInstruction{region: 2, item: providerInstructionItem(item), isItem: true})
			}
		}
		return instructions
	}
	appendLegacy := func(region int, messages []aipapi.Message) {
		for _, message := range messages {
			switch message.Role {
			case aipapi.RoleSystem, aipapi.RoleDeveloper:
				instructions = append(instructions, providerInstruction{region: region, message: providerInstructionMessage(message)})
			}
		}
	}
	appendLegacy(0, call.Instructions)
	appendLegacy(1, call.Messages)
	return instructions
}

func memoProjectionIngress(source recvTurnFacts) aipapi.Call {
	return source.ingressCall
}

func memoProjectionBaseline(source recvTurnFacts) aipapi.Call {
	return source.baseline
}

func projectRefreshedMemoContext(ctx context.Context, source recvTurnFacts, log *slog.Logger) context.Context {
	return source.projectContext(ctx, log)
}

// memoStateVisibleToClient reports whether the currently linked memo was
// surfaced to the client during this logical turn.
func (e *Executor) memoStateVisibleToClient(ctx context.Context, aLegID string, state interleavedstate.State) bool {
	if e == nil || e.InterleavedProcessor == nil || aLegID == "" {
		return false
	}
	return e.InterleavedProcessor.IsMemoVisibleToClient(ctx, aLegID)
}

// withoutSteeringOverlay returns a copy of snap without overlays matching
// isTarget, so the immediate visible-mode continuation does not duplicate
// reasoning the client already saw. The match predicate is feature-owned;
// this sequencing helper knows no overlay identity.
func withoutSteeringOverlay(snap conversationprojection.Snapshot, isTarget func(string) bool) conversationprojection.Snapshot {
	if isTarget == nil {
		return snap
	}
	kept := make([]conversationprojection.Overlay, 0, len(snap.Steering))
	for _, ov := range snap.Steering {
		if isTarget(ov.OverlayID) {
			continue
		}
		kept = append(kept, ov)
	}
	snap.Steering = kept
	return snap
}
