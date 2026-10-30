package processhost

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Connector diagnostic budget defaults.
//
// The drain keeps a connector's stdout and stderr readable for the whole
// process lifetime, so the host reads, copies and schedules on the
// connector's behalf for as long as it writes. The threat model requires
// bounded plugin log and diagnostic output (TM-07), so every drained byte is
// charged against a budget and a connector that outruns the budget is
// terminated instead of being allowed to keep the host busy.
const (
	// defaultOutputBurstBytes is the allowance a connector may spend at once.
	defaultOutputBurstBytes int64 = 4 << 20
	// defaultOutputBytesPerSecond is the sustained allowance.
	defaultOutputBytesPerSecond int64 = 1 << 20
	// drainChunkBytes is the read size of the drain workers.
	drainChunkBytes = 32 << 10
	// maxRefillInterval bounds the refill arithmetic so a long idle period
	// cannot overflow the token computation.
	maxRefillInterval = 10 * time.Second
)

// OutputBudget bounds the diagnostics a single connector generation may emit.
//
// The budget is a token bucket: BurstBytes is the allowance a connector may
// spend at once, and BytesPerSecond is the sustained allowance it refills at.
// A zero value selects the package default for that field.
type OutputBudget struct {
	// BurstBytes is the burst allowance in bytes.
	BurstBytes int64
	// BytesPerSecond is the sustained allowance in bytes per second.
	BytesPerSecond int64
}

// normalized returns the budget with the package defaults applied.
func (b OutputBudget) normalized() OutputBudget {
	if b.BurstBytes <= 0 {
		b.BurstBytes = defaultOutputBurstBytes
	}
	if b.BytesPerSecond <= 0 {
		b.BytesPerSecond = defaultOutputBytesPerSecond
	}
	return b
}

// outputBudget is the mutable token bucket behind OutputBudget.
type outputBudget struct {
	mu       sync.Mutex
	capacity int64
	rate     int64
	tokens   int64
	last     time.Time
	now      func() time.Time
}

func newOutputBudget(b OutputBudget, now func() time.Time) *outputBudget {
	n := b.normalized()
	if now == nil {
		now = time.Now
	}
	return &outputBudget{
		capacity: n.BurstBytes,
		rate:     n.BytesPerSecond,
		tokens:   n.BurstBytes,
		now:      now,
	}
}

// charge accounts n drained bytes and reports whether the connector outran
// its allowance. The burst allowance is spent first; the sustained allowance
// refills it over time.
func (b *outputBudget) charge(n int64) bool {
	if n <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.last.IsZero() {
		elapsed := min(now.Sub(b.last), maxRefillInterval)
		if elapsed > 0 {
			b.tokens += int64(elapsed) * b.rate / int64(time.Second)
			if b.tokens > b.capacity {
				b.tokens = b.capacity
			}
		}
	}
	b.last = now
	b.tokens -= n
	return b.tokens < 0
}

// pipeDrain owns the goroutines that consume a launched connector's stdout and
// stderr for the whole process lifetime.
//
// A connector reports progress and diagnostics on stderr. When nobody reads
// that pipe, the OS pipe buffer fills (64 KiB on Linux) and the connector's
// next write blocks forever: the connector stalls mid-request, possibly while
// holding a turn claim, and the host is still waiting for the peer to
// authenticate. The host therefore drains both pipes and discards the bytes.
//
// The drained bytes stay off every surface: they are not forwarded to a
// client, and they are not forwarded to the operator log (see
// docs/backend-plugins/threat-model.md, TM-07). Nothing is buffered, so the
// drain has a constant memory footprint, and every byte is charged against
// the diagnostic budget so a flooding connector is stopped rather than
// served.
type pipeDrain struct {
	stdout io.ReadCloser
	stderr io.ReadCloser

	budget   *outputBudget
	breached atomic.Bool
	breach   sync.Once
	onBreach func()

	wg   sync.WaitGroup
	once sync.Once
}

// startPipeDrain begins draining the process pipes under the diagnostic
// budget. onBreach, when set, runs once if the connector outruns the budget.
func startPipeDrain(proc Process, budget OutputBudget, onBreach func()) *pipeDrain {
	if proc == nil {
		return nil
	}
	d := &pipeDrain{
		stdout:   proc.Stdout(),
		stderr:   proc.Stderr(),
		budget:   newOutputBudget(budget, nil),
		onBreach: onBreach,
	}
	d.wg.Add(2)
	go d.consume(&d.stdout)
	go d.consume(&d.stderr)
	return d
}

// consume copies one pipe into the discard sink until the pipe ends, fails, or
// is closed by the owner. The drain keeps reading after a budget breach: the
// owner terminates the connector, and stopping the read here would recreate
// the blocked write this drain exists to prevent.
func (d *pipeDrain) consume(pipe *io.ReadCloser) {
	defer d.wg.Done()
	if *pipe == nil {
		return
	}
	buf := make([]byte, drainChunkBytes)
	for {
		n, err := (*pipe).Read(buf)
		if n > 0 && d.budget.charge(int64(n)) {
			d.reportBreach()
		}
		if err != nil {
			return
		}
	}
}

// reportBreach records the budget breach and notifies the owner once.
//
// The notification terminates the connector, and the termination joins the
// drain. It therefore cannot run on the worker that reports the breach: it
// runs on its own worker instead.
func (d *pipeDrain) reportBreach() {
	d.breach.Do(func() {
		d.breached.Store(true)
		if d.onBreach == nil {
			return
		}
		go d.onBreach()
	})
}

// exceeded reports whether the connector outran its diagnostic budget.
func (d *pipeDrain) exceeded() bool {
	return d != nil && d.breached.Load()
}

// stop releases the pipes and waits for the drain goroutines to finish. It is
// safe to call more than once.
func (d *pipeDrain) stop() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		if d.stdout != nil {
			_ = d.stdout.Close()
		}
		if d.stderr != nil {
			_ = d.stderr.Close()
		}
	})
	d.wg.Wait()
}
