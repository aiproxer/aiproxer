package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type Config struct {
	OwnerID        string
	ClaimTTL       time.Duration
	ClaimLimit     int
	GlobalMax      int
	PerProviderMax int
	RetrySchedule  terminalwork.RetrySchedule
	Clock          Clock
	TickInterval   time.Duration
	RenewInterval  time.Duration
	TickC          <-chan struct{}
	RenewPulse     <-chan struct{}
	NewTicker      func(time.Duration) Ticker
	Metrics        ProcessMetrics
	// OnTerminalDone is invoked after successful Complete or Quarantine so
	// generation pending-provider refs can be cleared (requirement 9.8).
	// Prefer composing callbacks before Processor.Start; post-start registration
	// uses AddOnTerminalDone (synchronized, multi-subscriber).
	OnTerminalDone func(rec terminalwork.WorkRecord)
	// GenerationPins releases runtime-generation pins on terminal completion.
	GenerationPins *GenerationPinTracker
	// GenerationResolver resolves providers for rows with exact runtime
	// instance+generation identity. Nil falls back to process registry only for
	// legacy rows; exact/malformed rows fail closed when resolver is missing.
	GenerationResolver GenerationBoundResolver
}

// Processor claims due work, invokes providers once per claim, and completes/retries/quarantines.
type Processor struct {
	store    WorkStore
	registry *Registry
	cfg      Config

	globalSem    chan struct{}
	providerSems sync.Map // string -> chan struct{}
	processMu    sync.Mutex

	unresolvedMu sync.Mutex
	unresolved   map[string]struct{}

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool

	terminalDoneMu sync.RWMutex
	terminalDone   []func(rec terminalwork.WorkRecord)
}

// NewProcessor validates config and returns a ready processor.
func NewProcessor(store WorkStore, registry *Registry, cfg Config) (*Processor, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil work store", ErrNilProvider)
	}
	if registry == nil {
		return nil, fmt.Errorf("%w: nil registry", ErrNilProvider)
	}
	if strings.TrimSpace(cfg.OwnerID) == "" {
		return nil, fmt.Errorf("%w: empty owner id", ErrNilProvider)
	}
	if cfg.ClaimTTL <= 0 {
		return nil, fmt.Errorf("%w: non-positive claim ttl", ErrNilProvider)
	}
	if cfg.ClaimLimit < 0 {
		return nil, fmt.Errorf("%w: negative claim limit", ErrNilProvider)
	}
	if cfg.ClaimLimit == 0 {
		cfg.ClaimLimit = 1
	}
	if cfg.GlobalMax <= 0 {
		cfg.GlobalMax = 1
	}
	if cfg.PerProviderMax <= 0 {
		cfg.PerProviderMax = 1
	}
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	if cfg.NewTicker == nil {
		cfg.NewTicker = defaultNewTicker
	}
	if cfg.RetrySchedule.Initial <= 0 {
		cfg.RetrySchedule.Initial = time.Second
	}
	if cfg.RetrySchedule.Multiplier < 1 {
		cfg.RetrySchedule.Multiplier = 2
	}
	if cfg.RetrySchedule.Max <= 0 {
		cfg.RetrySchedule.Max = 30 * time.Second
	}
	p := &Processor{
		store:      store,
		registry:   registry,
		cfg:        cfg,
		globalSem:  make(chan struct{}, cfg.GlobalMax),
		unresolved: make(map[string]struct{}),
	}
	if cfg.OnTerminalDone != nil {
		p.terminalDone = []func(terminalwork.WorkRecord){cfg.OnTerminalDone}
	}
	return p, nil
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func (p *Processor) ProcessDue(ctx context.Context) error {
	if p == nil {
		return ErrNotRunning
	}
	p.processMu.Lock()
	defer p.processMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	defer p.refreshMetrics(ctx)
	now := p.cfg.Clock.Now().UTC()
	claimed, err := p.store.ClaimDue(ctx, terminalwork.ClaimDueCommand{
		OwnerID: p.cfg.OwnerID,
		TTL:     p.cfg.ClaimTTL,
		Limit:   p.cfg.ClaimLimit,
		Now:     now,
	})
	if err != nil {
		p.observeTransition(TransitionClaimFailed, "unknown", "")
		return err
	}
	if len(claimed) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(claimed))
	for _, rec := range claimed {
		wg.Go(func() {
			if err := p.processOne(ctx, rec); err != nil {
				errCh <- err
			}
		})
	}
	wg.Wait()
	close(errCh)
	var joined error
	for err := range errCh {
		joined = errors.Join(joined, err)
	}
	return joined
}

func (p *Processor) observeTransition(state, kind, providerID string) {
	if p == nil || p.cfg.Metrics == nil {
		return
	}
	p.cfg.Metrics.ObserveTransition(state, kind, providerID)
}

func (p *Processor) refreshMetrics(ctx context.Context) {
	if p == nil || p.cfg.Metrics == nil {
		return
	}
	p.cfg.Metrics.RefreshAfterBatch(ctx)
}

func (p *Processor) processOne(ctx context.Context, rec terminalwork.WorkRecord) error {
	kind := string(rec.Kind)
	providerID := strings.TrimSpace(rec.ProviderID)
	if err := p.acquire(ctx, rec.ProviderID); err != nil {
		p.observeTransition(TransitionAcquireFailed, kind, providerID)
		return err
	}
	defer p.release(rec.ProviderID)

	invokeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewErrCh := make(chan error, 1)
	var renewWG sync.WaitGroup
	renewSrc, stopRenew := p.renewSource(invokeCtx)
	if stopRenew != nil {
		defer stopRenew()
	}
	if renewSrc != nil {
		renewWG.Go(func() {
			if err := p.renewLoop(invokeCtx, rec.WorkID, renewSrc); err != nil {
				select {
				case renewErrCh <- err:
				default:
				}
				cancel()
			}
		})
	}

	invokeErr := p.invokeSafe(invokeCtx, rec)
	cancel()
	renewWG.Wait()

	var renewErr error
	select {
	case renewErr = <-renewErrCh:
	default:
	}
	if renewErr != nil && invokeErr == nil {
		invokeErr = renewErr
	} else if renewErr != nil && invokeErr != nil && errors.Is(invokeErr, context.Canceled) {
		invokeErr = renewErr
	}

	storeCtx := context.WithoutCancel(ctx)
	now := p.cfg.Clock.Now().UTC()
	if invokeErr == nil {
		if err := p.store.Complete(storeCtx, terminalwork.CompleteCommand{
			WorkID:          rec.WorkID,
			ExpectedOwnerID: p.cfg.OwnerID,
			Now:             now,
		}); err != nil {
			p.observeTransition(TransitionValidationFailed, kind, providerID)
			return err
		}
		p.observeTransition(string(sdk.WorkStateCompleted), kind, providerID)
		p.notifyTerminalDone(rec)
		return nil
	}
	if IsPermanent(invokeErr) {
		if err := p.store.Quarantine(storeCtx, terminalwork.QuarantineCommand{
			WorkID: rec.WorkID,
			Err: terminalwork.BoundedError{
				Code:      errorCode(invokeErr),
				Permanent: true,
				Message:   safeErrorMessage(invokeErr),
			},
			Now: now,
		}); err != nil {
			p.observeTransition(TransitionValidationFailed, kind, providerID)
			return err
		}
		p.observeTransition(TransitionValidationFailed, kind, providerID)
		p.observeTransition(string(sdk.WorkStateQuarantined), kind, providerID)
		p.notifyTerminalDone(rec)
		return nil
	}
	if err := p.store.ScheduleRetry(storeCtx, terminalwork.ScheduleRetryCommand{
		WorkID:          rec.WorkID,
		ExpectedOwnerID: p.cfg.OwnerID,
		Schedule:        p.cfg.RetrySchedule,
		Err: terminalwork.BoundedError{
			Code:      errorCode(invokeErr),
			Permanent: false,
			Message:   safeErrorMessage(invokeErr),
		},
		Now: now,
	}); err != nil {
		p.observeTransition(TransitionValidationFailed, kind, providerID)
		return err
	}
	p.observeTransition(string(sdk.WorkStateRetry), kind, providerID)
	return nil
}

func (p *Processor) renewSource(ctx context.Context) (<-chan struct{}, func()) {
	if p.cfg.RenewPulse != nil {
		return p.cfg.RenewPulse, nil
	}
	if p.cfg.RenewInterval <= 0 {
		return nil, nil
	}
	ticker := p.cfg.NewTicker(p.cfg.RenewInterval)
	ch := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-done:
				ticker.Stop()
				return
			case <-ticker.C():
				select {
				case ch <- struct{}{}:
				case <-ctx.Done():
					ticker.Stop()
					return
				case <-done:
					ticker.Stop()
					return
				}
			}
		}
	}()
	return ch, func() { close(done) }
}

func (p *Processor) invokeSafe(ctx context.Context, rec terminalwork.WorkRecord) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%w: panic: %v", ErrProviderOutage, recovered)
		}
	}()

	providerID := strings.TrimSpace(rec.ProviderID)
	if providerID == "" {
		if rec.Kind.RequiresProvider() {
			p.noteUnresolved("")
			return fmt.Errorf("%w: empty provider", ErrMissingProvider)
		}
		providerID = string(rec.Kind)
	}
	prov, rerr := p.resolveProvider(rec, providerID)
	if rerr != nil {
		if errors.Is(rerr, ErrMissingProvider) {
			p.noteUnresolved(providerID)
		}
		return rerr
	}
	p.clearUnresolved(providerID)
	return prov.Invoke(ctx, rec, rec.SourceKey.String())
}

func (p *Processor) resolveProvider(rec terminalwork.WorkRecord, providerID string) (EffectProvider, error) {
	switch rec.Versions.RuntimeIdentity() {
	case terminalwork.RuntimeIdentityLegacy:
		return p.registry.Resolve(providerID, rec.Kind)
	case terminalwork.RuntimeIdentityMalformed:
		p.noteUnresolved(providerID)
		return nil, fmt.Errorf("%w: malformed runtime identity", ErrMissingProvider)
	case terminalwork.RuntimeIdentityExact:
		runtimeInst := strings.TrimSpace(rec.Versions.RuntimeInstanceID)
		runtimeGenID := strings.TrimSpace(rec.Versions.RuntimeGenerationID)
		if p.cfg.GenerationResolver == nil {
			p.noteUnresolved(providerID)
			return nil, fmt.Errorf("%w: runtime generation %s unbound", ErrMissingProvider, runtimeGenID)
		}
		prov, err := p.cfg.GenerationResolver.Resolve(runtimeInst, runtimeGenID, providerID, rec.Kind)
		if err != nil {
			return nil, err
		}
		return prov, nil
	default:
		p.noteUnresolved(providerID)
		return nil, fmt.Errorf("%w: unknown runtime identity", ErrMissingProvider)
	}
}

func (p *Processor) renewLoop(ctx context.Context, workID string, pulse <-chan struct{}) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-pulse:
			if !ok {
				return nil
			}
			now := p.cfg.Clock.Now().UTC()
			if err := p.store.RenewClaim(ctx, terminalwork.RenewClaimCommand{
				WorkID:  workID,
				OwnerID: p.cfg.OwnerID,
				TTL:     p.cfg.ClaimTTL,
				Now:     now,
			}); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return fmt.Errorf("%w: %v", ErrClaimRenewFailed, err)
			}
		}
	}
}

func (p *Processor) acquire(ctx context.Context, providerID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.globalSem <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-p.globalSem
		return err
	}
	sem := p.providerSemaphore(providerID)
	select {
	case <-ctx.Done():
		<-p.globalSem
		return ctx.Err()
	case sem <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-sem
		<-p.globalSem
		return err
	}
	return nil
}

func (p *Processor) release(providerID string) {
	sem := p.providerSemaphore(providerID)
	select {
	case <-sem:
	default:
	}
	select {
	case <-p.globalSem:
	default:
	}
}

func (p *Processor) providerSemaphore(providerID string) chan struct{} {
	key := strings.TrimSpace(providerID)
	if key == "" {
		key = "_"
	}
	if v, ok := p.providerSems.Load(key); ok {
		sem, ok := v.(chan struct{})
		if ok {
			return sem
		}
	}
	ch := make(chan struct{}, p.cfg.PerProviderMax)
	actual, _ := p.providerSems.LoadOrStore(key, ch)
	sem, ok := actual.(chan struct{})
	if !ok {
		return ch
	}
	return sem
}

// Run processes ticks until ctx is cancelled.
// Without TickC/TickInterval, runs ProcessDue once.
func (p *Processor) Run(ctx context.Context) error {
	if p == nil {
		return ErrNotRunning
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tickC, stopTick := p.tickSource(ctx)
	if stopTick != nil {
		defer stopTick()
	}
	if tickC == nil {
		return p.ProcessDue(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-tickC:
			if !ok {
				return nil
			}
			if err := p.ProcessDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
				_ = err
			}
		}
	}
}

func (p *Processor) tickSource(ctx context.Context) (<-chan struct{}, func()) {
	if p.cfg.TickC != nil {
		return p.cfg.TickC, nil
	}
	if p.cfg.TickInterval <= 0 {
		return nil, nil
	}
	ticker := p.cfg.NewTicker(p.cfg.TickInterval)
	ch := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-done:
				ticker.Stop()
				return
			case <-ticker.C():
				select {
				case ch <- struct{}{}:
				case <-ctx.Done():
					ticker.Stop()
					return
				case <-done:
					ticker.Stop()
					return
				}
			}
		}
	}()
	return ch, func() { close(done) }
}

// Start owns one Run goroutine until Shutdown. Waits out a prior run that is still exiting.
func (p *Processor) Start(parent context.Context) error {
	if p == nil {
		return ErrNotRunning
	}
	if parent == nil {
		parent = context.Background()
	}
	for {
		p.mu.Lock()
		if !p.started {
			ctx, cancel := context.WithCancel(parent)
			p.cancel = cancel
			p.done = make(chan struct{})
			p.started = true
			done := p.done
			p.mu.Unlock()
			go func() {
				defer func() {
					p.mu.Lock()
					p.started = false
					p.cancel = nil
					close(done)
					p.mu.Unlock()
				}()
				_ = p.Run(ctx)
			}()
			return nil
		}
		done := p.done
		p.mu.Unlock()
		if done == nil {
			return ErrAlreadyStarted
		}
		select {
		case <-done:
		case <-parent.Done():
			return parent.Err()
		}
	}
}

// Shutdown cancels Run and waits for the owned goroutine.
// On wait timeout, cancel remains in effect; started resets when Run exits.
func (p *Processor) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	cancel := p.cancel
	done := p.done
	started := p.started
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !started && done == nil {
		return nil
	}
	if done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Processor) notifyTerminalDone(rec terminalwork.WorkRecord) {
	if p == nil {
		return
	}
	// Snapshot callbacks under lock, then invoke outside so panic in one
	// subscriber cannot skip pin release or other subscribers.
	p.terminalDoneMu.RLock()
	cbs := append([]func(terminalwork.WorkRecord){}, p.terminalDone...)
	p.terminalDoneMu.RUnlock()

	if p.cfg.GenerationPins != nil {
		func() {
			defer func() { _ = recover() }()
			p.cfg.GenerationPins.MarkTerminal(rec.WorkID)
		}()
	}
	for _, fn := range cbs {
		if fn == nil {
			continue
		}
		func(cb func(terminalwork.WorkRecord)) {
			defer func() { _ = recover() }()
			cb(rec)
		}(fn)
	}
}

// AddOnTerminalDone registers an additional terminal-done subscriber.
// Safe under concurrent registration and invocation; does not overwrite
// existing callbacks (task 3.6). Prefer composing OnTerminalDone before Start.
func (p *Processor) AddOnTerminalDone(fn func(rec terminalwork.WorkRecord)) {
	if p == nil || fn == nil {
		return
	}
	p.terminalDoneMu.Lock()
	defer p.terminalDoneMu.Unlock()
	p.terminalDone = append(p.terminalDone, fn)
}

// SetOnTerminalDone appends a terminal-done callback (compat). Prefer
// AddOnTerminalDone or immutable Config.OnTerminalDone before Start.
func (p *Processor) SetOnTerminalDone(fn func(rec terminalwork.WorkRecord)) {
	p.AddOnTerminalDone(fn)
}

func (p *Processor) noteUnresolved(providerID string) {
	id := strings.TrimSpace(providerID)
	if id == "" {
		id = "_"
	}
	p.unresolvedMu.Lock()
	p.unresolved[id] = struct{}{}
	p.unresolvedMu.Unlock()
}

func (p *Processor) clearUnresolved(providerID string) {
	id := strings.TrimSpace(providerID)
	if id == "" {
		return
	}
	p.unresolvedMu.Lock()
	delete(p.unresolved, id)
	p.unresolvedMu.Unlock()
}

// UnresolvedProviderIDs returns provider IDs that failed resolution (pending drain).
func (p *Processor) UnresolvedProviderIDs() []string {
	if p == nil {
		return nil
	}
	p.unresolvedMu.Lock()
	defer p.unresolvedMu.Unlock()
	out := make([]string, 0, len(p.unresolved))
	for id := range p.unresolved {
		out = append(out, id)
	}
	return out
}

// Running reports whether Start owns an active Run goroutine.
func (p *Processor) Running() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

// Readiness is a content-safe processor status snapshot for composition/readiness (task 4.5).
type Readiness struct {
	Running               bool
	UnresolvedProviderIDs []string
}

// Readiness reports processor running state and unresolved provider IDs.
func (p *Processor) Readiness() Readiness {
	if p == nil {
		return Readiness{}
	}
	return Readiness{
		Running:               p.Running(),
		UnresolvedProviderIDs: p.UnresolvedProviderIDs(),
	}
}
