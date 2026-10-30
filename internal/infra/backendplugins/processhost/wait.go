package processhost

import (
	"sync"
	"time"
)

// ExactlyOnceWait wraps Process.Wait so concurrent/repeated waiters share one result.
type ExactlyOnceWait struct {
	once sync.Once
	err  error
}

func (w *ExactlyOnceWait) Wait(p Process) error {
	w.once.Do(func() {
		if p != nil {
			w.err = p.Wait()
		}
	})
	return w.err
}

// StopProcess runs graceful stop, then hard kill on timeout, then exactly-once wait.
// Descendant/process-tree ownership is platform-specific; callers must not assume
// tree cleanup where the platform profile is fail-closed.
func StopProcess(p Process, gracefulTimeout time.Duration) error {
	if p == nil {
		return nil
	}
	_ = p.GracefulStop(gracefulTimeout)
	_ = p.SignalKill()
	var w ExactlyOnceWait
	return w.Wait(p)
}

// DescendantCleanupSupported reports whether the current platform profile claims
// native process-tree cleanup (Windows job / Linux process group). Darwin remains fail-closed.
func DescendantCleanupSupported() bool {
	return descendantCleanupSupported()
}
