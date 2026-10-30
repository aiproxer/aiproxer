package runtimehost_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// FuzzGenerationLifecycleTransitions applies a bounded deterministic command
// sequence to generation lifecycle APIs. It does not fuzz concurrent coordinator
// races or wall-clock waits (req 10.1, 15.8, 16.9).
func FuzzGenerationLifecycleTransitions(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{0, 1, 2, 2, 3})
	f.Add([]byte{0, 1, 8, 9})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 48 {
			ops = ops[:48]
		}
		m := runtimehost.NewManager(16, nil)
		var (
			candidate *runtimehost.Generation
			lease     *runtimehost.Lease
			pin       *runtimehost.Pin
		)
		defer func() {
			if pin != nil {
				pin.Release()
			}
			if lease != nil {
				lease.Release()
			}
		}()

		for _, op := range ops {
			switch op % 10 {
			case 0:
				if candidate == nil || candidate.Lifecycle() == runtimehost.GenActive ||
					candidate.Lifecycle() == runtimehost.GenClosed ||
					candidate.Lifecycle() == runtimehost.GenFailed {
					candidate = m.Prepare("fuzz")
				}
			case 1:
				if candidate != nil {
					_ = m.Publish(candidate)
				}
			case 2:
				if lease == nil {
					if l, ok := m.Acquire(); ok {
						lease = l
					}
				}
			case 3:
				if lease != nil {
					lease.Release()
					lease = nil
				}
			case 4:
				if lease != nil && pin == nil {
					if p, ok := lease.TransferPin(runtimehost.PinSSE); ok {
						pin = p
						lease = nil
					}
				}
			case 5:
				if pin != nil {
					pin.Release()
					pin = nil
				}
			case 6:
				if g := m.Active(); g != nil {
					_ = g.BeginQuiesce()
					_ = g.MarkQuiesced()
					_ = g.BeginClose()
					_ = g.Close()
				}
			case 7:
				if candidate != nil {
					_ = candidate.Transition(runtimehost.GenFailed)
					_ = candidate.Discard()
				}
			case 8:
				_ = m.Prepare("alt")
				_ = m.Publish(m.Prepare("pub"))
			case 9:
				m.SweepClosed()
			}
		}
		// Lifecycle word must remain a known enum value for any live generation.
		if g := m.Active(); g != nil {
			st := g.Lifecycle()
			if st > runtimehost.GenFailed {
				t.Fatalf("unknown lifecycle %v", st)
			}
		}
	})
}
