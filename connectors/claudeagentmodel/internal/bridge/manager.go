package bridge

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Release releases one live-sidecar admission slot exactly once.
type Release interface {
	Release()
}

// Admission bounds live sidecars for one configured instance. The owning engine
// supplies it; the bridge never invents its own bound, so discovery probes and
// conversations share one authority.
type Admission interface {
	Acquire(ctx context.Context, wait time.Duration) (Release, error)
}

// AdmissionFunc adapts a function to Admission.
type AdmissionFunc func(ctx context.Context, wait time.Duration) (Release, error)

// Acquire implements Admission.
func (f AdmissionFunc) Acquire(ctx context.Context, wait time.Duration) (Release, error) {
	return f(ctx, wait)
}

// noAdmission is used by tests that do not bound live sidecars.
type noAdmission struct{}

func (noAdmission) Acquire(context.Context, time.Duration) (Release, error) { return nopRelease{}, nil }

type nopRelease struct{}

func (nopRelease) Release() {}

// Manager owns the bounded set of live conversations of one configured
// instance. A conversation is one sidecar child plus at most one parked native
// turn; other conversations never share it.
type Manager struct {
	mu         sync.Mutex
	admission  Admission
	pendingTTL time.Duration
	idleTTL    time.Duration
	turnWait   time.Duration
	now        func() time.Time

	byKey          map[string]*Session
	byCall         map[string]*Session
	byName         map[string]map[*Session]struct{}
	all            map[*Session]struct{}
	tombstones     map[string]tombstone
	nameTombstones map[nameTombstoneKey]nameTombstone
}

type tombstone struct {
	code string
	at   time.Time
}

// nameTombstoneKey retains replay evidence for one exact name-only continuation
// identity. A common tool name alone is never evidence: another conversation may
// legitimately use the same name with a different ordered prefix.
type nameTombstoneKey struct {
	name   string
	prefix [32]byte
	args   [32]byte
}

type nameTombstone struct {
	code string
	at   time.Time
}

const (
	// maxTombstones bounds the retained expiry/consumption markers.
	maxTombstones = 256
)

// NewManager builds the per-instance conversation manager. A nil admission
// admits every conversation, which is only appropriate for tests.
func NewManager(admission Admission, pendingTTL, idleTTL, turnWait time.Duration, now func() time.Time) *Manager {
	if admission == nil {
		admission = noAdmission{}
	}
	if pendingTTL <= 0 {
		pendingTTL = 30 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{
		admission:      admission,
		pendingTTL:     pendingTTL,
		idleTTL:        idleTTL,
		turnWait:       turnWait,
		now:            now,
		byKey:          make(map[string]*Session),
		byCall:         make(map[string]*Session),
		byName:         make(map[string]map[*Session]struct{}),
		all:            make(map[*Session]struct{}),
		tombstones:     make(map[string]tombstone),
		nameTombstones: make(map[nameTombstoneKey]nameTombstone),
	}
}

func callTombstoneKey(callID string) string { return "call:" + callID }

func (m *Manager) register(s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.key != "" {
		if existing := m.byKey[s.key]; existing != nil {
			if existing.hasPending() {
				return continuationError(CodeContinuationPending, "the proxy session already has a pending tool call")
			}
			return continuationError(CodeConversationBusy, "the proxy session already has an active conversation")
		}
		m.byKey[s.key] = s
	}
	m.all[s] = struct{}{}
	return nil
}

func (s *Session) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending != nil
}

func (m *Manager) indexPending(s *Session, pending *PendingCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if pending.CallID != "" {
		if existing, ok := m.byCall[pending.CallID]; ok {
			if existing != nil && existing != s {
				// Two conversations produced the same opaque id. Neither may be
				// guessed; keep the ambiguity explicit until one of them resolves.
				m.byCall[pending.CallID] = nil
			}
		} else {
			m.byCall[pending.CallID] = s
		}
		// A fresh pending call supersedes an old marker for the same opaque id.
		delete(m.tombstones, callTombstoneKey(pending.CallID))
	}
	if pending.Name != "" {
		owners := m.byName[pending.Name]
		if owners == nil {
			owners = make(map[*Session]struct{})
			m.byName[pending.Name] = owners
		}
		owners[s] = struct{}{}
		// Retained name evidence is intentionally not erased here. It is keyed by
		// the exact prefix and argument digest, so a genuinely advanced serial
		// call does not collide with the consumed/expired predecessor, while a
		// new conversation with an identical prefix cannot inherit a release.
	}
}

func (m *Manager) unregister(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.key != "" && m.byKey[s.key] == s {
		delete(m.byKey, s.key)
	}
	for id, owner := range m.byCall {
		if owner == s {
			delete(m.byCall, id)
		}
	}
	for name, owners := range m.byName {
		delete(owners, s)
		if len(owners) == 0 {
			delete(m.byName, name)
		}
	}
	delete(m.all, s)
	m.pruneAmbiguousCallLocked()
}

// pruneAmbiguousCallLocked removes a nil call-id collision marker once no live
// session still owns that pending id. The marker is only ambiguity evidence, not
// a permanent reservation.
func (m *Manager) pruneAmbiguousCallLocked() {
	for id, owner := range m.byCall {
		if owner != nil {
			continue
		}
		live := false
		for s := range m.all {
			if s.PendingCallID() == id {
				live = true
				break
			}
		}
		if !live {
			delete(m.byCall, id)
		}
	}
}

// consume records a successful single release and removes the pending indexes.
func (m *Manager) consume(s *Session, pending *PendingCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeOwnersLocked(s, pending)
	m.markTombstoneLocked(pending, CodeContinuationConsumed)
	m.pruneLocked()
}

// tombstone records why a pending call can no longer be released.
func (m *Manager) tombstone(s *Session, pending *PendingCall, code string) {
	if pending == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeOwnersLocked(s, pending)
	m.markTombstoneLocked(pending, code)
	m.pruneLocked()
}

func (m *Manager) removeOwnersLocked(s *Session, pending *PendingCall) {
	if pending.CallID != "" {
		if owner, ok := m.byCall[pending.CallID]; ok && owner == s {
			delete(m.byCall, pending.CallID)
		}
	}
	if pending.Name != "" {
		if owners := m.byName[pending.Name]; owners != nil {
			delete(owners, s)
			if len(owners) == 0 {
				delete(m.byName, pending.Name)
			}
		}
	}
}

func (m *Manager) markTombstoneLocked(pending *PendingCall, code string) {
	if pending.CallID != "" {
		if _, exists := m.tombstones[callTombstoneKey(pending.CallID)]; !exists {
			m.tombstones[callTombstoneKey(pending.CallID)] = tombstone{code: code, at: m.now()}
		}
	}
	if pending.Name != "" && pending.PrefixByName != ([32]byte{}) {
		key := nameTombstoneKey{name: pending.Name, prefix: pending.PrefixByName, args: pending.ArgsDigest}
		if _, exists := m.nameTombstones[key]; !exists {
			m.nameTombstones[key] = nameTombstone{code: code, at: m.now()}
		}
	}
}

// Lookup resolves the parked conversation a tool result continues. It never
// releases anything: the caller must validate model, catalogue and prefix
// before Resume.
func (m *Manager) Lookup(callID, sessionKey string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if mark, ok := m.tombstones[callTombstoneKey(callID)]; ok {
		return nil, continuationError(mark.code, "the tool call can no longer be released")
	}
	if sessionKey != "" {
		if s := m.byKey[sessionKey]; s != nil {
			return s, nil
		}
		// A host-issued session id that names no parked conversation (for
		// example a fresh per-request id) does not invalidate the stateless
		// exact-ID plus transcript-prefix proof below. It only adds binding when
		// it matches.
	}
	if s, ok := m.byCall[callID]; ok {
		if s == nil {
			return nil, continuationError(CodeAmbiguousContinuation, "two parked conversations share that tool call id")
		}
		return s, nil
	}
	switch len(m.byCall) {
	case 0:
		return nil, ErrNoPending
	case 1:
		return nil, continuationError(CodeContinuationMismatch, "the tool result does not match the pending native call")
	default:
		return nil, continuationError(CodeAmbiguousContinuation, "several parked conversations exist and none matches the tool result")
	}
}

// LookupName resolves a parked conversation for one selected name-only
// occurrence. The proof (ordered prefix plus argument digest) is part of the
// lookup: a consumed or expired marker for another prefix never shadows a live
// conversation. With identical live and retained evidence, stateless matching is
// ambiguous; a trusted session id may select the live conversation explicitly.
func (m *Manager) LookupName(proof NameProof, sessionKey string) (*Session, error) {
	proof.Name = strings.TrimSpace(proof.Name)
	if proof.Name == "" {
		return nil, continuationError(CodeContinuationMismatch, "the tool result has no tool name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()

	key := nameTombstoneKey{name: proof.Name, prefix: proof.Prefix, args: proof.ArgsDigest}
	if sessionKey != "" {
		if s := m.byKey[sessionKey]; s != nil {
			if s.nameProofMatches(proof) {
				return s, nil
			}
			if s.hasPending() {
				return nil, continuationError(CodeContinuationMismatch, "the proxy session does not hold that pending tool call")
			}
			if mark, ok := m.nameTombstones[key]; ok {
				return nil, nameTombstoneError(mark)
			}
			return nil, continuationError(CodeContinuationMismatch, "the proxy session does not hold that pending tool call")
		}
	}

	live := m.byName[proof.Name]
	exact := 0
	var exactSession *Session
	for s := range live {
		if s.nameProofMatches(proof) {
			exact++
			exactSession = s
		}
	}
	mark, hasMark := m.nameTombstones[key]
	switch {
	case len(live) == 0:
		if hasMark {
			return nil, nameTombstoneError(mark)
		}
		if len(m.byCall) == 0 && len(m.byName) == 0 {
			return nil, ErrNoPending
		}
		return nil, continuationError(CodeContinuationMismatch, "the tool result does not match a pending name-only call")
	case exact == 0:
		// The request is a genuine replay of a consumed/expired call, even when
		// unrelated conversations with the same name remain live.
		if hasMark {
			return nil, nameTombstoneError(mark)
		}
		if len(live) == 1 {
			return nil, continuationError(CodeContinuationMismatch, "the tool result does not match the pending name-only call")
		}
		return nil, continuationError(CodeAmbiguousContinuation, "several parked conversations share that tool name and none matches the request prefix")
	case exact == 1:
		if hasMark {
			// A live conversation and a consumed predecessor have the same visible
			// prefix and arguments. Without trusted binding the request could be
			// either; refuse rather than release the wrong conversation.
			return nil, continuationError(CodeAmbiguousContinuation, "a consumed and a live name-only call share the same visible prefix")
		}
		return exactSession, nil
	default:
		return nil, continuationError(CodeAmbiguousContinuation, "several parked conversations match that name-only prefix")
	}
}

// HasLiveName reports whether any live conversation currently holds a pending
// call with this name. It does not release or prove anything; the engine uses it
// only to decide whether a mixed suffix is a replay or a strict refusal.
func (m *Manager) HasLiveName(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byName[strings.TrimSpace(name)]) > 0
}

func nameTombstoneError(mark nameTombstone) error {
	return continuationError(mark.code, "the name-only tool call can no longer be released")
}

func (s *Session) nameProofMatches(proof NameProof) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.Name != proof.Name {
		return false
	}
	if proof.Prefix == ([32]byte{}) || s.pending.PrefixByName == ([32]byte{}) {
		return false
	}
	return PrefixesEqual(s.pending.PrefixByName, proof.Prefix) && s.pending.ArgsDigest == proof.ArgsDigest
}

// PendingCallID reports the pending call of one session, used by tests and
// diagnostics. It never releases anything.
func (s *Session) PendingCallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return ""
	}
	return s.pending.CallID
}

// PendingSnapshot returns a copy of the current pending call for validation by
// the owning engine. The copy is never used to release by itself; Resume
// revalidates the live pending entry under the session lock.
func (s *Session) PendingSnapshot() (PendingCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return PendingCall{}, false
	}
	return *s.pending, true
}

// LiveRuntimes reports the number of owned conversations.
func (m *Manager) LiveRuntimes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.all)
}

// Close retires every owned conversation exactly once.
func (m *Manager) Close() error {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.all))
	for s := range m.all {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.retire()
	}
	return nil
}

func (m *Manager) pruneLocked() {
	cutoff := m.now().Add(-2 * m.pendingTTL)
	for id, mark := range m.tombstones {
		if mark.at.Before(cutoff) {
			delete(m.tombstones, id)
		}
	}
	for key, mark := range m.nameTombstones {
		if mark.at.Before(cutoff) {
			delete(m.nameTombstones, key)
		}
	}
	for m.tombstoneCountLocked() > maxTombstones {
		m.dropOldestTombstoneLocked()
	}
}

func (m *Manager) tombstoneCountLocked() int {
	return len(m.tombstones) + len(m.nameTombstones)
}

func (m *Manager) dropOldestTombstoneLocked() {
	var (
		oldestAt   time.Time
		oldestID   string
		oldestName nameTombstoneKey
		isName     bool
		found      bool
	)
	for id, mark := range m.tombstones {
		if !found || mark.at.Before(oldestAt) {
			oldestAt, oldestID, isName, found = mark.at, id, false, true
		}
	}
	for key, mark := range m.nameTombstones {
		if !found || mark.at.Before(oldestAt) {
			oldestAt, oldestName, isName, found = mark.at, key, true, true
		}
	}
	if !found {
		return
	}
	if isName {
		delete(m.nameTombstones, oldestName)
		return
	}
	delete(m.tombstones, oldestID)
}
