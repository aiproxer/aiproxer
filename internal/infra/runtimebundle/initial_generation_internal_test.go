package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

func TestJoinInitialFailureCleanup_OrderAndJoin(t *testing.T) {
	t.Parallel()
	var order []string
	primary := errors.New("primary boom")
	genErr := errors.New("gen rollback failed")
	psErr := errors.New("process close failed")
	traceErr := errors.New("trace shutdown failed")

	err := joinInitialFailureCleanup(
		context.Background(), primary,
		func() error {
			order = append(order, "gen")
			return genErr
		},
		func() error {
			order = append(order, "process")
			return psErr
		},
		func(context.Context) error {
			order = append(order, "trace")
			return traceErr
		},
	)
	if want := []string{"gen", "process", "trace"}; len(order) != 3 ||
		order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("order=%v want %v", order, want)
	}
	if !errors.Is(err, primary) {
		t.Fatalf("must preserve primary: %v", err)
	}
	if !errors.Is(err, genErr) || !errors.Is(err, psErr) || !errors.Is(err, traceErr) {
		t.Fatalf("must join cleanup errors: %v", err)
	}
	msg := err.Error()
	for _, secret := range []string{"password", "token", "sk-", "api_key"} {
		if strings.Contains(strings.ToLower(msg), secret) {
			t.Fatalf("cleanup error leaked secret fragment %q in %q", secret, msg)
		}
	}
}

func TestJoinInitialFailureCleanup_IgnoresAlreadyClosed(t *testing.T) {
	t.Parallel()
	primary := errors.New("primary")
	err := joinInitialFailureCleanup(
		context.Background(), primary,
		func() error { return runtimehost.ErrAlreadyClosed },
		nil,
		nil,
	)
	if !errors.Is(err, primary) {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal("ErrAlreadyClosed must not be joined")
	}
}

func TestJoinInitialFailureCleanup_PreservesMixedAlreadyClosedJoin(t *testing.T) {
	t.Parallel()
	primary := errors.New("primary")
	sentinel := errors.New("sentinel cleanup failure")
	err := joinInitialFailureCleanup(
		context.Background(), primary,
		func() error { return errors.Join(runtimehost.ErrAlreadyClosed, sentinel) },
		nil,
		nil,
	)
	if !errors.Is(err, primary) {
		t.Fatalf("must preserve primary: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("must preserve sibling cleanup sentinel: %v", err)
	}
	if !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("mixed join may still surface ErrAlreadyClosed: %v", err)
	}
}

func TestOmitSoleAlreadyClosed_SoleVsMixed(t *testing.T) {
	t.Parallel()
	if got := omitSoleAlreadyClosed(nil); got != nil {
		t.Fatalf("nil -> %v", got)
	}
	if got := omitSoleAlreadyClosed(runtimehost.ErrAlreadyClosed); got != nil {
		t.Fatalf("sole AlreadyClosed -> %v", got)
	}
	wrapped := fmt.Errorf("close: %w", runtimehost.ErrAlreadyClosed)
	if got := omitSoleAlreadyClosed(wrapped); got != nil {
		t.Fatalf("wrapped sole AlreadyClosed -> %v", got)
	}
	sentinel := errors.New("sentinel cleanup failure")
	mixed := errors.Join(runtimehost.ErrAlreadyClosed, sentinel)
	got := omitSoleAlreadyClosed(mixed)
	if !errors.Is(got, sentinel) {
		t.Fatalf("mixed join must keep sentinel: %v", got)
	}
	if !errors.Is(got, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("mixed join must keep AlreadyClosed component: %v", got)
	}
}
