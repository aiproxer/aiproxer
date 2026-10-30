package largebody_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// Task Brief (Task 2 — Preserve truthful spill cleanup at root and final reader):
// Acceptance: 4.1 defer removal to final reader + reject new readers after root
// close; 4.2 success/ErrNotExist confirm absence, failure retains attribution;
// 4.3 bounded sanitized once-only notice (phase-2 file); 4.4 outcome/reservation
// untouched, no retry/worker. Completion: every test below green, no production
// change outside internal/core/largebody, validation
// `go test -count=1 ./internal/core/largebody` green.

// lplcRemoveStub is a minimal injectable unlink seam: it counts claimants and
// returns a programmed error. It reuses the existing RemoveFile config seam.
type lplcRemoveStub struct {
	calls  atomic.Int32
	err    error
	onCall func(path string)
}

func (s *lplcRemoveStub) remove(path string) error {
	s.calls.Add(1)
	if s.onCall != nil {
		s.onCall(path)
	}
	return s.err
}

// lplcStubRC is a minimal injectable descriptor seam via the OpenFile config.
type lplcStubRC struct {
	closeFn func() error
	closed  atomic.Bool
}

func (s *lplcStubRC) Read(p []byte) (int, error) { return 0, io.EOF }

func (s *lplcStubRC) Close() error {
	s.closed.Store(true)
	if s.closeFn != nil {
		return s.closeFn()
	}
	return nil
}

func lplcWriteRealSpillFile(t *testing.T, dir string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "lplc_spill.tmp")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// lplcFailingSource builds a real-file CompletedSource whose unlink always
// fails with unlinkErr. It covers the repeated file-backed failing-source
// setup shared by the immediate, deferred, race, and no-side-files tests;
// each test keeps its own distinct assertions.
func lplcFailingSource(t *testing.T, content []byte, unlinkErr error) (*largebody.CompletedSource, string, *lplcRemoveStub) {
	t.Helper()
	stub := &lplcRemoveStub{err: unlinkErr}
	path := lplcWriteRealSpillFile(t, t.TempDir(), content)
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath:   path,
		Size:       int64(len(content)),
		RemoveFile: stub.remove,
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	return src, path, stub
}

// lplcCloseGuarded runs a Close with a deadlock detector: if removal I/O or the
// failure notice ever runs under the lifecycle mutex, reentrant test probes
// deadlock and this guard converts the hang into a failure.
func lplcCloseGuarded(t *testing.T, closeFn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Close blocked: removal I/O or notice appears to run under the lifecycle mutex")
		return nil
	}
}

func TestLargePayloadLifecycle_SpillImmediateRemovalSuccess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := bytes.Repeat([]byte("S"), 64)
	path := lplcWriteRealSpillFile(t, dir, content)

	var stub lplcRemoveStub
	stub.err = nil
	remove := func(p string) error {
		if err := os.Remove(p); err != nil {
			return err
		}
		return stub.remove(p)
	}
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		FilePath:   path,
		Size:       int64(len(content)),
		RemoveFile: remove,
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}

	if err := lplcCloseGuarded(t, src.Close); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want exactly 1", got)
	}
	if src.FilePath() != "" {
		t.Fatalf("FilePath after success got %q, want empty (confirmed absence)", src.FilePath())
	}
	if src.HasSpilled() {
		t.Fatal("HasSpilled after success must be false")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file must be absent from disk, stat err=%v", err)
	}

	// Repeated root Close stays nil and never retries.
	if err := src.Close(); err != nil {
		t.Fatalf("repeated Close got %v, want nil", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls after repeat got %d, want still 1 (no retry)", got)
	}
}

func TestLargePayloadLifecycle_SpillDeferredRemovalSuccess(t *testing.T) {
	t.Parallel()

	var stub lplcRemoveStub
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 16,
		RemoveFile: func(p string) error {
			if err := os.Remove(p); err != nil {
				return err
			}
			return stub.remove(p)
		},
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	payload := bytes.Repeat([]byte("D"), 128)
	if _, err := buf.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	filePath := src.FilePath()
	if filePath == "" {
		t.Fatal("expected spill file")
	}

	r, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Root close defers while a reader is active (4.1): no unlink yet.
	if err := lplcCloseGuarded(t, src.Close); err != nil {
		t.Fatalf("root Close: %v", err)
	}
	if got := stub.calls.Load(); got != 0 {
		t.Fatalf("unlink calls while reader active got %d, want 0 (deferred)", got)
	}
	if !src.IsDeletePending() {
		t.Fatal("expected delete-pending while reader active")
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file must survive while reader active: %v", err)
	}

	// Final reader releases removal (4.1): exactly one unlink, success.
	if err := lplcCloseGuarded(t, r.Close); err != nil {
		t.Fatalf("final reader Close: %v", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls after final reader got %d, want 1", got)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file must be gone after final reader, stat err=%v", err)
	}
}

func TestLargePayloadLifecycle_SpillAlreadyAbsentIsConfirmed(t *testing.T) {
	t.Parallel()

	t.Run("immediate", func(t *testing.T) {
		t.Parallel()
		absent := fmt.Errorf("already reaped: %w", os.ErrNotExist)
		var stub lplcRemoveStub
		stub.err = absent
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			FilePath:   filepath.Join(t.TempDir(), "gone.tmp"),
			Size:       32,
			RemoveFile: stub.remove,
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}

		// os.ErrNotExist means confirmed absence (4.2): nil, path cleared.
		if err := lplcCloseGuarded(t, src.Close); err != nil {
			t.Fatalf("Close on already-absent spill got %v, want nil", err)
		}
		if src.FilePath() != "" {
			t.Fatalf("FilePath got %q, want empty after confirmed absence", src.FilePath())
		}
		if src.HasSpilled() {
			t.Fatal("HasSpilled must be false after confirmed absence")
		}
		if got := stub.calls.Load(); got != 1 {
			t.Fatalf("unlink calls got %d, want 1", got)
		}
		if err := src.Close(); err != nil {
			t.Fatalf("repeated Close got %v, want nil", err)
		}
	})

	t.Run("deferred", func(t *testing.T) {
		t.Parallel()
		absent := os.ErrNotExist
		var stub lplcRemoveStub
		stub.err = absent
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory:     []byte("prefix"),
			FilePath:   filepath.Join(t.TempDir(), "gone.tmp"),
			Size:       64,
			RemoveFile: stub.remove,
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
		if err := src.Close(); err != nil {
			t.Fatalf("root Close: %v", err)
		}
		if err := lplcCloseGuarded(t, r.Close); err != nil {
			t.Fatalf("final reader Close on already-absent spill got %v, want nil", err)
		}
		if src.FilePath() != "" {
			t.Fatalf("FilePath got %q, want empty after confirmed absence", src.FilePath())
		}
	})
}

func TestLargePayloadLifecycle_SpillImmediateFailureRetainsAttribution(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc immediate unlink failure")
	src, path, stub := lplcFailingSource(t, bytes.Repeat([]byte("F"), 32), sentinel)

	err := lplcCloseGuarded(t, src.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Close got %v, want error identity %v", err, sentinel)
	}
	// Failed unlink never records reclamation (4.2): path/error retained.
	if src.FilePath() != path {
		t.Fatalf("FilePath got %q, want retained %q after failed unlink", src.FilePath(), path)
	}
	if !src.HasSpilled() {
		t.Fatal("HasSpilled must stay true after failed unlink")
	}

	// Repeated root Close stays nil and must NOT retry (design).
	if err := src.Close(); err != nil {
		t.Fatalf("repeated Close got %v, want nil", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want 1 (no retry after failure)", got)
	}
}

func TestLargePayloadLifecycle_SpillDeferredFailureReachesFinalReader(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc deferred unlink failure")
	src, path, stub := lplcFailingSource(t, bytes.Repeat([]byte("G"), 48), sentinel)

	r, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Root close defers; new readers rejected after root closure (4.1).
	if err := lplcCloseGuarded(t, src.Close); err != nil {
		t.Fatalf("root Close with active reader got %v, want nil (deferred)", err)
	}
	if _, err := src.Open(); !errors.Is(err, largebody.ErrSourceClosed) {
		t.Fatalf("Open after root Close got %v, want ErrSourceClosed", err)
	}

	// Final reader exposes the deferred failure with identity (4.2).
	err = lplcCloseGuarded(t, r.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("final reader Close got %v, want error identity %v", err, sentinel)
	}
	if src.FilePath() != path {
		t.Fatalf("FilePath got %q, want retained %q after deferred failure", src.FilePath(), path)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want 1", got)
	}

	// No retry from the root afterwards.
	if err := src.Close(); err != nil {
		t.Fatalf("repeated root Close got %v, want nil", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want still 1 (no retry)", got)
	}
}

func TestLargePayloadLifecycle_SpillDescriptorCloseErrorJoined(t *testing.T) {
	t.Parallel()

	descErr := errors.New("lplc descriptor close failure")
	removeErr := errors.New("lplc deferred removal failure")
	var stub lplcRemoveStub
	stub.err = removeErr
	rc := &lplcStubRC{closeFn: func() error { return descErr }}
	src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory:     []byte("prefix"),
		FilePath:   filepath.Join(t.TempDir(), "joined.tmp"),
		Size:       64,
		RemoveFile: stub.remove,
		OpenFile: func(path string) (io.ReadCloser, error) {
			return rc, nil
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedSource: %v", err)
	}
	r, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("root Close: %v", err)
	}

	// Both errors must survive the join; neither may be dropped.
	err = lplcCloseGuarded(t, r.Close)
	if !errors.Is(err, descErr) {
		t.Fatalf("reader Close got %v, want descriptor error %v preserved", err, descErr)
	}
	if !errors.Is(err, removeErr) {
		t.Fatalf("reader Close got %v, want deferred removal error %v preserved", err, removeErr)
	}
}

func TestLargePayloadLifecycle_SpillSingleClaimantRootVsLastReader(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc claimant race unlink failure")
	src, _, stub := lplcFailingSource(t, bytes.Repeat([]byte("R"), 48), sentinel)
	r, err := src.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Race root Close against the final reader Close behind a start gate.
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var rootErr, readerErr error
	go func() {
		defer wg.Done()
		<-start
		rootErr = src.Close()
	}()
	go func() {
		defer wg.Done()
		<-start
		readerErr = r.Close()
	}()
	close(start)
	wg.Wait()

	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want exactly 1 (single claimant)", got)
	}
	// Exactly one closer delivers the failure; the other sees nil.
	if (rootErr == nil) == (readerErr == nil) {
		t.Fatalf("want exactly one removal error, got root=%v reader=%v", rootErr, readerErr)
	}
	delivered := rootErr
	if delivered == nil {
		delivered = readerErr
	}
	if !errors.Is(delivered, sentinel) {
		t.Fatalf("delivered error got %v, want identity %v", delivered, sentinel)
	}
}

func TestLargePayloadLifecycle_SpillDescriptorClosesBeforeCensusRelease(t *testing.T) {
	t.Parallel()

	t.Run("completed source reader", func(t *testing.T) {
		t.Parallel()
		var stub lplcRemoveStub
		stub.err = errors.New("lplc barrier unlink failure")
		seen := make(chan int, 1)
		var src *largebody.CompletedSource
		var err error
		src, err = largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			Memory:     []byte("prefix"),
			FilePath:   filepath.Join(t.TempDir(), "barrier.tmp"),
			Size:       64,
			RemoveFile: stub.remove,
			OpenFile: func(path string) (io.ReadCloser, error) {
				return &lplcStubRC{closeFn: func() error {
					// Reentrant by design: proves the lifecycle mutex is
					// NOT held while the descriptor closes (else deadlock).
					seen <- src.ActiveReaders()
					return nil
				}}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}
		r, err := src.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := src.Close(); err != nil {
			t.Fatalf("root Close: %v", err)
		}
		_ = lplcCloseGuarded(t, r.Close)
		select {
		case n := <-seen:
			if n != 1 {
				t.Fatalf("ActiveReaders during descriptor close got %d, want 1 (census released after)", n)
			}
		default:
			t.Fatal("descriptor Close was never invoked")
		}
	})

	t.Run("spill buffer reader", func(t *testing.T) {
		t.Parallel()
		seen := make(chan int, 1)
		var buf *largebody.SpillBuffer
		var err error
		buf, err = largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 8,
			OpenFile: func(path string) (io.ReadCloser, error) {
				return &lplcStubRC{closeFn: func() error {
					seen <- buf.ActiveReaders()
					return nil
				}}, nil
			},
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		if _, err := buf.Write(bytes.Repeat([]byte("B"), 64)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		r, err := buf.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_ = lplcCloseGuarded(t, r.Close)
		select {
		case n := <-seen:
			if n != 1 {
				t.Fatalf("ActiveReaders during descriptor close got %d, want 1 (census released after)", n)
			}
		default:
			t.Fatal("descriptor Close was never invoked")
		}
		_ = buf.Close()
	})
}

func TestLargePayloadLifecycle_SpillSharedCaptureLifecycleFailure(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc shared lifecycle unlink failure")
	var stub lplcRemoveStub
	stub.err = sentinel
	buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         t.TempDir(),
		MemorySpoolBytes: 8,
		RemoveFile:       stub.remove,
	})
	if err != nil {
		t.Fatalf("NewSpillBuffer: %v", err)
	}
	if _, err := buf.Write(bytes.Repeat([]byte("H"), 64)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Pre-completion reader shares ONE lifecycle through Complete (4.1).
	oldReader, err := buf.Open()
	if err != nil {
		t.Fatalf("pre-completion Open: %v", err)
	}
	src, err := buf.Complete()
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	filePath := src.FilePath()
	if filePath == "" {
		t.Fatal("expected spill file")
	}

	if err := lplcCloseGuarded(t, src.Close); err != nil {
		t.Fatalf("root Close with pre-completion reader got %v, want nil (deferred)", err)
	}

	err = lplcCloseGuarded(t, oldReader.Close)
	if !errors.Is(err, sentinel) {
		t.Fatalf("pre-completion reader Close got %v, want deferred identity %v", err, sentinel)
	}
	if src.FilePath() != filePath {
		t.Fatalf("FilePath got %q, want retained %q", src.FilePath(), filePath)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want 1 across shared lifecycle", got)
	}

	// Buffer root Close after Complete delegates to the shared lifecycle:
	// already resolved as failure, so nil with no retry.
	if err := buf.Close(); err != nil {
		t.Fatalf("buf.Close after Complete got %v, want nil", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("unlink calls got %d, want still 1", got)
	}
}

func TestLargePayloadLifecycle_SpillReservationReleasedExactlyOnceDespiteFailure(t *testing.T) {
	t.Parallel()

	t.Run("immediate failure", func(t *testing.T) {
		t.Parallel()
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
		sentinel := errors.New("lplc reservation immediate unlink failure")
		var stub lplcRemoveStub
		stub.err = sentinel
		src, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
			FilePath:    filepath.Join(t.TempDir(), "res.tmp"),
			Size:        32,
			Reservation: res,
			RemoveFile:  stub.remove,
		})
		if err != nil {
			t.Fatalf("NewCompletedSource: %v", err)
		}

		err = lplcCloseGuarded(t, src.Close)
		if !errors.Is(err, sentinel) {
			t.Fatalf("Close got %v, want %v", err, sentinel)
		}
		// Logical reservation releases exactly once on root close (4.4),
		// independent of the unlink outcome.
		if got := ledger.InflightBytes(); got != 0 {
			t.Fatalf("InflightBytes got %d, want 0 after root close", got)
		}
		if got := ledger.ActiveReservations(); got != 0 {
			t.Fatalf("ActiveReservations got %d, want 0 after root close", got)
		}
		if err := src.Close(); err != nil {
			t.Fatalf("repeated Close got %v, want nil", err)
		}
		if got := ledger.InflightBytes(); got != 0 {
			t.Fatalf("InflightBytes after repeat got %d, want 0 (exact-once)", got)
		}
	})

	t.Run("deferred failure", func(t *testing.T) {
		t.Parallel()
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
		sentinel := errors.New("lplc reservation deferred unlink failure")
		var stub lplcRemoveStub
		stub.err = sentinel
		buf, err := largebody.NewSpillBuffer(largebody.SpillConfig{
			SpoolDir:         t.TempDir(),
			MemorySpoolBytes: 8,
			Reservation:      res,
			RemoveFile:       stub.remove,
		})
		if err != nil {
			t.Fatalf("NewSpillBuffer: %v", err)
		}
		if _, err := buf.Write(bytes.Repeat([]byte("V"), 64)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		src, err := buf.Complete()
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		r, err := src.Open()
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		if err := lplcCloseGuarded(t, src.Close); err != nil {
			t.Fatalf("root Close got %v, want nil (deferred)", err)
		}
		if got := ledger.InflightBytes(); got != 0 {
			t.Fatalf("InflightBytes got %d, want 0 (released at root close)", got)
		}

		err = lplcCloseGuarded(t, r.Close)
		if !errors.Is(err, sentinel) {
			t.Fatalf("final reader Close got %v, want %v", err, sentinel)
		}
		if got := ledger.InflightBytes(); got != 0 {
			t.Fatalf("InflightBytes after deferred failure got %d, want 0", got)
		}
		if got := ledger.ActiveReservations(); got != 0 {
			t.Fatalf("ActiveReservations got %d, want 0", got)
		}
	})
}

func TestLargePayloadLifecycle_SpillFailedRemovalCreatesNoSideFiles(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("lplc no-side-files unlink failure")
	content := bytes.Repeat([]byte("Q"), 32)
	src, path, _ := lplcFailingSource(t, content, sentinel)
	dir := filepath.Dir(path)

	_ = lplcCloseGuarded(t, src.Close)

	// No retry queue, journal, or worker output: the spool dir holds only the
	// original (unresolved) spill file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("spool dir holds %v, want only the unresolved spill file (no retry/persistence)", names)
	}
}
