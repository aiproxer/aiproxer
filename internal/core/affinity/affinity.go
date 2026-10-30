package affinity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

var ErrIdentityRequired = errors.New("affinity: identity required")

type MissingIdentityPolicy string

const (
	MissingIdentityIgnore     MissingIdentityPolicy = "ignore"
	MissingIdentityFailClosed MissingIdentityPolicy = "fail_closed"
)

type Scope string

const (
	ScopeSession Scope = "session"
	ScopeClient  Scope = "client"
)

type Key struct {
	Scope Scope
	ID    string
}

func (k Key) Valid() bool {
	return k.Scope != "" && strings.TrimSpace(k.ID) != ""
}

type Binding struct {
	Key          Key
	BackendID    string
	CandidateKey string
	Model        string
	UpdatedAt    time.Time
	Reason       string
}

type Store interface {
	Get(ctx context.Context, key Key) (Binding, bool, error)
	Set(ctx context.Context, binding Binding) error
	Delete(ctx context.Context, key Key) error
}

type IdentityInput struct {
	Mode                  routing.AffinityMode
	Session               session.SessionView
	Principal             execview.PrincipalView
	MissingIdentityPolicy MissingIdentityPolicy
}

func ResolveKey(in IdentityInput) (Key, bool, error) {
	switch in.Mode {
	case routing.AffinityNone:
		return Key{}, false, nil
	case routing.AffinitySession:
		id := strings.TrimSpace(in.Session.AuthoritativeSessionID)
		if id == "" {
			id = strings.TrimSpace(in.Session.ClientSessionHint)
		}
		if id == "" {
			return missingIdentity(in.MissingIdentityPolicy)
		}
		return Key{Scope: ScopeSession, ID: id}, true, nil
	case routing.AffinityClient:
		id := strings.TrimSpace(in.Principal.ID)
		if id == "" {
			return missingIdentity(in.MissingIdentityPolicy)
		}
		return Key{Scope: ScopeClient, ID: id}, true, nil
	default:
		return Key{}, false, nil
	}
}

func missingIdentity(policy MissingIdentityPolicy) (Key, bool, error) {
	if policy == MissingIdentityFailClosed {
		return Key{}, false, ErrIdentityRequired
	}
	return Key{}, false, nil
}

func BindingFromCandidate(key Key, cand routing.AttemptCandidate, now time.Time, reason string) Binding {
	return Binding{
		Key:          key,
		BackendID:    strings.TrimSpace(cand.Primary.Backend),
		CandidateKey: strings.TrimSpace(cand.Key),
		Model:        strings.TrimSpace(cand.Primary.Model),
		UpdatedAt:    now,
		Reason:       strings.TrimSpace(reason),
	}
}
