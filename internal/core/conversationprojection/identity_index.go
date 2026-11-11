package conversationprojection

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// identityTrajectory is one trajectory slice together with the semantic identity
// derived once for each of its elements.
//
// MessageIdentityOf and ItemIdentityOf normalize an atom, marshal it and hash it
// with SHA-256, so identity derivation is the largest single cost of projection
// and reassertion: measured at roughly 78% of Reassert CPU on a 100-message
// history with 256 never_backend tags and two overlays. An operation consults the
// same trajectory up to seven times - ambiguity collision check, provenance
// removal, never_backend filtering, the remaining-identity check, extra-identity
// removal, anchor resolution, and the final projection - and every consult used
// to re-derive unchanged identities.
//
// Deriving once and carrying the table with the slice turns those later consults
// into lookups. The derivation error travels with the element too, so a phase
// that skips an unidentifiable message keeps skipping exactly that message, and
// the phases that fail closed still report the same derivation error.
type identityTrajectory[T any] struct {
	elems []T
	ids   []MessageIdentity
	errs  []error
}

func newIdentityTrajectory[T any](elems []T, identify func(T) (MessageIdentity, error)) identityTrajectory[T] {
	t := identityTrajectory[T]{
		elems: elems,
		ids:   make([]MessageIdentity, len(elems)),
		errs:  make([]error, len(elems)),
	}
	for i := range elems {
		t.ids[i], t.errs[i] = identify(elems[i])
	}
	return t
}

// newMessageItemTrajectory derives identities for message items only. Non-message
// items carry no message identity, and every item-authority pass of projection and
// reassertion skips them before asking for one, so their slots stay unclaimed here
// instead of paying a derivation that can only fail.
func newMessageItemTrajectory(call aipapi.Call) itemTrajectory {
	t := identityTrajectory[aipapi.Item]{
		elems: call.Items,
		ids:   make([]MessageIdentity, len(call.Items)),
		errs:  make([]error, len(call.Items)),
	}
	for i := range call.Items {
		if call.Items[i].Kind != aipapi.ItemKindMessage {
			// Still record the reason identity derivation does not apply, so an
			// unguarded caller fails exactly like ItemIdentityOf and never reads a
			// silently empty identity.
			t.errs[i] = errNonMessageItemIdentity
			continue
		}
		t.ids[i], t.errs[i] = ItemIdentityOf(call.Items[i])
	}
	return t
}

// errNonMessageItemIdentity is the shared reason a non-message item has no
// message identity. Sharing one value keeps the table build allocation-free per
// skipped item.
var errNonMessageItemIdentity = fmt.Errorf("%w: item has no message identity", ErrNonMessageItem)

func (t identityTrajectory[T]) len() int { return len(t.elems) }

// identity returns the identity derived for element i and the derivation error.
// A non-nil error means the element has no usable identity.
func (t identityTrajectory[T]) identity(i int) (MessageIdentity, error) {
	return t.ids[i], t.errs[i]
}

// retained returns the trajectory restricted to the elements selected by keep,
// carrying the identity already derived for each retained element. Keeping every
// element returns the receiver unchanged, because those phases would otherwise
// copy a trajectory they did not modify.
func (t identityTrajectory[T]) retained(keep []bool) identityTrajectory[T] {
	n := 0
	for _, ok := range keep {
		if ok {
			n++
		}
	}
	if n == len(t.elems) {
		return t
	}
	next := identityTrajectory[T]{
		elems: make([]T, 0, n),
		ids:   make([]MessageIdentity, 0, n),
		errs:  make([]error, 0, n),
	}
	for i := range t.elems {
		if !keep[i] {
			continue
		}
		next.elems = append(next.elems, t.elems[i])
		next.ids = append(next.ids, t.ids[i])
		next.errs = append(next.errs, t.errs[i])
	}
	return next
}

// legacyTrajectory is a legacy-authority call's instruction and message regions
// with their identities already derived.
type legacyTrajectory struct {
	instr identityTrajectory[aipapi.Message]
	msgs  identityTrajectory[aipapi.Message]
}

func newLegacyTrajectory(call aipapi.Call) legacyTrajectory {
	return legacyTrajectory{
		instr: newIdentityTrajectory(call.Instructions, MessageIdentityOf),
		msgs:  newIdentityTrajectory(call.Messages, MessageIdentityOf),
	}
}

// itemTrajectory is an item-authority call's trajectory with its identities
// already derived for the message items.
type itemTrajectory = identityTrajectory[aipapi.Item]

func newItemTrajectory(call aipapi.Call) itemTrajectory {
	return newMessageItemTrajectory(call)
}

// projectionInput carries the identities already derived for one call, so the
// projection that consumes them never re-hashes the same trajectory.
type projectionInput struct {
	itemAuthority bool
	legacy        legacyTrajectory
	items         itemTrajectory
}

// identifyCall derives the identities of the region the call's authority selects.
// Legacy instructions and messages stay separate regions because their order is
// part of the trajectory.
func identifyCall(call aipapi.Call) projectionInput {
	if call.HasItemAuthority() {
		return projectionInput{itemAuthority: true, items: newItemTrajectory(call)}
	}
	return projectionInput{legacy: newLegacyTrajectory(call)}
}

// applyTo writes the trajectories back to call after a phase replaced a slice,
// which keeps the call and its derived identities describing one trajectory.
func (in projectionInput) applyTo(call *aipapi.Call) {
	if in.itemAuthority {
		call.Items = in.items.elems
		return
	}
	call.Instructions = in.legacy.instr.elems
	call.Messages = in.legacy.msgs.elems
}

// anchorIndex resolves MessageAnchors against the identities of one region by
// lookup.
//
// Resolution used to scan - and re-derive - a whole region per overlay, so a
// conversation view with n anchored overlays cost n hashing passes over the
// trajectory. The index is built from the identity table the operation already
// holds, which makes each overlay's resolution cost independent of its position
// and of the number of overlays.
type anchorIndex struct {
	firstInvalid int
	invalidErr   error
	positions    map[MessageIdentity][]int
}

func newAnchorIndex(size int) anchorIndex {
	return anchorIndex{firstInvalid: -1, positions: make(map[MessageIdentity][]int, size)}
}

// add records one element's position, or the derivation failure of the first
// element that has no usable identity.
func (x *anchorIndex) add(pos int, id MessageIdentity, err error) {
	if err != nil {
		if x.firstInvalid < 0 {
			x.firstInvalid, x.invalidErr = pos, err
		}
		return
	}
	x.positions[id] = append(x.positions[id], pos)
}

// resolve answers a MessageAnchor against the indexed positions. The scan it
// replaces reported a derivation error only when the failing element came before
// the resolved position, so the index reproduces that order instead of failing a
// resolution the scan would have completed.
func (x anchorIndex) resolve(anchor MessageAnchor) (int, bool, error) {
	if err := anchor.Validate(); err != nil {
		return -1, false, err
	}
	list := x.positions[anchor.Identity]
	occ := int(anchor.Occurrence)
	if occ <= 0 || occ > len(list) {
		if x.firstInvalid >= 0 {
			return -1, false, x.invalidErr
		}
		return -1, false, nil
	}
	pos := list[occ-1]
	if x.firstInvalid >= 0 && x.firstInvalid < pos {
		return -1, false, x.invalidErr
	}
	return pos, true, nil
}

// resolveLegacy answers a legacy anchor, whose positions span the instruction
// region and then the message region, exactly like resolveAnchorLegacy.
func (x anchorIndex) resolveLegacy(instrLen int, anchor MessageAnchor) (isInstr bool, idx int, found bool, err error) {
	pos, found, err := x.resolve(anchor)
	if err != nil || !found {
		return false, -1, false, err
	}
	if pos < instrLen {
		return true, pos, true, nil
	}
	return false, pos - instrLen, true, nil
}

// newLegacyAnchorIndex indexes an instruction region followed by a message
// region, which is the order resolveAnchorLegacy scans.
func newLegacyAnchorIndex(instr, msgs identityTrajectory[aipapi.Message]) anchorIndex {
	x := newAnchorIndex(instr.len() + msgs.len())
	for i := range instr.elems {
		id, err := instr.identity(i)
		x.add(i, id, err)
	}
	offset := instr.len()
	for i := range msgs.elems {
		id, err := msgs.identity(i)
		x.add(offset+i, id, err)
	}
	return x
}

// newItemAnchorIndex indexes message items only: non-message items carry no
// message identity, and resolveAnchorInItems skips them the same way.
func newItemAnchorIndex(t itemTrajectory) anchorIndex {
	x := newAnchorIndex(t.len())
	for i := range t.elems {
		if t.elems[i].Kind != aipapi.ItemKindMessage {
			continue
		}
		id, err := t.identity(i)
		x.add(i, id, err)
	}
	return x
}

// resolveItems answers a MessageAnchor against the indexed item positions,
// matching resolveAnchorInItems.
func (x anchorIndex) resolveItems(anchor MessageAnchor) (int, bool, error) {
	pos, found, err := x.resolve(anchor)
	if err != nil || !found {
		return -1, false, err
	}
	return pos, true, nil
}
