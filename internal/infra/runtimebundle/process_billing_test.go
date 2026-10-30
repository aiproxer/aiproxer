package runtimebundle

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
)

// failingStartTerminalSink is a complete external sink whose owned-resource
// start fails. The failed start must leave no close registration behind.
type failingStartTerminalSink struct {
	closes int
}

func (*failingStartTerminalSink) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return nil
}

func (*failingStartTerminalSink) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return nil
}

func (*failingStartTerminalSink) StartOwnedResources(context.Context) error {
	return errors.New("owned resource start failed")
}

func (s *failingStartTerminalSink) Close() error {
	s.closes++
	return nil
}

func TestConfigureExternalBillingLifecycleStartFailureRegistersNoClose(t *testing.T) {
	t.Parallel()

	sink := &failingStartTerminalSink{}
	opts := &BuildOptions{Production: completeExternalProduction(sink)}
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}

	err := configureExternalBillingLifecycle(owner, opts)
	if err == nil {
		t.Fatal("a failed owned-resource start must fail the process build")
	}
	if len(closers) != 0 {
		t.Fatalf("failed start registered %d process closers, want none", len(closers))
	}
	if sink.closes != 0 {
		t.Fatalf("failed start closed the sink %d times, want none", sink.closes)
	}
}

var _ billingport.TerminalUsageSink = (*failingStartTerminalSink)(nil)
