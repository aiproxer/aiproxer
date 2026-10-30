//go:build linux

package configsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestLeaseRegressionReleasedBorrowHasNoValidationCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, _ := NewFixedSource(path, 0)
	snap, _, err := src.ReadStable(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	v, slot := snap.TakeBaseline()
	if v.leaseUnavailable {
		_ = slot.Close(context.Background())
		t.Skip("requires supported ext4")
	}
	defer func() { _ = slot.Close(context.Background()) }()
	b, err := v.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	live, err := v.Borrow()
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	b.Release()
	if err := b.ValidateProvenance(v); err == nil {
		t.Fatal("released borrow still performs handle validation while an independent borrow is live")
	}
}

func TestLeaseRegressionMalformedMountParentFailsClosed(t *testing.T) {
	ok, err := parseExt4MountInfo([]byte("43 malformed 8:1 / /tmp rw - ext4 /dev/loop0 rw\n"), 43, 8, 1)
	if ok && err == nil {
		t.Fatal("malformed parent mount ID accepted as positive ext4 evidence")
	}
}

func TestSourceBorrowOperationsRetainHandleUntilCompletion(t *testing.T) {
	for _, operation := range []string{"baseline", "provenance", "snapshot"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(path, []byte("accepted"), 0o600); err != nil {
				t.Fatal(err)
			}
			src, err := NewFixedSource(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			snap, _, err := src.ReadStable(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			v, slot := snap.TakeBaseline()
			if !v.RequiresLease() || v.leaseUnavailable {
				t.Skip("requires supported ext4")
			}
			owner := slot.owner.Load()
			defer func() { _ = slot.Close(context.Background()) }()
			borrow, err := v.Borrow()
			if err != nil {
				t.Fatal(err)
			}
			copyBorrow := *borrow
			entered, resume := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
				borrow.Release()
			}()
			injected := errors.New("private close cause")
			var closes atomic.Int32
			owner.core.closeFile = func(f *os.File) error { closes.Add(1); _ = f.Close(); return injected }
			owner.core.captureMetadata = func(f *os.File) (sourceFileMetadata, error) { close(entered); <-resume; return captureLeaseMetadata(f) }
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "baseline":
					done <- borrow.ValidateBaseline(v)
				case "provenance":
					done <- borrow.ValidateProvenance(v)
				case "snapshot":
					done <- borrow.validateSnapshot(&snap)
				}
			}()
			<-entered
			_ = owner.RequestClose()
			copyBorrow.Release()
			borrow.Release()
			if closes.Load() != 0 {
				t.Fatal("Release closed a descriptor during an admitted syscall operation")
			}
			if err := copyBorrow.ValidateProvenance(v); err == nil {
				t.Fatal("released copy retained validation authority")
			}
			close(resume)
			if err := <-done; err != nil {
				t.Fatalf("admitted operation lost handle: %v", err)
			}
			for range 2 {
				if err := owner.Close(context.Background()); !errors.Is(err, injected) || !IsSourceCleanupError(err) {
					t.Fatalf("cached close error: %v", err)
				}
			}
			if closes.Load() != 1 {
				t.Fatalf("physical close calls=%d want once", closes.Load())
			}
		})
	}
}
