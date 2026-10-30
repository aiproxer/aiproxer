package app

import (
	"fmt"
	"strings"
	"sync"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// PinRelease releases a retained runtime-generation pin exactly once.
type PinRelease interface {
	Release()
}

type ownershipEntry struct {
	pin       PinRelease
	clearExec func()
}

// trackerSlot is the per-WorkID admission state under GenerationPinTracker.mu.
type trackerSlot struct {
	reservations int
	terminal     bool
	ownership    *ownershipEntry
}

// GenerationPinTracker holds process-owned runtime-generation pins keyed by
// durable terminal-work ID (req 10.3, 10.7). Each WorkID adopts at most one
// combined ownership entry containing an optional runtime pin and/or an
// optional executable-generation pending clear handle.
//
// Adoption is ordered with terminal transitions via BeginAdoption reservations:
// MarkTerminal under the same mutex prevents late Publish after the sole
// terminal callback has run.
type GenerationPinTracker struct {
	mu   sync.Mutex
	byID map[string]*trackerSlot
}

// NewGenerationPinTracker returns an empty tracker bounded by pending work.
func NewGenerationPinTracker() *GenerationPinTracker {
	return &GenerationPinTracker{byID: make(map[string]*trackerSlot)}
}

// AdoptionToken is an in-flight WorkID adoption reservation. BeginAdoption
// before append/replay lookup; End on every return path. Executable Bind must
// run only via PublishBound so winner selection and pending hold are atomic
// with MarkTerminal.
type AdoptionToken struct {
	tracker *GenerationPinTracker
	workID  string
	ended   bool
}

// OwnershipBinder creates optional executable-generation pending ownership for
// the eligible tracker winner. It runs while GenerationPinTracker.mu is held
// and must not re-enter the tracker or block on external I/O. Production
// binders are in-memory / snapshotgen only (task 3.6).
//
// created=true means this call established a hold and release must release it.
// created=false means an idempotent no-op; release must be nil / ignored.
type OwnershipBinder func() (release func(), created bool)

// BeginAdoption reserves workID for adoption under the tracker mutex. The
// returned token must End on every path. Nil tracker / empty workID yield a
// no-op token.
func (t *GenerationPinTracker) BeginAdoption(workID string) *AdoptionToken {
	if t == nil || workID == "" {
		return &AdoptionToken{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = make(map[string]*trackerSlot)
	}
	e := t.byID[workID]
	if e == nil {
		e = &trackerSlot{}
		t.byID[workID] = e
	}
	e.reservations++
	return &AdoptionToken{tracker: t, workID: workID}
}

// PublishBound atomically selects the tracker winner and invokes bind for that
// eligible candidate only. MarkTerminal cannot interleave between Bind and
// ownership publication. Losers release pin outside the lock and never call
// bind. Runtime-pin-only (bind creates nothing) and executable-only (pin=nil)
// ownership remain valid; reject only when both resulting resources are nil.
func (tok *AdoptionToken) PublishBound(pin PinRelease, bind OwnershipBinder) bool {
	if tok == nil || tok.tracker == nil || tok.workID == "" || tok.ended {
		unwindCandidate(pin, nil)
		return false
	}
	t := tok.tracker
	t.mu.Lock()
	e := t.byID[tok.workID]
	if e == nil || e.terminal || e.ownership != nil {
		t.mu.Unlock()
		unwindCandidate(pin, nil)
		return false
	}

	var clearExec func()
	var created bool
	var panicked bool
	if bind != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			clearExec, created = bind()
		}()
	}
	if panicked {
		t.mu.Unlock()
		if !created {
			clearExec = nil
		}
		unwindCandidate(pin, clearExec)
		return false
	}
	if !created {
		clearExec = nil
	}
	if pin == nil && clearExec == nil {
		t.mu.Unlock()
		return false
	}
	e.ownership = &ownershipEntry{pin: pin, clearExec: clearExec}
	t.mu.Unlock()
	return true
}

// Publish installs pre-built combined ownership under the reservation. Prefer
// PublishBound for production paths so executable Bind cannot race MarkTerminal.
// Rejects (and unwinds the candidate) when terminal was marked, a winner
// already exists, or the token is invalid/ended. Never clears a winner's
// resources.
func (tok *AdoptionToken) Publish(pin PinRelease, clearExec func()) bool {
	if tok == nil || tok.tracker == nil || tok.workID == "" || tok.ended {
		unwindCandidate(pin, clearExec)
		return false
	}
	if pin == nil && clearExec == nil {
		return false
	}
	t := tok.tracker
	t.mu.Lock()
	e := t.byID[tok.workID]
	if e == nil || e.terminal || e.ownership != nil {
		t.mu.Unlock()
		unwindCandidate(pin, clearExec)
		return false
	}
	e.ownership = &ownershipEntry{pin: pin, clearExec: clearExec}
	t.mu.Unlock()
	return true
}

// End releases the adoption reservation. When no reservations and no ownership
// remain, the WorkID slot (including a terminal tombstone) is removed so the
// tracker stays bounded by in-flight admission/ownership.
func (tok *AdoptionToken) End() {
	if tok == nil || tok.tracker == nil || tok.workID == "" || tok.ended {
		return
	}
	tok.ended = true
	t := tok.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.byID[tok.workID]
	if e == nil {
		return
	}
	if e.reservations > 0 {
		e.reservations--
	}
	t.maybeDeleteLocked(tok.workID, e)
}

// MarkTerminal records a definitive terminal transition before removing
// ownership. In-flight tokens cannot Publish afterward. Ownership is released
// outside the mutex with panic isolation. Tombstones without remaining
// reservations are dropped immediately.
func (t *GenerationPinTracker) MarkTerminal(workID string) {
	if t == nil || workID == "" {
		return
	}
	t.mu.Lock()
	if t.byID == nil {
		t.byID = make(map[string]*trackerSlot)
	}
	e := t.byID[workID]
	if e == nil {
		t.mu.Unlock()
		return
	}
	e.terminal = true
	var toRelease *ownershipEntry
	if e.ownership != nil {
		toRelease = e.ownership
		e.ownership = nil
	}
	if e.reservations == 0 {
		delete(t.byID, workID)
	}
	t.mu.Unlock()
	releaseOwnership(toRelease)
}

// Hold atomically registers combined ownership for workID exactly once when no
// terminal tombstone exists. Prefer BeginAdoption/Publish for accept/replay
// paths that race with MarkTerminal. Returns false when rejected; the caller
// must unwind the candidate and must not invoke clearExec against a winner.
func (t *GenerationPinTracker) Hold(workID string, pin PinRelease, clearExec func()) bool {
	if t == nil || workID == "" {
		return false
	}
	if pin == nil && clearExec == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = make(map[string]*trackerSlot)
	}
	e := t.byID[workID]
	if e == nil {
		e = &trackerSlot{}
		t.byID[workID] = e
	}
	if e.terminal || e.ownership != nil {
		return false
	}
	e.ownership = &ownershipEntry{pin: pin, clearExec: clearExec}
	return true
}

// Release drops combined ownership without marking terminal (nonterminal
// rollback / abort when durable append definitively did not happen). Does not
// create a terminal tombstone.
func (t *GenerationPinTracker) Release(workID string) {
	if t == nil || workID == "" {
		return
	}
	t.mu.Lock()
	e := t.byID[workID]
	var toRelease *ownershipEntry
	if e != nil && e.ownership != nil {
		toRelease = e.ownership
		e.ownership = nil
		t.maybeDeleteLocked(workID, e)
	}
	t.mu.Unlock()
	releaseOwnership(toRelease)
}

// AbortOwnership is the nonterminal abort seam: remove candidate ownership when
// durable append definitively did not happen. Alias of Release.
func (t *GenerationPinTracker) AbortOwnership(workID string) {
	t.Release(workID)
}

// OwnershipSafe reports whether an existing tracker entry already proves
// ownership is handled (published ownership or terminal tombstone), so a
// replay candidate may release without publishing a replacement hold.
func (t *GenerationPinTracker) OwnershipSafe(workID string) bool {
	if t == nil || workID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.byID[workID]
	if e == nil {
		return false
	}
	return e.terminal || e.ownership != nil
}

// Len reports outstanding held ownership entries (test/diagnostics).
func (t *GenerationPinTracker) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, e := range t.byID {
		if e != nil && e.ownership != nil {
			n++
		}
	}
	return n
}

// EntryCount reports tracked WorkID slots including terminal tombstones and
// in-flight reservations (test/diagnostics for bounded cleanup).
func (t *GenerationPinTracker) EntryCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byID)
}

// AdoptHold attempts Hold; on rejection the loser pin and clearExec (when
// provided by this candidate) are unwound. Never clears a winner hold.
func (t *GenerationPinTracker) AdoptHold(workID string, pin PinRelease, clearExec func()) bool {
	if t == nil || workID == "" || (pin == nil && clearExec == nil) {
		unwindCandidate(pin, clearExec)
		return false
	}
	if t.Hold(workID, pin, clearExec) {
		return true
	}
	unwindCandidate(pin, clearExec)
	return false
}

func (t *GenerationPinTracker) maybeDeleteLocked(workID string, e *trackerSlot) {
	if e == nil {
		return
	}
	if e.reservations == 0 && e.ownership == nil {
		delete(t.byID, workID)
	}
}

func unwindCandidate(pin PinRelease, clearExec func()) {
	if pin != nil {
		func() {
			defer func() { _ = recover() }()
			pin.Release()
		}()
	}
	if clearExec != nil {
		func() {
			defer func() { _ = recover() }()
			clearExec()
		}()
	}
}

func releaseOwnership(entry *ownershipEntry) {
	if entry == nil {
		return
	}
	if entry.pin != nil {
		func() {
			defer func() { _ = recover() }()
			entry.pin.Release()
		}()
	}
	if entry.clearExec != nil {
		func() {
			defer func() { _ = recover() }()
			entry.clearExec()
		}()
	}
}

// GenerationBoundResolver resolves effect providers from a retained runtime
// configuration generation's immutable request-plane/provider view.
// Explicit runtime-identity rows must fail closed when that exact
// instance+generation/provider cannot be resolved — never fall through to a
// newer process-global same-ID provider (task 3.6).
type GenerationBoundResolver interface {
	Resolve(runtimeInstanceID, runtimeGenerationID, providerID string, kind sdk.WorkKind) (EffectProvider, error)
}

// TerminalProviderView is an immutable generation-owned terminal-effect
// provider resolver. It never exposes mutable registries, Built, or config.
type TerminalProviderView interface {
	Resolve(providerID string, kind sdk.WorkKind) (EffectProvider, error)
}

// FrozenTerminalProviders is an immutable snapshot of terminal effect providers
// captured at generation compilation. Future candidates may differ without
// mutating prior generation views.
type FrozenTerminalProviders struct {
	byID      map[string]EffectProvider
	kindIndex map[string]map[sdk.WorkKind]struct{}
}

// SnapshotTerminalProviders copies providers from a Registry into an immutable view.
func SnapshotTerminalProviders(reg *Registry) *FrozenTerminalProviders {
	if reg == nil {
		return &FrozenTerminalProviders{
			byID:      map[string]EffectProvider{},
			kindIndex: map[string]map[sdk.WorkKind]struct{}{},
		}
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := &FrozenTerminalProviders{
		byID:      make(map[string]EffectProvider, len(reg.byID)),
		kindIndex: make(map[string]map[sdk.WorkKind]struct{}, len(reg.kindIndex)),
	}
	for id, p := range reg.byID {
		out.byID[id] = p
		kinds := reg.kindIndex[id]
		copied := make(map[sdk.WorkKind]struct{}, len(kinds))
		for k := range kinds {
			copied[k] = struct{}{}
		}
		out.kindIndex[id] = copied
	}
	return out
}

// Resolve implements TerminalProviderView.
func (v *FrozenTerminalProviders) Resolve(providerID string, kind sdk.WorkKind) (EffectProvider, error) {
	if v == nil {
		return nil, ErrMissingProvider
	}
	id := strings.TrimSpace(providerID)
	p, ok := v.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMissingProvider, id)
	}
	if _, ok := v.kindIndex[id][kind]; !ok {
		return nil, fmt.Errorf("%w: %s does not support %s", ErrUnsupportedKind, id, kind)
	}
	return p, nil
}

var _ TerminalProviderView = (*FrozenTerminalProviders)(nil)
