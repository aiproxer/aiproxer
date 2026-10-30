package sessionclassification

import (
	"context"
	"errors"
	"sync"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/uptrace/bun"
)

var (
	// ErrStateHolderClosed reports use after process shutdown has started.
	ErrStateHolderClosed = errors.New("session classification: process state holder is closed")
	// ErrStateHolderConfig reports an invalid process state holder configuration.
	ErrStateHolderConfig = errors.New("session classification: invalid process state holder configuration")
	// ErrStateNotInitialized reports that no enabled generation has prepared the
	// shared classification state yet. Resolving state never initializes it.
	ErrStateNotInitialized = errors.New("session classification: process state is not initialized")
)

type schemaStore interface {
	featurestate.Store
	EnsureSchema(context.Context) error
}

type holderFactories struct {
	newMemoryStore func() (featurestate.Store, error)
	newBunStore    func(*bun.DB) (schemaStore, error)
	newCoordinator func(featurestate.Store, featurestate.Observer) (*Coordinator, error)
}

func defaultHolderFactories() holderFactories {
	return holderFactories{
		newMemoryStore: func() (featurestate.Store, error) {
			return NewMemoryStore(MemoryStoreConfig{})
		},
		newBunStore: func(db *bun.DB) (schemaStore, error) {
			return NewBunStore(db)
		},
		newCoordinator: func(store featurestate.Store, observer featurestate.Observer) (*Coordinator, error) {
			return NewCoordinator(store, CoordinatorConfig{Observer: observer})
		},
	}
}

type holderInitialization struct {
	done            chan struct{}
	err             error // Genuine initialization failure or process shutdown rejection.
	ownerContextErr error // The initializer's context was canceled before publication.
}

// StateHolder owns one process-wide store and coordinator shared by every
// enabled classification generation. Construction is intentionally lazy: it
// stores borrowed capabilities and performs no schema, network, or state work.
type StateHolder struct {
	mu sync.Mutex

	db           *bun.DB
	collector    *PrometheusCollector
	factories    holderFactories
	store        featurestate.Store
	coordinator  *Coordinator
	initializing *holderInitialization
	owners       int
	closing      bool
	closed       bool
}

// NewStateHolder creates the lightweight process-owned shell. The Bun DB is
// borrowed and is never closed by this holder.
func NewStateHolder(db *bun.DB, collector *PrometheusCollector) *StateHolder {
	return newStateHolder(db, collector, defaultHolderFactories())
}

func newStateHolder(db *bun.DB, collector *PrometheusCollector, factories holderFactories) *StateHolder {
	return &StateHolder{db: db, collector: collector, factories: factories}
}

// Acquire initializes the selected store on the first enabled generation and
// records one generation owner after initialization succeeds. Concurrent
// callers share one in-progress initialization without holding the holder lock
// during schema I/O.
func (h *StateHolder) Acquire(ctx context.Context) error {
	if h == nil {
		return ErrStateHolderConfig
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		if err := ctx.Err(); err != nil {
			h.mu.Unlock()
			return err
		}
		if h.closing || h.closed {
			h.mu.Unlock()
			return ErrStateHolderClosed
		}
		if h.coordinator != nil {
			h.owners++
			h.mu.Unlock()
			return nil
		}
		if active := h.initializing; active != nil {
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-active.done:
				if err := ctx.Err(); err != nil {
					return err
				}
				if errors.Is(active.err, ErrStateHolderClosed) {
					return active.err
				}
				if active.ownerContextErr != nil {
					// The attempt failed while its owner was canceled. A live
					// waiter can retry with its own context, even when the store
					// adapter returns a bounded error that hides cancellation.
					continue
				}
				if active.err != nil {
					return active.err
				}
			}
			continue
		}

		active := &holderInitialization{done: make(chan struct{})}
		h.initializing = active
		h.mu.Unlock()

		store, coordinator, initializeErr := h.initialize(ctx)

		h.mu.Lock()
		active.ownerContextErr = ctx.Err()
		switch {
		case h.closing || h.closed:
			active.err = ErrStateHolderClosed
		case initializeErr != nil:
			active.err = initializeErr
		case active.ownerContextErr != nil:
			// Initialization may finish successfully while its owner is
			// canceled. Do not publish state or an owner in that case.
		default:
			h.store = store
			h.coordinator = coordinator
			h.owners++
			h.collector.setStoreReady(true)
		}
		h.initializing = nil
		close(active.done)
		resultErr := active.err
		if resultErr == nil {
			resultErr = active.ownerContextErr
		}
		h.mu.Unlock()
		return resultErr
	}
}

func (h *StateHolder) initialize(ctx context.Context) (featurestate.Store, *Coordinator, error) {
	var (
		store featurestate.Store
		err   error
	)
	if h.db == nil {
		if h.factories.newMemoryStore == nil {
			return nil, nil, ErrStateHolderConfig
		}
		store, err = h.factories.newMemoryStore()
	} else {
		if h.factories.newBunStore == nil {
			return nil, nil, ErrStateHolderConfig
		}
		var durable schemaStore
		durable, err = h.factories.newBunStore(h.db)
		if err == nil {
			if durable == nil {
				return nil, nil, ErrStateHolderConfig
			}
			err = durable.EnsureSchema(ctx)
			store = durable
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if store == nil || h.factories.newCoordinator == nil {
		return nil, nil, ErrStateHolderConfig
	}
	coordinator, err := h.factories.newCoordinator(store, h.Observer())
	if err != nil {
		return nil, nil, err
	}
	if coordinator == nil {
		return nil, nil, ErrStateHolderConfig
	}
	return store, coordinator, nil
}

// Release drops one generation's ownership while retaining the shared store
// and positive cache for overlapping or later enabled generations.
func (h *StateHolder) Release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.owners > 0 {
		h.owners--
	}
	h.mu.Unlock()
}

// Coordinator returns the initialized process-wide coordinator, if available.
func (h *StateHolder) Coordinator() *Coordinator {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	return h.coordinator
}

// Observer returns the process-owned bounded observation sink for classification.
// It is the same collector the metrics registry registered, so a generation's
// classifier and the process coordinator record into one bounded surface. The
// sink performs no feature work and is safe to resolve before any enabled
// generation has initialized state.
func (h *StateHolder) Observer() featurestate.Observer {
	if h == nil || h.collector == nil {
		return nil
	}
	return h.collector
}

// ClassificationState resolves the shared process coordinator for a
// generation-bound classifier. It satisfies the feature-owned state-authority
// contract and is deliberately read-only: composing or calling a classifier
// never initializes feature state, so a candidate generation that never reaches
// candidate prepare leaves no schema or network dependency behind.
func (h *StateHolder) ClassificationState() (featurestate.Store, error) {
	if h == nil {
		return nil, ErrStateHolderConfig
	}
	coordinator := h.Coordinator()
	if coordinator == nil {
		return nil, ErrStateNotInitialized
	}
	return coordinator, nil
}

// Close releases the holder's references exactly once. Stores and DB clients
// are not closed here because memory state is GC-owned and Bun DB is borrowed.
func (h *StateHolder) Close() error {
	if h == nil {
		return nil
	}
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil
		}
		if active := h.initializing; active != nil {
			h.closing = true
			h.mu.Unlock()
			<-active.done
			continue
		}
		h.closing = true
		h.closed = true
		h.owners = 0
		h.store = nil
		h.coordinator = nil
		h.collector.setStoreReady(false)
		h.mu.Unlock()
		return nil
	}
}
