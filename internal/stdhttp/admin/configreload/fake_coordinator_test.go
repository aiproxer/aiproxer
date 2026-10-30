package configreload_test

import (
	"context"
	"sync"
	"sync/atomic"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// fakeCoordinator mirrors the serialized busy/status/host-context behavior needed
// by management adapter goldens without compiling a full runtimehost pipeline.
type fakeCoordinator struct {
	mu          sync.Mutex
	busy        bool
	shutdown    atomic.Bool
	attempts    atomic.Int64
	activeGen   atomic.Int64
	last        sdkreload.Result
	fixedSource string
	reloadFn    func(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
	onComplete  func(sdkreload.Result)
}

func newFakeCoordinator(fixed string, fn func(context.Context, sdkreload.Trigger) sdkreload.Result) *fakeCoordinator {
	c := &fakeCoordinator{fixedSource: fixed, reloadFn: fn}
	c.activeGen.Store(1)
	c.last = sdkreload.Result{Category: sdkreload.ResultPublished, ActiveGeneration: 1}
	return c
}

func (c *fakeCoordinator) FixedSourcePath() string { return c.fixedSource }
func (c *fakeCoordinator) MarkShutdown()           { c.shutdown.Store(true) }
func (c *fakeCoordinator) SetOnComplete(fn func(sdkreload.Result)) {
	c.mu.Lock()
	c.onComplete = fn
	c.mu.Unlock()
}

func (c *fakeCoordinator) Status() sdkreload.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return sdkreload.Status{
		ActiveGeneration: c.activeGen.Load(),
		LastResult:       c.last,
		Busy:             c.busy,
	}
}

func (c *fakeCoordinator) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if c.shutdown.Load() {
		return sdkreload.Result{Category: sdkreload.ResultCanceled, ReasonCategory: "shutdown"}
	}
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return sdkreload.Result{
			Category:         sdkreload.ResultBusy,
			ActiveGeneration: c.activeGen.Load(),
			ReasonCategory:   "reload-in-progress",
		}
	}
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
	}()

	attempt := c.attempts.Add(1)
	var res sdkreload.Result
	if c.reloadFn != nil {
		res = c.reloadFn(ctx, trigger)
	} else {
		res = sdkreload.Result{Category: sdkreload.ResultPublished}
	}
	res.AttemptID = attempt
	if res.ActiveGeneration == 0 {
		res.ActiveGeneration = c.activeGen.Load()
	}
	if res.Category == sdkreload.ResultPublished {
		prev := c.activeGen.Load()
		res.PreviousGeneration = prev
		c.activeGen.Store(prev + 1)
		res.ActiveGeneration = c.activeGen.Load()
	}
	c.mu.Lock()
	c.last = res
	onComplete := c.onComplete
	c.mu.Unlock()
	if onComplete != nil {
		onComplete(res)
	}
	return res
}
