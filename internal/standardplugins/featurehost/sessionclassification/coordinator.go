package sessionclassification

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

const (
	defaultCoordinatorCacheCapacity = 4096
	defaultCoordinatorMaxInflight   = 1024
	maxCoordinatorEntries           = 65536
	defaultCoordinatorIdleTTL       = 30 * time.Minute
	maxCoordinatorIdleTTL           = 30 * 24 * time.Hour
	coordinatorIdleEvictionBudget   = 16
)

// ErrInvalidCoordinatorConfig reports a missing store or non-finite coordinator bounds.
var ErrInvalidCoordinatorConfig = errors.New("session classification: invalid coordinator config")

// CoordinatorConfig sets finite bounds for the process-local cache and
// same-key operation coordination. Zero values select a cache capacity of
// 4096, an in-flight capacity of 1024, and a 30-minute idle TTL.
type CoordinatorConfig struct {
	// CacheCapacity bounds retained positive records; capacity pressure evicts only local cache entries.
	CacheCapacity int
	// MaxInflight bounds keyed Load and Promote operations; new keys bypass coordination when full.
	MaxInflight int
	// IdleTTL expires unused positive cache entries during later cache operations.
	IdleTTL time.Duration
	// Now supplies cache timestamps and is called before taking the coordinator lock.
	Now func() time.Time
	// Observer receives one bounded store observation per operation. Nil keeps
	// the coordinator observation-free.
	Observer featurestate.Observer
}

// Coordinator wraps the feature-owned authoritative state store. Its cache
// retains only positive classification records; all unknown turns remain
// visible to future Loads through the authoritative store.
type Coordinator struct {
	mu sync.Mutex

	store    featurestate.Store
	config   CoordinatorConfig
	now      func() time.Time
	observer featurestate.Observer

	cache    map[featurestate.Key]*list.Element
	cacheLRU list.List
	flights  map[flightKey]*operationFlight
}

type cachedPositive struct {
	key      featurestate.Key
	record   featurestate.Record
	lastUsed time.Time
}

type flightKind uint8

const (
	flightLoad flightKind = iota + 1
	flightPromote
)

type flightKey struct {
	key  featurestate.Key
	kind flightKind
}

type flightResult struct {
	record          featurestate.Record
	found           bool
	promoted        bool
	err             error
	ownerContextErr error
}

type operationFlight struct {
	done   chan struct{}
	result flightResult
}

var _ featurestate.Store = (*Coordinator)(nil)

// NewCoordinator creates a bounded process-local coordinator without
// performing storage I/O or taking ownership of the supplied store.
func NewCoordinator(store featurestate.Store, config CoordinatorConfig) (*Coordinator, error) {
	if store == nil {
		return nil, ErrInvalidCoordinatorConfig
	}
	if config.CacheCapacity == 0 {
		config.CacheCapacity = defaultCoordinatorCacheCapacity
	}
	if config.CacheCapacity < 1 || config.CacheCapacity > maxCoordinatorEntries {
		return nil, ErrInvalidCoordinatorConfig
	}
	if config.MaxInflight == 0 {
		config.MaxInflight = defaultCoordinatorMaxInflight
	}
	if config.MaxInflight < 1 || config.MaxInflight > maxCoordinatorEntries {
		return nil, ErrInvalidCoordinatorConfig
	}
	if config.IdleTTL == 0 {
		config.IdleTTL = defaultCoordinatorIdleTTL
	}
	if config.IdleTTL < 0 || config.IdleTTL > maxCoordinatorIdleTTL {
		return nil, ErrInvalidCoordinatorConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Coordinator{
		store:    store,
		config:   config,
		now:      config.Now,
		observer: config.Observer,
		cache:    make(map[featurestate.Key]*list.Element, config.CacheCapacity),
		flights:  make(map[flightKey]*operationFlight, min(config.MaxInflight, 16)),
	}, nil
}

// Load returns a cached positive or loads the current authoritative record.
// Unknown results are never retained between calls.
func (c *Coordinator) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	record, found, err := c.load(ctx, key)
	c.observeStore(featurestate.StoreObservation{
		Operation: featurestate.StoreOperationLoad,
		Outcome:   loadOutcome(record, err),
	})
	return record, found, err
}

// loadOutcome maps a read to the closed bounded outcome vocabulary: a positive
// projection is a hit, anything else without an error is a miss, and a bounded
// durable failure is an error.
func loadOutcome(record featurestate.Record, err error) featurestate.StoreOutcome {
	switch {
	case err != nil:
		return featurestate.StoreOutcomeError
	case record.Classification.IsCodingAgent():
		return featurestate.StoreOutcomeHit
	default:
		return featurestate.StoreOutcomeMiss
	}
}

func (c *Coordinator) load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}

	id := flightKey{key: key, kind: flightLoad}
	for {
		if err := ctx.Err(); err != nil {
			return featurestate.Record{}, false, err
		}
		now := c.now()
		c.mu.Lock()
		if record, ok := c.cachedPositiveLocked(key, now); ok {
			c.mu.Unlock()
			return record, true, nil
		}
		if active := c.flights[id]; active != nil {
			c.mu.Unlock()
			result, retry := c.waitForFlight(ctx, active)
			if retry {
				continue
			}
			if err := ctx.Err(); err != nil {
				return featurestate.Record{}, false, err
			}
			if record, ok := c.cachedPositive(key, c.now()); ok {
				return record, true, nil
			}
			if result.err != nil {
				return featurestate.Record{}, false, result.err
			}
			return result.record, result.found, nil
		}
		active := c.reserveFlightLocked(id)
		c.mu.Unlock()

		if active == nil {
			return c.loadWithoutFlight(ctx, key)
		}
		record, found, err := c.store.Load(ctx, key)
		result := flightResult{record: record, found: found, err: err, ownerContextErr: ctx.Err()}
		if err == nil && found {
			c.cachePositive(key, record, c.now())
		}
		c.finishFlight(id, active, result)
		if err != nil {
			if cached, ok := c.cachedPositive(key, c.now()); ok {
				return cached, true, nil
			}
			return featurestate.Record{}, false, err
		}
		if cached, ok := c.cachedPositive(key, c.now()); ok {
			return cached, true, nil
		}
		return record, found, nil
	}
}

func (c *Coordinator) loadWithoutFlight(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	record, found, err := c.store.Load(ctx, key)
	if err != nil {
		if cached, ok := c.cachedPositive(key, c.now()); ok {
			return cached, true, nil
		}
		return featurestate.Record{}, false, err
	}
	if found {
		c.cachePositive(key, record, c.now())
	}
	if cached, ok := c.cachedPositive(key, c.now()); ok {
		return cached, true, nil
	}
	return record, found, nil
}

// Promote atomically delegates a first-positive proposal and caches its stable
// result for later turns.
func (c *Coordinator) Promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	record, promoted, err := c.promote(ctx, key, proposal, now)
	c.observeStore(featurestate.StoreObservation{
		Operation: featurestate.StoreOperationPromote,
		Outcome:   promoteOutcome(record, promoted, err),
	})
	return record, promoted, err
}

// promoteOutcome maps a promotion attempt to the closed bounded outcome
// vocabulary. First-positive-wins and an unchanged winner both report
// unchanged, so a replayed proposal never looks like a second transition.
func promoteOutcome(record featurestate.Record, promoted bool, err error) featurestate.StoreOutcome {
	switch {
	case err != nil:
		return featurestate.StoreOutcomeError
	case promoted && record.Classification.IsCodingAgent():
		return featurestate.StoreOutcomeApplied
	default:
		return featurestate.StoreOutcomeUnchanged
	}
}

func (c *Coordinator) promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidatePositiveProposal(proposal); err != nil {
		return featurestate.Record{}, false, err
	}

	id := flightKey{key: key, kind: flightPromote}
	for {
		if err := ctx.Err(); err != nil {
			return featurestate.Record{}, false, err
		}
		clockNow := c.now()
		c.mu.Lock()
		if record, ok := c.cachedPositiveLocked(key, clockNow); ok {
			c.mu.Unlock()
			return record, false, nil
		}
		if active := c.flights[id]; active != nil {
			c.mu.Unlock()
			result, retry := c.waitForFlight(ctx, active)
			if retry {
				continue
			}
			if err := ctx.Err(); err != nil {
				return featurestate.Record{}, false, err
			}
			if record, ok := c.cachedPositive(key, c.now()); ok {
				return record, false, nil
			}
			if result.err != nil {
				return featurestate.Record{}, false, result.err
			}
			return result.record, false, nil
		}
		active := c.reserveFlightLocked(id)
		c.mu.Unlock()

		if active == nil {
			return c.promoteWithoutFlight(ctx, key, proposal, now)
		}
		record, promoted, err := c.store.Promote(ctx, key, proposal, now)
		result := flightResult{record: record, promoted: promoted, found: err == nil, err: err, ownerContextErr: ctx.Err()}
		if err == nil {
			c.cachePositive(key, record, c.now())
		}
		c.finishFlight(id, active, result)
		if err != nil {
			if cached, ok := c.cachedPositive(key, c.now()); ok {
				return cached, false, nil
			}
			return featurestate.Record{}, false, err
		}
		if cached, ok := c.cachedPositive(key, c.now()); ok {
			return cached, promoted, nil
		}
		return record, promoted, nil
	}
}

func (c *Coordinator) promoteWithoutFlight(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	record, promoted, err := c.store.Promote(ctx, key, proposal, now)
	if err != nil {
		if cached, ok := c.cachedPositive(key, c.now()); ok {
			return cached, false, nil
		}
		return featurestate.Record{}, false, err
	}
	c.cachePositive(key, record, c.now())
	if cached, ok := c.cachedPositive(key, c.now()); ok {
		return cached, promoted, nil
	}
	return record, promoted, nil
}

// ClaimRemote delegates attempt-budget and lease authority to Store. A cached
// positive short-circuits a new claim after validating its inputs.
func (c *Coordinator) ClaimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	claim, record, ok, err := c.claimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
	c.observeStore(featurestate.StoreObservation{
		Operation: featurestate.StoreOperationRemoteClaim,
		Outcome:   claimOutcome(ok, err),
	})
	return claim, record, ok, err
}

// claimOutcome maps a remote lease claim to the closed bounded outcome
// vocabulary. A refused claim is bounded denial, not an error.
func claimOutcome(ok bool, err error) featurestate.StoreOutcome {
	switch {
	case err != nil:
		return featurestate.StoreOutcomeError
	case ok:
		return featurestate.StoreOutcomeApplied
	default:
		return featurestate.StoreOutcomeDenied
	}
}

func (c *Coordinator) claimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if maxAttempts == 0 || maxAttempts > featurestate.MaxRemoteAttemptsPerSession || leaseTTL <= 0 || leaseTTL > featurestate.MaxRemoteLeaseTTL || retryBackoff < 0 || retryBackoff > featurestate.MaxRemoteRetryBackoff {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrInvalidRemoteOptions
	}
	if record, ok := c.cachedPositive(key, c.now()); ok {
		return featurestate.RemoteClaim{}, record, false, nil
	}

	claim, record, ok, err := c.store.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
	if err == nil {
		c.cachePositive(key, record, c.now())
	}
	return claim, record, ok, err
}

// CompleteRemote delegates lease completion and retains any accepted positive
// record, including a current positive returned with ErrStaleRemoteClaim.
func (c *Coordinator) CompleteRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	record, err := c.completeRemote(ctx, claim, result, now)
	outcome := featurestate.StoreOutcomeUnchanged
	switch {
	case err != nil && !errors.Is(err, featurestate.ErrStaleRemoteClaim):
		outcome = featurestate.StoreOutcomeError
	case record.Classification.IsCodingAgent():
		outcome = featurestate.StoreOutcomeApplied
	}
	c.observeStore(featurestate.StoreObservation{
		Operation: featurestate.StoreOperationRemoteComplete,
		Outcome:   outcome,
	})
	return record, err
}

// observeStore records one bounded durable operation result when a bounded
// observation sink is configured. The operation and outcome are already closed
// enum values, and the observation itself is a struct that cannot carry request
// content, so no identity or payload can reach the observation surface here.
func (c *Coordinator) observeStore(observation featurestate.StoreObservation) {
	if c == nil || c.observer == nil {
		return
	}
	c.observer.ObserveStore(observation)
}

func (c *Coordinator) completeRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	record, err := c.store.CompleteRemote(ctx, claim, result, now)
	if err == nil || errors.Is(err, featurestate.ErrStaleRemoteClaim) {
		c.cachePositive(claim.Key, record, c.now())
	}
	return record, err
}

func (c *Coordinator) reserveFlightLocked(id flightKey) *operationFlight {
	if len(c.flights) >= c.config.MaxInflight {
		return nil
	}
	active := &operationFlight{done: make(chan struct{})}
	c.flights[id] = active
	return active
}

func (c *Coordinator) finishFlight(id flightKey, active *operationFlight, result flightResult) {
	active.result = result
	c.mu.Lock()
	if c.flights[id] == active {
		delete(c.flights, id)
	}
	close(active.done)
	c.mu.Unlock()
}

func (c *Coordinator) waitForFlight(ctx context.Context, active *operationFlight) (flightResult, bool) {
	select {
	case <-ctx.Done():
		return flightResult{err: ctx.Err()}, false
	case <-active.done:
		if err := ctx.Err(); err != nil {
			return flightResult{err: err}, false
		}
		result := active.result
		if result.err != nil && result.ownerContextErr != nil && ctx.Err() == nil {
			return flightResult{}, true
		}
		return result, false
	}
}

func (c *Coordinator) cachedPositive(key featurestate.Key, now time.Time) (featurestate.Record, bool) {
	c.mu.Lock()
	record, ok := c.cachedPositiveLocked(key, now)
	c.mu.Unlock()
	return record, ok
}

func (c *Coordinator) cachedPositiveLocked(key featurestate.Key, now time.Time) (featurestate.Record, bool) {
	c.evictIdleLocked(now)
	element := c.cache[key]
	if element == nil {
		return featurestate.Record{}, false
	}
	entry, ok := element.Value.(*cachedPositive)
	if !ok {
		c.removeCacheEntryLocked(element)
		return featurestate.Record{}, false
	}
	if !now.Before(entry.lastUsed.Add(c.config.IdleTTL)) {
		c.removeCacheEntryLocked(element)
		return featurestate.Record{}, false
	}
	entry.lastUsed = now
	c.cacheLRU.MoveToFront(element)
	return entry.record, true
}

func (c *Coordinator) cachePositive(key featurestate.Key, record featurestate.Record, now time.Time) {
	if record.Key != key || !record.Classification.IsCodingAgent() {
		return
	}
	c.mu.Lock()
	c.evictIdleLocked(now)
	if element := c.cache[key]; element != nil {
		if entry, ok := element.Value.(*cachedPositive); ok {
			entry.lastUsed = now
			c.cacheLRU.MoveToFront(element)
			c.mu.Unlock()
			return
		}
		c.removeCacheEntryLocked(element)
	}
	for len(c.cache) >= c.config.CacheCapacity {
		element := c.cacheLRU.Back()
		if element == nil {
			break
		}
		c.removeCacheEntryLocked(element)
	}
	element := c.cacheLRU.PushFront(&cachedPositive{key: key, record: record, lastUsed: now})
	c.cache[key] = element
	c.mu.Unlock()
}

func (c *Coordinator) evictIdleLocked(now time.Time) {
	for range coordinatorIdleEvictionBudget {
		element := c.cacheLRU.Back()
		if element == nil {
			return
		}
		entry, ok := element.Value.(*cachedPositive)
		if !ok {
			c.removeCacheEntryLocked(element)
			continue
		}
		if now.Before(entry.lastUsed.Add(c.config.IdleTTL)) {
			return
		}
		c.removeCacheEntryLocked(element)
	}
}

func (c *Coordinator) removeCacheEntryLocked(element *list.Element) {
	if entry, ok := element.Value.(*cachedPositive); ok {
		delete(c.cache, entry.key)
	} else {
		for key, candidate := range c.cache {
			if candidate == element {
				delete(c.cache, key)
				break
			}
		}
	}
	c.cacheLRU.Remove(element)
}
