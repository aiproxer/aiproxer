package credpool

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// noCopy is a field the same way as in [sync.WaitGroup]: go vet's copylocks
// analyzer reports accidental copies of [Pool] values (which would duplicate [sync.Mutex]).
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// Credential is one API key attached to a backend instance. ID is a non-secret
// operator-facing identifier; when empty, [New] assigns a pool-local opaque id
// (c0, c1, ...). IDs are never derived from secret material.
type Credential struct {
	ID                string
	Secret            string
	RemoteOrgID       string
	RemoteProjectID   string
	RemoteWorkspaceID string
	RemoteAccountID   string
	RemoteRegion      string
}

// State is usefulness of a credential for diagnostics only.
type State string

const (
	StateUsable      State = "usable"
	StateCooldown    State = "cooldown"
	StateAuthInvalid State = "auth_invalid"
)

// CredentialStatus is a secret-free view for tests and diagnostics.
type CredentialStatus struct {
	ID                string
	RemoteOrgID       string
	RemoteProjectID   string
	RemoteWorkspaceID string
	RemoteAccountID   string
	RemoteRegion      string
	State             State
	CooldownUntil     time.Time // zero means not in cooldown / n/a for usable
}

// Pool holds ordered credentials and per-credential usefulness state.
type Pool struct {
	noCopy noCopy
	mu     sync.Mutex
	creds  []poolEntry
}

type poolEntry struct {
	id                string
	secret            string
	remoteOrgID       string
	remoteProjectID   string
	remoteWorkspaceID string
	remoteAccountID   string
	remoteRegion      string
	cooldownUntil     time.Time // zero = no active cooldown
	isAuthInvalid     bool
}

// New builds a pool from normalized credentials (non-empty secrets).
func New(credentials []Credential) (*Pool, error) {
	if len(credentials) == 0 {
		return nil, errEmptyCredentialList
	}
	out := make([]poolEntry, 0, len(credentials))
	seenIDs := make(map[string]struct{}, len(credentials))
	for i, c := range credentials {
		if c.Secret == "" {
			return nil, fmt.Errorf("credpool: empty secret at index %d", i)
		}
		id := "c" + strconv.Itoa(i)
		if c.ID != "" {
			id = c.ID
		}
		if _, dup := seenIDs[id]; dup {
			return nil, fmt.Errorf("credpool: duplicate credential id %q", id)
		}
		seenIDs[id] = struct{}{}
		out = append(out, poolEntry{
			id:                id,
			secret:            c.Secret,
			remoteOrgID:       c.RemoteOrgID,
			remoteProjectID:   c.RemoteProjectID,
			remoteWorkspaceID: c.RemoteWorkspaceID,
			remoteAccountID:   c.RemoteAccountID,
			remoteRegion:      c.RemoteRegion,
		})
	}
	return &Pool{creds: out}, nil
}

// Acquire returns the first usable credential in registration order: not in
// exclude, not auth-invalid, and not in an active cooldown at now.
func (p *Pool) Acquire(now time.Time, exclude map[string]struct{}) (Credential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		e := &p.creds[i]
		if exclude != nil {
			if _, skip := exclude[e.id]; skip {
				continue
			}
		}
		if e.isAuthInvalid {
			continue
		}
		if !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now) {
			continue
		}
		return Credential{
			ID:                e.id,
			Secret:            e.secret,
			RemoteOrgID:       e.remoteOrgID,
			RemoteProjectID:   e.remoteProjectID,
			RemoteWorkspaceID: e.remoteWorkspaceID,
			RemoteAccountID:   e.remoteAccountID,
			RemoteRegion:      e.remoteRegion,
		}, nil
	}
	return Credential{}, ErrNoUsableCredential
}

// AcquireByID returns the credential with the given id when it is usable at now
// (not auth-invalid and not in an active cooldown).
func (p *Pool) AcquireByID(now time.Time, id string) (Credential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		e := &p.creds[i]
		if e.id != id {
			continue
		}
		if e.isAuthInvalid {
			return Credential{}, ErrNoUsableCredential
		}
		if !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now) {
			return Credential{}, ErrNoUsableCredential
		}
		return Credential{
			ID:                e.id,
			Secret:            e.secret,
			RemoteOrgID:       e.remoteOrgID,
			RemoteProjectID:   e.remoteProjectID,
			RemoteWorkspaceID: e.remoteWorkspaceID,
			RemoteAccountID:   e.remoteAccountID,
			RemoteRegion:      e.remoteRegion,
		}, nil
	}
	return Credential{}, ErrNoUsableCredential
}

// MarkRateLimited puts the credential in cooldown until the given instant.
// If the credential already has a cooldown that ends later than until, the existing
// deadline is kept (never shortens an in-flight cooldown).
func (p *Pool) MarkRateLimited(id string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		if p.creds[i].id == id {
			if cur := p.creds[i].cooldownUntil; cur.After(until) {
				until = cur
			}
			p.creds[i].cooldownUntil = until
			return
		}
	}
}

// MarkAuthInvalid permanently marks the credential unusable for auth reasons.
func (p *Pool) MarkAuthInvalid(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.creds {
		if p.creds[i].id == id {
			p.creds[i].isAuthInvalid = true
			return
		}
	}
}

// Snapshot returns secret-free status for every credential in order.
func (p *Pool) Snapshot(now time.Time) []CredentialStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]CredentialStatus, 0, len(p.creds))
	for i := range p.creds {
		e := p.creds[i]
		st := CredentialStatus{
			ID:                e.id,
			RemoteOrgID:       e.remoteOrgID,
			RemoteProjectID:   e.remoteProjectID,
			RemoteWorkspaceID: e.remoteWorkspaceID,
			RemoteAccountID:   e.remoteAccountID,
			RemoteRegion:      e.remoteRegion,
		}
		switch {
		case e.isAuthInvalid:
			st.State = StateAuthInvalid
		case !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now):
			st.State = StateCooldown
			st.CooldownUntil = e.cooldownUntil
		default:
			st.State = StateUsable
		}
		out = append(out, st)
	}
	return out
}
