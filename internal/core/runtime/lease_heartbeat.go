package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// leaseHeartbeat owns one request-scoped renew loop. Stop is idempotent.
type leaseHeartbeat struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	degraded atomic.Bool
}

func newLeaseHeartbeat() *leaseHeartbeat {
	return &leaseHeartbeat{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

func (h *leaseHeartbeat) Stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopCh) })
	<-h.doneCh
}

func (h *leaseHeartbeat) Degraded() bool {
	return h != nil && h.degraded.Load()
}

func (e *Executor) startLeaseHeartbeat(parent context.Context, st *requestAuthorityState) {
	coord := e.requestCoordinatorFor(st)
	if e == nil || st == nil || coord == nil || coord.Concurrency == nil {
		return
	}
	targets := append([]leaseRenewTarget(nil), st.LeaseTargets...)
	if len(targets) == 0 {
		leaseID := st.LeaseID
		if leaseID == "" {
			leaseID = st.Decision.Lease.LeaseID
		}
		if leaseID == "" && st.LeaseSetID == "" {
			return
		}
		gen := st.LeaseGeneration
		if gen == 0 {
			gen = st.Decision.Lease.Generation
		}
		expires := st.LeaseExpiresAt
		if expires.IsZero() {
			expires = st.Decision.Lease.ExpiresAt
		}
		targets = []leaseRenewTarget{{
			LeaseID:         leaseID,
			Generation:      gen,
			ExpiresAt:       expires,
			RenewBefore:     st.RenewBefore,
			TTL:             st.LeaseTTL,
			FailureBehavior: st.FailureBehavior,
		}}
	}

	defaultRenewBefore := st.RenewBefore
	if defaultRenewBefore <= 0 {
		defaultRenewBefore = st.Decision.Lease.RenewBefore
	}
	if defaultRenewBefore <= 0 {
		defaultRenewBefore = e.ConcurrencyRenewBefore
	}
	if defaultRenewBefore <= 0 {
		defaultRenewBefore = 15 * time.Second
	}
	defaultTTL := st.LeaseTTL
	if defaultTTL <= 0 {
		defaultTTL = st.Decision.Lease.TTL
	}
	if defaultTTL <= 0 {
		defaultTTL = e.ConcurrencyLeaseTTL
	}
	if defaultTTL <= 0 {
		defaultTTL = time.Minute
	}
	defaultFB := st.FailureBehavior
	if defaultFB == "" {
		defaultFB = st.Decision.Lease.FailureBehavior
	}
	if defaultFB == "" {
		defaultFB = authority.FailureFailClosed
	}

	for i := range targets {
		if targets[i].RenewBefore <= 0 {
			targets[i].RenewBefore = defaultRenewBefore
		}
		if targets[i].TTL <= 0 {
			targets[i].TTL = defaultTTL
		}
		if targets[i].FailureBehavior == "" {
			targets[i].FailureBehavior = defaultFB
		}
	}

	st.LeaseTargets = targets
	st.LeaseIDs = make([]string, 0, len(targets))
	for _, t := range targets {
		st.LeaseIDs = append(st.LeaseIDs, t.LeaseID)
	}
	if st.LeaseID == "" && len(targets) > 0 {
		st.LeaseID = targets[0].LeaseID
		st.LeaseGeneration = targets[0].Generation
		st.LeaseExpiresAt = targets[0].ExpiresAt
	}
	if st.RenewBefore <= 0 {
		st.RenewBefore = targets[0].RenewBefore
	}
	if st.LeaseTTL <= 0 {
		st.LeaseTTL = targets[0].TTL
	}
	if st.FailureBehavior == "" {
		st.FailureBehavior = targets[0].FailureBehavior
	}

	hb := newLeaseHeartbeat()
	st.heartbeat = hb
	reqID := st.RequestID
	setID := st.LeaseSetID
	cleanupTimeout := coord.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = 2 * time.Second
	}

	go func() {
		defer close(hb.doneCh)
		live := append([]leaseRenewTarget(nil), targets...)
		setGen := st.LeaseGeneration
		for {
			wait := nextRenewWait(live)
			timer := time.NewTimer(wait)
			select {
			case <-hb.stopCh:
				timer.Stop()
				return
			case <-parent.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-hb.stopCh:
				return
			default:
			}

			failClosed := false
			anyFailOpenRetry := false
			rctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupTimeout)
			if setID != "" {
				primary := live[0]
				ttl := primary.TTL
				if ttl <= 0 {
					ttl = defaultTTL
				}
				rb := primary.RenewBefore
				if rb <= 0 {
					rb = defaultRenewBefore
				}
				dec, err := coord.RenewLease(rctx, authority.LeaseRenew{
					SetID:              setID,
					LeaseID:            st.LeaseID,
					RequestID:          reqID,
					ExpectedGeneration: setGen,
					TTL:                ttl,
					RenewBefore:        rb,
				})
				cancel()
				behavior := primary.FailureBehavior
				if behavior == "" {
					behavior = defaultFB
				}
				if err != nil || dec.Kind != authority.LeaseAllow {
					hb.degraded.Store(true)
					if behavior == authority.FailureFailClosed {
						failClosed = true
					} else {
						anyFailOpenRetry = true
					}
				} else {
					if dec.Generation > 0 {
						setGen = dec.Generation
						st.LeaseGeneration = dec.Generation
					}
					if !dec.ExpiresAt.IsZero() {
						st.LeaseExpiresAt = dec.ExpiresAt
						for i := range live {
							live[i].ExpiresAt = dec.ExpiresAt
							live[i].Generation = setGen
						}
					}
					st.LeaseTargets = append([]leaseRenewTarget(nil), live...)
				}
			} else {
				tickOK := true
				pending := append([]leaseRenewTarget(nil), live...)
				for i := range pending {
					src := live[i]
					t := &pending[i]
					dec, err := coord.RenewLease(rctx, authority.LeaseRenew{
						LeaseID:            src.LeaseID,
						RequestID:          reqID,
						ExpectedGeneration: src.Generation,
						TTL:                src.TTL,
						RuleID:             src.RuleID,
					})
					behavior := src.FailureBehavior
					if behavior == "" {
						behavior = defaultFB
					}
					if err != nil || dec.Kind != authority.LeaseAllow {
						tickOK = false
						hb.degraded.Store(true)
						if behavior == authority.FailureFailClosed {
							failClosed = true
						} else {
							anyFailOpenRetry = true
						}
						continue
					}
					if dec.Generation > 0 {
						t.Generation = dec.Generation
					}
					if !dec.ExpiresAt.IsZero() {
						t.ExpiresAt = dec.ExpiresAt
					}
				}
				cancel()
				if tickOK {
					live = pending
					if primary := findTarget(live, st.LeaseID); primary != nil {
						st.LeaseGeneration = primary.Generation
						st.LeaseExpiresAt = primary.ExpiresAt
					} else if len(live) > 0 {
						st.LeaseGeneration = live[0].Generation
						st.LeaseExpiresAt = live[0].ExpiresAt
					}
					st.LeaseTargets = append([]leaseRenewTarget(nil), live...)
				}
			}
			if failClosed {
				if err := e.markLeaseSetUncertain(parent, st); err != nil {
					st.LeaseSetUncertainErr = err
				}
				_ = e.acceptLeaseSetReleaseIntent(parent, st, "renew_fail_closed")
				if st.cancelRequest != nil {
					st.cancelRequest()
				}
				return
			}
			if anyFailOpenRetry {
				retryWait := minFailOpenRetry(live, defaultRenewBefore)
				retry := time.NewTimer(retryWait)
				select {
				case <-hb.stopCh:
					retry.Stop()
					return
				case <-parent.Done():
					retry.Stop()
					return
				case <-retry.C:
				}
			}
		}
	}()
}

func findTarget(targets []leaseRenewTarget, leaseID string) *leaseRenewTarget {
	for i := range targets {
		if targets[i].LeaseID == leaseID {
			return &targets[i]
		}
	}
	return nil
}

func minFailOpenRetry(targets []leaseRenewTarget, renewBefore time.Duration) time.Duration {
	retryWait := renewBefore
	for _, t := range targets {
		w := max(time.Until(t.ExpiresAt)/2, 100*time.Millisecond)
		rb := t.RenewBefore
		if rb <= 0 {
			rb = renewBefore
		}
		if w > rb {
			w = rb
		}
		if w < retryWait {
			retryWait = w
		}
	}
	if retryWait < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	return retryWait
}

func nextRenewWait(targets []leaseRenewTarget) time.Duration {
	var best time.Duration = -1
	for _, t := range targets {
		rb := t.RenewBefore
		if rb <= 0 {
			rb = 15 * time.Second
		}
		wait := max(time.Until(t.ExpiresAt.Add(-rb)), 0)
		if best < 0 || wait < best {
			best = wait
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func (e *Executor) stopLeaseHeartbeat(st *requestAuthorityState) {
	if st == nil || st.heartbeat == nil {
		return
	}
	st.heartbeat.Stop()
	st.heartbeat = nil
}
