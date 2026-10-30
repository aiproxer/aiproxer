package largebody_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// Phase-2 RED: the narrow safe-notification construction input. These tests do
// not compile until SpillConfig/CompletedSourceConfig gain the optional
// `OnRemovalFailure func()` input (4.3); `Source` stays unchanged.

// lplcNoticeFailingSource builds a CompletedSource whose unlink returns
// unlinkErr and whose removal notice counts into fired. With withReader the
// source is memory-backed with a stub descriptor so Open() succeeds for the
// deferred (final-reader) tests; otherwise it is path-only for the immediate
// tests. Assertions stay in each test.
func lplcNoticeFailingSource(t *testing.T, name string, unlinkErr error, fired *atomic.Int32, withReader bool) (*largebody.CompletedSource, *lplcRemoveStub) {
	t.Helper()
	stub := &lplcRemoveStub{err: unlinkErr}
	cfg := largebody.CompletedSourceConfig{
		FilePath:         filepath.Join(t.TempDir(), name),
		Size:             64,
		RemoveFile:       stub.remove,
		OnRemovalFailure: func() { fired.Add(1) },
	}
	if withReader {
		cfg.Memory = []byte("prefix")
		cfg.OpenFile = func(path string) (io.ReadCloser, error) {
			return &lplcStubRC{}, nil
		}
	}
	src, err := largebody.NewCompletedSource(cfg)
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	return src, stub
}

func TestLargePayloadLifecycle_SpillRemovalNoticeFiresOnceImmediate(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc notice immediate unlink failure")
	var fired atomic.Int32
	src, stub := lplcNoticeFailingSource(t, "notice.tmp", sentinel, &fired, false)

	err := lplcCloseGuarded(t, src.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Close got %v, want %v", err, sentinel)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("notice fired %d times, want exactly once", got)
	}

	// No second notice on repeated Close (no retry, no re-notify).
	if err := src.Close(); err != nil {
		t.Fatalf("repeated Close got %v, want nil", err)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("notice fired %d times after repeat, want still 1", got)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want 1", got)
	}
}

func TestLargePayloadLifecycle_SpillRemovalNoticeDeferredToFinalReader(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc notice deferred unlink failure")
	var fired atomic.Int32
	src, _ := lplcNoticeFailingSource(t, "notice-deferred.tmp", sentinel, &fired, true)
	r, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := lplcCloseGuarded(t, src.Close); err != nil {
		t.Fatalf("root Close got %v, want nil (deferred)", err)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("notice fired %d times at root close, want 0 (deferred to final reader)", got)
	}

	err = lplcCloseGuarded(t, r.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("final reader Close got %v, want %v", err, sentinel)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("notice fired %d times after final reader, want exactly once", got)
	}
}

func TestLargePayloadLifecycle_SpillRemovalNoticeSilentOnSuccessOrAbsent(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var stub lplcRemoveStub
		var fired atomic.Int32
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory:           []byte("prefix"),
			FilePath:         filepath.Join(t.TempDir(), "notice-success.tmp"),
			Size:             64,
			RemoveFile:       stub.remove,
			OnRemovalFailure: func() { fired.Add(1) },
			OpenFile: func(path string) (io.ReadCloser, error) {
				return &lplcStubRC{}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		if err := lplcCloseGuarded(t, src.Close); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := fired.Load(); got != 0 {
			t.Fatalf("notice fired %d times on success, want 0", got)
		}
	})

	t.Run("already absent", func(t *testing.T) {
		t.Parallel()
		var absent lplcRemoveStub
		absent.err = fmt.Errorf("reaped: %w", os.ErrNotExist)
		var fired atomic.Int32
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			FilePath:         filepath.Join(t.TempDir(), "notice-absent.tmp"),
			Size:             16,
			RemoveFile:       absent.remove,
			OnRemovalFailure: func() { fired.Add(1) },
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		if err := lplcCloseGuarded(t, src.Close); err != nil {
			t.Fatalf("Close on absent spill got %v, want nil", err)
		}
		if got := fired.Load(); got != 0 {
			t.Fatalf("notice fired %d times on confirmed absence, want 0", got)
		}
	})
}

func TestLargePayloadLifecycle_SpillRemovalNoticeOutsideMutex(t *testing.T) {
	t.Parallel()

	t.Run("immediate", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("lplc notice outside-lock unlink failure")
		var stub lplcRemoveStub
		var src *largebody.CompletedSource
		var err error
		// The unlink seam itself reenters lifecycle state: this deadlocks if
		// filesystem I/O ever runs under the lifecycle mutex.
		stub.onCall = func(path string) {
			_ = src.ActiveReaders()
			_ = src.FilePath()
		}
		stub.err = sentinel
		src, err = largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			FilePath:   filepath.Join(t.TempDir(), "notice-lock.tmp"),
			Size:       16,
			RemoveFile: stub.remove,
			OnRemovalFailure: func() {
				// Reentrant by design: proves the notice fires outside the
				// lifecycle mutex (else the guard times out).
				_ = src.ActiveReaders()
				_ = src.FilePath()
				_ = src.IsDeletePending()
			},
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		err = lplcCloseGuarded(t, src.Close)
		if !errors.Is(err, sentinel) {
			t.Fatalf("Close got %v, want %v", err, sentinel)
		}
	})

	t.Run("deferred", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("lplc notice deferred outside-lock failure")
		var stub lplcRemoveStub
		stub.err = sentinel
		var src *largebody.CompletedSource
		var err error
		var fired atomic.Int32
		src, err = largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory:     []byte("prefix"),
			FilePath:   filepath.Join(t.TempDir(), "notice-lock-deferred.tmp"),
			Size:       64,
			RemoveFile: stub.remove,
			OnRemovalFailure: func() {
				_ = src.ActiveReaders()
				_ = src.FilePath()
				fired.Add(1)
			},
			OpenFile: func(path string) (io.ReadCloser, error) {
				return &lplcStubRC{}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		r, err := src.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := lplcCloseGuarded(t, src.Close); err != nil {
			t.Fatalf("root Close: %v", err)
		}
		err = lplcCloseGuarded(t, r.Close)
		if !errors.Is(err, sentinel) {
			t.Fatalf("final reader Close got %v, want %v", err, sentinel)
		}
		if got := fired.Load(); got != 1 {
			t.Fatalf("notice fired %d times, want 1", got)
		}
	})
}

func TestLargePayloadLifecycle_SpillRemovalNoticePanicSafe(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc notice panic unlink failure")
	var stub lplcRemoveStub
	stub.err = sentinel
	dir := t.TempDir()
	path := lplcWriteRealSpillFile(t, dir, []byte("panic-probe-payload"))
	var fired atomic.Int32
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64,
		MaxInflightSpoolBytes: 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}
	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation: %v", err)
	}
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath:    path,
		Size:        19,
		Reservation: res,
		RemoveFile:  stub.remove,
		OnRemovalFailure: func() {
			fired.Add(1)
			panic("lplc observer panic must not escape cleanup")
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}

	// Observer panic must not prevent state publication or owned cleanup:
	// Close returns normally with the removal error (no panic escapes).
	err = lplcCloseGuarded(t, src.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Close got %v, want %v (panic must not replace the error)", err, sentinel)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("notice fired %d times, want 1 despite panic", got)
	}
	if src.FilePath() != path {
		t.Fatalf("FilePath got %q, want retained %q (state published despite panic)", src.FilePath(), path)
	}
	if got := ledger.ActiveReservations(); got != 0 {
		t.Fatalf("ActiveReservations got %d, want 0 (reservation released despite panic)", got)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("repeated Close got %v, want nil", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want 1", got)
	}
}

func TestLargePayloadLifecycle_SpillRemovalNoticeCarriesNoPayload(t *testing.T) {
	t.Parallel()

	// The privacy mechanism is structural: the callback takes no arguments, so
	// the source cannot leak a filesystem path, prompt bytes, digest, model, or
	// raw filesystem error through it. Pin both construction inputs to zero-arg.
	for name, typ := range map[string]reflect.Type{
		"SpillConfig":           reflect.TypeFor[largebody.SpillConfig](),
		"CompletedSourceConfig": reflect.TypeFor[largebody.CompletedSourceConfig](),
	} {
		field, ok := typ.FieldByName("OnRemovalFailure")
		if !ok {
			t.Fatalf("%s lacks OnRemovalFailure construction input", name)
		}
		ft := field.Type
		if ft.Kind() != reflect.Func || ft.NumIn() != 0 || ft.NumOut() != 0 {
			t.Fatalf("%s.OnRemovalFailure has type %v, want func() (no path/error/payload may cross)", name, ft)
		}
	}

	// A structural pin would be meaningless if the callback were never wired:
	// the zero-argument notice must still fire exactly once on a genuine
	// removal failure.
	sentinel := errors.New("lplc notice no-payload unlink failure")
	var stub lplcRemoveStub
	stub.err = sentinel
	var fired atomic.Int32
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath:         filepath.Join(t.TempDir(), "notice-no-payload.tmp"),
		Size:             16,
		RemoveFile:       stub.remove,
		OnRemovalFailure: func() { fired.Add(1) },
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	if err := lplcCloseGuarded(t, src.Close); !errors.Is(err, sentinel) {
		t.Fatalf("Close got %v, want %v", err, sentinel)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("notice fired %d times, want exactly once", got)
	}
}
