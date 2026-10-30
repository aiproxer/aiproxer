package runtime

import (
	"strings"
	"sync"
)

// structuralCauseKey identifies the prompt-cache key a structural cause is
// published for.
//
// The prompt-cache collapse diagnostic keys its attribution state by session,
// backend and model. The session is constant within one request execution, so
// the backend and model identify the key for publication purposes: two
// attempts of one request that share a backend and a model share a cache key
// and therefore share one publication.
type structuralCauseKey struct {
	backend string
	model   string
}

// structuralCauseClaims records the cache keys one request execution already
// published a structural cause for.
//
// A structural cause names one change to the cached prefix. The request that
// made the change is the only request that may publish it, and it publishes it
// at most once per affected cache key: a later leg of the same request, or a
// retry of the same candidate, carries the same change and must not re-arm a
// cause that a healthy sample already proved stale.
//
// The claim set is owned by the request execution and lives exactly as long as
// that request, so it needs no retention or eviction policy of its own. The
// request's recovery controller holds it, and each attempt's request facts
// reference it. Every attempt of the request shares it — parallel candidates,
// replacements, retries and continuations included — so exactly one attempt per
// cache key publishes the cause, whatever order those attempts finish in. A different
// request execution has its own claim set, so its own cause is never
// suppressed by another request's publication.
//
// The request execution is the identity: no A-leg identifier (reused by
// resumed secure sessions), no content-derived trace identifier (shared by
// identical requests), and no billing or continuation identifier takes part in
// the decision.
type structuralCauseClaims struct {
	mu   sync.Mutex
	keys map[structuralCauseKey]struct{}
}

func newStructuralCauseClaims() *structuralCauseClaims {
	return &structuralCauseClaims{keys: make(map[structuralCauseKey]struct{}, 1)}
}

// publish takes the key for this request execution and reports whether the
// caller may publish a structural cause for it. Only the first caller for a
// key publishes; every later attempt of the same request is answered false.
func (c *structuralCauseClaims) publish(key structuralCauseKey) bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, claimed := c.keys[key]; claimed {
		return false
	}
	c.keys[key] = struct{}{}
	return true
}

// claimStructuralCause returns the structural cause this attempt of the
// request may publish for its cache key.
//
// An empty cause publishes nothing and claims nothing: a leg that carries no
// structural change must not consume the single publication its request has
// for that key. A cause that the request already published for that key is
// suppressed, because the change it names happened once and a later leg of
// the same request must not re-arm a cause that a healthy sample already
// proved stale.
func (f requestFacts) claimStructuralCause(cause, backend, model string) string {
	cause = strings.TrimSpace(cause)
	if cause == "" {
		return ""
	}
	key := structuralCauseKey{backend: strings.TrimSpace(backend), model: strings.TrimSpace(model)}
	if !f.structuralCause.publish(key) {
		return ""
	}
	return cause
}
