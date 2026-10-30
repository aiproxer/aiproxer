// Package geoip contains process-owned Country MMDB infrastructure.
package geoip

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

// Reader is the narrow active-reader capability used by Service.
type Reader interface {
	LookupCountry(netip.Addr) (country string, found bool, err error)
	Close() error
}

// Service owns one active reader and serializes publication/close with in-flight
// update operations. Generations receive only the non-owning lookup capability.
type Service struct {
	readerMu  sync.RWMutex
	active    Reader
	version   string
	updatedAt time.Time

	lifecycleMu   sync.Mutex
	closed        bool
	updateCtx     context.Context
	cancel        context.CancelFunc
	updateWG      sync.WaitGroup
	schedulerWG   sync.WaitGroup
	publicationWG sync.WaitGroup
}

// New creates a service around an already validated reader.
func New(reader Reader) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{active: reader, updateCtx: ctx, cancel: cancel}
}

// OpenLocal opens and structurally verifies one operator-owned MMDB without
// constructing a managed updater or performing network I/O.
func OpenLocal(path string) (*Service, error) {
	reader, err := openMMDB(path)
	if err != nil {
		return nil, err
	}
	return New(reader), nil
}

// LookupCountry holds the reader read lease through decode, so Close and
// publication cannot close the underlying mapped file while it is in use.
func (s *Service) LookupCountry(addr netip.Addr) (string, bool, error) {
	if s == nil {
		return "", false, fmt.Errorf("geoip: nil service")
	}
	s.readerMu.RLock()
	defer s.readerMu.RUnlock()
	if s.active == nil {
		return "", false, fmt.Errorf("geoip: country database is not ready")
	}
	return s.active.LookupCountry(addr)
}

// Ready reports whether a validated active reader is installed.
func (s *Service) Ready() bool {
	if s == nil {
		return false
	}
	s.lifecycleMu.Lock()
	closed := s.closed
	s.readerMu.RLock()
	ready := s.active != nil && !closed
	s.readerMu.RUnlock()
	s.lifecycleMu.Unlock()
	return ready
}

// Status is bounded process-owned readiness information.
type Status struct {
	Ready     bool
	Version   string
	UpdatedAt time.Time
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.lifecycleMu.Lock()
	closed := s.closed
	s.readerMu.RLock()
	status := Status{Ready: s.active != nil && !closed, Version: s.version, UpdatedAt: s.updatedAt}
	s.readerMu.RUnlock()
	s.lifecycleMu.Unlock()
	return status
}

// Publish swaps in a validated reader. The old reader is closed only after the
// write lock drains all lookups that could still reference it. The publication
// lease also makes direct Publish calls safe against Close; updater calls are
// additionally covered by RunOwnedUpdate.
func (s *Service) Publish(ctx context.Context, next Reader) error {
	return s.publish(ctx, next, "", false, nil)
}

// PublishVersion publishes a reader and records a bounded version token as one
// lifecycle- fenced operation. Recording the status in the same publication
// lease prevents Close from observing a newly published reader with stale
// metadata (or metadata written after shutdown).
func (s *Service) PublishVersion(ctx context.Context, next Reader, version string) error {
	return s.publish(ctx, next, version, true, nil)
}

// PublishVersionWithCommit executes the durable manifest commit and the
// in-memory reader publication under one lifecycle publication fence. The
// commit callback must not publish the reader itself; on failure next is closed
// and the previous active reader remains selected.
func (s *Service) PublishVersionWithCommit(ctx context.Context, next Reader, version string, commit func() error) error {
	return s.publish(ctx, next, version, true, commit)
}

func (s *Service) publish(ctx context.Context, next Reader, version string, recordVersion bool, commit func() error) error {
	if s == nil || next == nil {
		return fmt.Errorf("geoip: nil service or reader")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		_ = next.Close()
		return fmt.Errorf("geoip: service is closed")
	}
	select {
	case <-ctx.Done():
		s.lifecycleMu.Unlock()
		_ = next.Close()
		return ctx.Err()
	default:
	}
	// Close establishes the closed bit while holding lifecycleMu and waits for
	// this lease before touching readers. Add is therefore ordered against Wait.
	s.publicationWG.Add(1)
	defer s.publicationWG.Done()
	// Keep lifecycleMu held across the durable commit and the short swap. Close
	// cannot establish closed state between the admission check and publication.
	if commit != nil {
		if err := commit(); err != nil {
			_ = next.Close()
			s.lifecycleMu.Unlock()
			return err
		}
	}

	// Only the reader lock protects active-reader lookup/retirement. No I/O is
	// performed in this critical section.
	s.readerMu.Lock()
	old := s.active
	s.active = next
	if recordVersion {
		s.version = strings.TrimSpace(version)
		s.updatedAt = time.Now().UTC()
	}
	s.readerMu.Unlock()
	s.lifecycleMu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

// RunOwnedUpdate registers an updater operation and serializes its final
// publication fence with Close. The callback must perform download/verify work
// without holding readerMu and may call Publish only before returning.
func (s *Service) RunOwnedUpdate(ctx context.Context, fn func(context.Context) error) error {
	if s == nil || fn == nil {
		return fmt.Errorf("geoip: nil service or update function")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("geoip: service is closed")
	}
	s.updateWG.Add(1)
	root := s.updateCtx
	s.lifecycleMu.Unlock()
	defer s.updateWG.Done()
	if root == nil {
		root = context.Background()
	}
	operation, cancel := context.WithCancel(root)
	defer cancel()
	return fn(joinContexts(operation, ctx))
}

func joinContexts(a, b context.Context) context.Context {
	if b == nil {
		return a
	}
	ctx, cancel := context.WithCancel(a)
	go func() {
		select {
		case <-b.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}

// StartManagedUpdater starts the process-owned periodic update loop. The loop
// uses bounded jitter and stops through the service lifecycle context.
func (s *Service) StartManagedUpdater(updater *ManagedUpdater, interval time.Duration) error {
	if s == nil || updater == nil {
		return fmt.Errorf("geoip: nil managed updater")
	}
	if interval < coregeoip.MinUpdateInterval || interval > coregeoip.MaxUpdateInterval {
		return fmt.Errorf("geoip: managed interval must be between %s and %s", coregeoip.MinUpdateInterval, coregeoip.MaxUpdateInterval)
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return fmt.Errorf("geoip: service is closed")
	}
	root := s.updateCtx
	s.schedulerWG.Add(1)
	s.lifecycleMu.Unlock()
	go func() {
		defer s.schedulerWG.Done()
		next := interval
		for {
			timer := time.NewTimer(jitterInterval(next))
			select {
			case <-root.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			_, _ = updater.Update(root)
		}
	}()
	return nil
}

func jitterInterval(interval time.Duration) time.Duration {
	// A deterministic per-process phase is sufficient to avoid a fleet-wide
	// exact timer boundary without introducing an attacker-controlled source.
	phase := float64((time.Now().UnixNano()%2001)-1000) / 10000
	return time.Duration(float64(interval) * (1 + phase))
}

// Close establishes the closed state first, waits owned update operations, and
// only then closes the active reader. It is idempotent.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.lifecycleMu.Unlock()
	s.schedulerWG.Wait()
	s.updateWG.Wait()
	s.publicationWG.Wait()
	s.readerMu.Lock()
	old := s.active
	s.active = nil
	s.readerMu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}
