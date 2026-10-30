package runtimebundle

import (
	"errors"
	"fmt"
	"testing"
)

// TestWithDisposedClosers covers the Build error-cleanup helper directly. The
// closer-leak tests assert disposal side effects but not error joining, so this
// focuses on: nil err pass-through, err pass-through on successful disposal,
// joined error on disposal failure, and disposal-error surfacing when err is
// nil.
func TestWithDisposedClosers(t *testing.T) {
	t.Parallel()

	buildErr := errors.New("build step failed")
	disposalErr := errors.New("closer exploded")

	closersNoErr := []func() error{
		func() error { return nil },
		func() error { return nil },
	}
	closersWithErr := []func() error{
		func() error { return disposalErr },
	}

	t.Run("nil err and clean disposal returns nil", func(t *testing.T) {
		t.Parallel()
		if got := withDisposedClosers(nil, closersNoErr); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("non-nil err and clean disposal returns err unchanged", func(t *testing.T) {
		t.Parallel()
		got := withDisposedClosers(buildErr, closersNoErr)
		if !errors.Is(got, buildErr) {
			t.Fatalf("expected returned error to wrap buildErr, got %v", got)
		}
		if got.Error() != buildErr.Error() {
			t.Fatalf("expected unchanged error text %q, got %q", buildErr.Error(), got.Error())
		}
	})

	t.Run("non-nil err and disposal failure returns joined error", func(t *testing.T) {
		t.Parallel()
		got := withDisposedClosers(buildErr, closersWithErr)
		if !errors.Is(got, buildErr) {
			t.Fatalf("expected joined error to wrap buildErr, got %v", got)
		}
		if !errors.Is(got, disposalErr) {
			t.Fatalf("expected joined error to wrap disposalErr, got %v", got)
		}
	})

	t.Run("nil err and disposal failure returns disposal error", func(t *testing.T) {
		t.Parallel()
		got := withDisposedClosers(nil, closersWithErr)
		if !errors.Is(got, disposalErr) {
			t.Fatalf("expected disposal error to surface, got %v", got)
		}
	})

	t.Run("empty closers and nil err returns nil", func(t *testing.T) {
		t.Parallel()
		if got := withDisposedClosers(nil, nil); got != nil {
			t.Fatalf("expected nil for empty closers, got %v", got)
		}
	})

	t.Run("joins multiple disposal errors", func(t *testing.T) {
		t.Parallel()
		second := errors.New("second closer failed")
		closers := []func() error{
			func() error { return disposalErr },
			func() error { return second },
		}
		got := withDisposedClosers(fmt.Errorf("runtimebundle: %w", buildErr), closers)
		if !errors.Is(got, buildErr) || !errors.Is(got, disposalErr) || !errors.Is(got, second) {
			t.Fatalf("expected joined error to wrap all three errors, got %v", got)
		}
	})
}
