package runtimebundle

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestPluginBuildCleanup_ImmediateRegisterReverseDispose(t *testing.T) {
	t.Parallel()
	var order []int
	var cleaned atomic.Int64
	closers := []func() error{}
	closers = RegisterPluginBuildCleanup(closers, func() error {
		order = append(order, 1)
		cleaned.Add(1)
		return nil
	})
	closers = append(closers, func() error {
		order = append(order, 2)
		return nil
	})
	if len(closers) != 2 {
		t.Fatalf("closers=%d", len(closers))
	}
	err := withDisposedClosers(errors.New("later assembly failed"), closers)
	if err == nil {
		t.Fatal("expected build error preserved")
	}
	if cleaned.Load() != 1 {
		t.Fatalf("plugin cleanup not run: %d", cleaned.Load())
	}
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("want reverse dispose order [2,1], got %v", order)
	}
}

func TestPluginBuildCleanup_IdempotentCleanupFunc(t *testing.T) {
	t.Parallel()
	var n atomic.Int64
	onceCleanup := func() func() error {
		var done atomic.Bool
		return func() error {
			if done.CompareAndSwap(false, true) {
				n.Add(1)
			}
			return nil
		}
	}()
	closers := RegisterPluginBuildCleanup(nil, onceCleanup)
	_ = withDisposedClosers(errors.New("fail"), closers)
	_ = onceCleanup()
	if n.Load() != 1 {
		t.Fatalf("cleanup ran %d times", n.Load())
	}
}
