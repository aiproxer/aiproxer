package proxymemory

import (
	"container/list"
	"sync"
)

const defaultDecisionCacheCap = 4096

type decisionCacheKey struct {
	tenantID    string
	principalID string
	aLegID      string
}

// decisionState is the monotone publication state of one cached decision.
//
// The state only moves forward:
//
//	decisionPending  the decision is cached but was never published to the
//	                 writer queue (or its publication is in flight).
//	decisionQueued   the decision was published to the writer queue.
//	decisionFailed   the decision will never become durable: the queue refused
//	                 it, the write failed, or the store refused it.
//
// A failed decision is terminal. A later producer mark can never resurrect it,
// so a consumer failure is never overwritten by the producer that published
// the job (design §9).
type decisionState uint8

const (
	decisionPending decisionState = iota
	decisionQueued
	decisionFailed
)

// DecisionStatus is the publication state of one cached decision, as seen by
// a consumer that may authorize an injection or a capture. The set is closed,
// so a consumer can never read a terminal failure as a usable decision:
// DecisionAbsent is no cached decision, DecisionPending is a cached decision
// that was never published to the writer queue, DecisionQueued is one that was
// published (its durable write may still be in flight), and DecisionFailed is
// one that will never become durable: the queue refused it, the write failed,
// or the store refused it. The failed state is terminal and authorizes
// neither an injection nor a capture.
//
// A durable decision is not a cache status: the durable store is its
// authority, and the current durable read wins over any local state.
type DecisionStatus uint8

const (
	// DecisionAbsent means no cached decision exists for the identity.
	DecisionAbsent DecisionStatus = iota
	// DecisionPending means the cached decision was never published to the
	// writer queue.
	DecisionPending
	// DecisionQueued means the cached decision was published to the writer
	// queue.
	DecisionQueued
	// DecisionFailed means the cached decision will never become durable.
	DecisionFailed
)

// decisionCacheEntry is the cached decision and its position in the LRU order.
type decisionCacheEntry struct {
	job   FirstTurnJob
	state decisionState
	elem  *list.Element
}

// DecisionCache shares immutable first-turn decisions across generations. It is
// process-owned and bounded: a replay cache, never the authority for owner,
// binding, A-leg or epoch checks.
type DecisionCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[decisionCacheKey]*decisionCacheEntry
	order    *list.List
}

func NewDecisionCache(capacity int) *DecisionCache {
	if capacity <= 0 {
		capacity = defaultDecisionCacheCap
	}
	return &DecisionCache{
		capacity: capacity,
		entries:  make(map[decisionCacheKey]*decisionCacheEntry, capacity),
		order:    list.New(),
	}
}

// GetStatus returns the immutable decision of one identity and its
// publication status. The status is the only authority a consumer has on the
// cached decision: a terminal failure is never reported as a usable decision,
// so a resumed request can never replay a decision that will not become
// durable.
func (c *DecisionCache) GetStatus(p Principal, aLegID string) (FirstTurnJob, DecisionStatus) {
	if c == nil {
		return FirstTurnJob{}, DecisionAbsent
	}
	key := decisionCacheKey{tenantID: p.TenantID, principalID: p.PrincipalID, aLegID: aLegID}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return FirstTurnJob{}, DecisionAbsent
	}
	c.order.MoveToFront(entry.elem)
	switch entry.state {
	case decisionQueued:
		return entry.job, DecisionQueued
	case decisionFailed:
		return entry.job, DecisionFailed
	default:
		return entry.job, DecisionPending
	}
}

func (c *DecisionCache) Put(job FirstTurnJob) {
	if c == nil {
		return
	}
	key := decisionCacheKey{
		tenantID:    job.Principal.TenantID,
		principalID: job.Principal.PrincipalID,
		aLegID:      job.ALegID,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok {
		// A superseded decision keeps the publication state only when it is the
		// same decision. A different decision starts over from pending: the
		// previous publication says nothing about the new one.
		if sameFirstTurnJob(entry.job, job) {
			entry.job = job
		} else {
			entry.job = job
			entry.state = decisionPending
		}
		c.order.MoveToFront(entry.elem)
		return
	}
	entry := &decisionCacheEntry{job: job, state: decisionPending}
	c.entries[key] = entry
	entry.elem = c.order.PushFront(key)
	for len(c.entries) > c.capacity {
		c.remove(c.order.Back())
	}
}

// Admit publishes the decision to the writer queue and records the queued
// state in one atomic step: the state transition and the publication happen
// under the cache lock, so the consumer can never fail the decision before the
// producer recorded that it was queued.
//
// publish must not block: it is called with the cache lock held. It reports
// whether the decision entered the writer queue; a refused publication moves
// the decision to its terminal failed state.
func (c *DecisionCache) Admit(job FirstTurnJob, publish func() bool) bool {
	if c == nil {
		return false
	}
	key := decisionCacheKey{tenantID: job.Principal.TenantID, principalID: job.Principal.PrincipalID, aLegID: job.ALegID}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !sameFirstTurnJob(entry.job, job) {
		// The decision is not (or no longer) the cached one: publishing it
		// would authorize a capture for a decision nobody can validate.
		return false
	}
	if entry.state == decisionFailed {
		// A failed decision is terminal.
		return false
	}
	entry.state = decisionQueued
	if publish != nil && !publish() {
		entry.state = decisionFailed
		return false
	}
	return true
}

// MarkWriteFailed stops an unbound cached decision from authorizing capture.
// The transition is terminal: a later MarkQueued can never resurrect it.
func (c *DecisionCache) MarkWriteFailed(job FirstTurnJob) {
	if c == nil {
		return
	}
	key := decisionCacheKey{tenantID: job.Principal.TenantID, principalID: job.Principal.PrincipalID, aLegID: job.ALegID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok && sameFirstTurnJob(entry.job, job) {
		entry.state = decisionFailed
	}
}

func sameFirstTurnJob(a, b FirstTurnJob) bool {
	return a.Principal == b.Principal && a.ALegID == b.ALegID && a.Project == b.Project &&
		a.Decision == b.Decision && a.ErasureEpoch == b.ErasureEpoch && a.EpochKnown == b.EpochKnown
}

// Forget removes cached decisions for one erased bank before the durable Forget.
func (c *DecisionCache) Forget(owner Owner) {
	c.evict(func(job FirstTurnJob) bool {
		return job.Principal.TenantID == owner.TenantID &&
			job.Principal.PrincipalID == owner.PrincipalID &&
			job.Project.Key == owner.ProjectKey
	})
}

// RetireALeg evicts all cached owners for one retired A-leg.
func (c *DecisionCache) RetireALeg(aLegID string) {
	c.evict(func(job FirstTurnJob) bool { return job.ALegID == aLegID })
}

func (c *DecisionCache) evict(match func(FirstTurnJob) bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for elem := c.order.Back(); elem != nil; {
		prev := elem.Prev()
		key, ok := elem.Value.(decisionCacheKey)
		if ok {
			if entry := c.entries[key]; entry != nil && match(entry.job) {
				c.remove(elem)
			}
		}
		elem = prev
	}
}

func (c *DecisionCache) remove(elem *list.Element) {
	if elem == nil {
		return
	}
	key, ok := elem.Value.(decisionCacheKey)
	if !ok {
		return
	}
	delete(c.entries, key)
	c.order.Remove(elem)
}
