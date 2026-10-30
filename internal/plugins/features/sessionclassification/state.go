package sessionclassification

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

const (
	// MaxAuthorityIDBytes bounds proxy-owned identifiers retained by this feature.
	MaxAuthorityIDBytes = 256
	// MaxRemoteLeaseIDBytes bounds the opaque control token for a remote attempt.
	MaxRemoteLeaseIDBytes = 64
)

var (
	// ErrNoAuthority means the session view contains no proxy-owned scope.
	ErrNoAuthority = errors.New("session classification: no proxy authority")
	// ErrInvalidKey means an authority key is malformed or outside its fixed bound.
	ErrInvalidKey = errors.New("session classification: invalid authority key")
	// ErrInvalidProposal means a promotion is not a valid positive classification.
	ErrInvalidProposal = errors.New("session classification: invalid positive proposal")
	// ErrInvalidRemoteOptions means a remote attempt limit or duration is outside its finite bounds.
	ErrInvalidRemoteOptions = errors.New("session classification: invalid remote claim bounds")
	// ErrStoreCapacity means a new authoritative record cannot be admitted without evicting retained state.
	ErrStoreCapacity = errors.New("session classification: store capacity reached")
	// ErrStaleRemoteClaim means a completion no longer owns the active remote lease.
	ErrStaleRemoteClaim = errors.New("session classification: stale remote claim")
	// ErrInvalidRemoteClaim means a remote completion token is malformed.
	ErrInvalidRemoteClaim = errors.New("session classification: invalid remote claim")
	// ErrInvalidStoreContext means a store operation received a nil context.
	ErrInvalidStoreContext = errors.New("session classification: nil store context")
	// ErrInvalidStoreTime means a store operation received a zero timestamp.
	ErrInvalidStoreTime = errors.New("session classification: invalid store time")
	// ErrLeaseNonce means the bounded lease nonce could not be created or validated.
	ErrLeaseNonce = errors.New("session classification: remote lease nonce unavailable")
	// ErrLeaseSequenceExhausted means the process-local lease sequence has no remaining values.
	ErrLeaseSequenceExhausted = errors.New("session classification: remote lease sequence exhausted")
	// ErrInvalidStoreConfig means the in-memory adapter bounds are invalid.
	ErrInvalidStoreConfig = errors.New("session classification: invalid memory store bounds")
)

// ScopeKind identifies which proxy-owned authority issued an opaque ID.
type ScopeKind string

const (
	ScopeSecureSession ScopeKind = "secure_session"
	ScopeALeg          ScopeKind = "a_leg"
)

// Key scopes feature state to one proxy-owned secure session or A-leg.
// ScopeKind is part of the key so equal ID bytes from different authorities do
// not share state.
type Key struct {
	Kind ScopeKind
	ID   string
}

// ResolveKey prefers the proxy-owned secure session ID and falls back to the
// proxy-owned A-leg only when the secure session ID is exactly empty. It never
// reads ClientSessionHint and never rewrites an authoritative ID.
func ResolveKey(view session.SessionView) (Key, error) {
	if view.AuthoritativeSessionID != "" {
		key := Key{Kind: ScopeSecureSession, ID: view.AuthoritativeSessionID}
		if err := ValidateKey(key); err != nil {
			return Key{}, err
		}
		return key, nil
	}
	if view.ALegID == "" {
		return Key{}, ErrNoAuthority
	}
	key := Key{Kind: ScopeALeg, ID: view.ALegID}
	if err := ValidateKey(key); err != nil {
		return Key{}, err
	}
	return key, nil
}

// ValidateKey checks the bounded opaque authority without normalizing it.
func ValidateKey(key Key) error {
	if key.Kind != ScopeSecureSession && key.Kind != ScopeALeg {
		return ErrInvalidKey
	}
	if key.ID == "" || len(key.ID) > MaxAuthorityIDBytes || !utf8.ValidString(key.ID) || strings.TrimSpace(key.ID) == "" {
		return ErrInvalidKey
	}
	for _, r := range key.ID {
		if unicode.IsControl(r) {
			return ErrInvalidKey
		}
	}
	return nil
}

// Record is the bounded persisted feature state for one authority key. It
// contains classification and remote-control metadata only.
type Record struct {
	Key                  Key
	Classification       session.Classification
	RemoteAttempts       uint32
	RemoteLeaseID        string
	RemoteLeaseUntil     time.Time
	RemoteNextEligibleAt time.Time
	UpdatedAt            time.Time
}

// RemoteClaim proves ownership of one leased remote attempt. RetryBackoff is
// captured with the claim so completion can start backoff at completion time;
// abandoned claims become eligible again as soon as their lease expires.
type RemoteClaim struct {
	Key          Key
	LeaseID      string
	Attempt      uint32
	RetryBackoff time.Duration
}

// RemoteCompletion carries only a validated positive proposal. The zero value
// represents a below-threshold, unavailable, or otherwise non-positive result.
type RemoteCompletion struct {
	Proposal session.Classification
}

// Store is the consumed feature-owned persistence contract. Implementations
// must preserve first-positive-wins semantics and make remote lease operations
// atomic for each authority key.
type Store interface {
	Load(ctx context.Context, key Key) (Record, bool, error)

	Promote(ctx context.Context, key Key, proposal session.Classification, now time.Time) (record Record, promoted bool, err error)

	ClaimRemote(ctx context.Context, key Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (claim RemoteClaim, record Record, ok bool, err error)

	CompleteRemote(ctx context.Context, claim RemoteClaim, result RemoteCompletion, now time.Time) (Record, error)
}

// ValidatePositiveProposal checks that a promotion contains a valid positive snapshot.
func ValidatePositiveProposal(proposal session.Classification) error {
	if !proposal.IsCodingAgent() {
		return ErrInvalidProposal
	}
	return nil
}

// ValidateStoreContext rejects nil contexts and propagates cancellation.
func ValidateStoreContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidStoreContext
	}
	return ctx.Err()
}

// ValidateStoreTime rejects an absent timestamp before it enters state.
func ValidateStoreTime(now time.Time) error {
	if now.IsZero() {
		return ErrInvalidStoreTime
	}
	return nil
}

// RoundRemoteDeadlineUpToMicrosecond rounds a remote lease or retry deadline
// upward to the shared durable timestamp precision. Keeping expiry deadlines
// aligned lets SQLite, PostgreSQL, and memory stores enforce identical
// eligibility boundaries without allowing a deadline to expire early.
func RoundRemoteDeadlineUpToMicrosecond(deadline time.Time) time.Time {
	truncated := deadline.Truncate(time.Microsecond)
	if !truncated.Equal(deadline) {
		return truncated.Add(time.Microsecond)
	}
	return truncated
}
