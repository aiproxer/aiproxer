package runtimebundle

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// lifecycleTerminalSink is the structural external terminal sink: it satisfies
// the terminal usage port and owns the binding resources through the same
// start/close interfaces the binding adapter exposes.
type lifecycleTerminalSink struct {
	mu     sync.Mutex
	starts int
	closes int
}

func (*lifecycleTerminalSink) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return nil
}

func (*lifecycleTerminalSink) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return nil
}

func (s *lifecycleTerminalSink) StartOwnedResources(context.Context) error {
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
	return nil
}

func (s *lifecycleTerminalSink) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}

func (s *lifecycleTerminalSink) counts() (starts, closes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts, s.closes
}

// lifecycleSink is the terminal-port shape without the owned-resource starter.
type lifecycleSink struct{}

func (*lifecycleSink) AppendCall(context.Context, usageevidence.CallUsageRecord) error { return nil }

func (*lifecycleSink) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return nil
}

type lifecycleGate struct{}

func (lifecycleGate) Check(context.Context, string) error { return nil }

type lifecycleAdmission struct{}

func (lifecycleAdmission) Admit(context.Context, coreruntime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{}, nil
}

func completeExternalProduction(sink billingport.TerminalUsageSink) ProductionOptions {
	return ProductionOptions{
		BillingTerminalUsageSink: sink,
		BillingCreditGate:        lifecycleGate{},
		BillingExposureAdmission: lifecycleAdmission{},
		BillingIdentity: coreruntime.BillingIdentity{
			AccountID: func(context.Context, aipapi.Call) string { return "account" },
		},
	}
}

func TestConfigureExternalBillingLifecycleSnapshotsAndStartsOnce(t *testing.T) {
	t.Parallel()

	sink := &lifecycleTerminalSink{}
	mounts := []AdminMount{{Path: "/admin/enterprise", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}
	opts := &BuildOptions{Production: completeExternalProduction(sink)}
	opts.Production.AdminMounts = mounts
	var closers []func() error
	owner := &processResourceOwner{register: func(close func() error) { closers = append(closers, close) }}

	if err := configureExternalBillingLifecycle(owner, opts); err != nil {
		t.Fatalf("configureExternalBillingLifecycle: %v", err)
	}
	starts, closes := sink.counts()
	if starts != 1 || closes != 0 {
		t.Fatalf("lifecycle after construction = starts:%d closes:%d, want 1/0", starts, closes)
	}
	// The caller's slice is snapshotted at the process boundary.
	mounts[0] = AdminMount{Path: "/admin/mutated", Handler: http.NotFoundHandler()}
	if len(opts.Production.AdminMounts) != 1 || opts.Production.AdminMounts[0].Path != "/admin/enterprise" {
		t.Fatalf("admin mounts were not snapshotted: %+v", opts.Production.AdminMounts)
	}
	for _, closer := range slices.Backward(closers) {
		if err := closer(); err != nil {
			t.Fatalf("close process resource: %v", err)
		}
	}
	starts, closes = sink.counts()
	if starts != 1 || closes != 1 {
		t.Fatalf("lifecycle after process close = starts:%d closes:%d, want 1/1", starts, closes)
	}
}

func TestConfigureExternalBillingLifecycleSkipsWithoutBinding(t *testing.T) {
	t.Parallel()

	opts := &BuildOptions{}
	owner := &processResourceOwner{register: func(func() error) { t.Fatal("empty production registered a closer") }}
	if err := configureExternalBillingLifecycle(owner, opts); err != nil {
		t.Fatalf("configureExternalBillingLifecycle without binding: %v", err)
	}
}

func TestConfigureExternalBillingLifecycleRefusesANonOwningSink(t *testing.T) {
	t.Parallel()

	opts := &BuildOptions{Production: completeExternalProduction(&lifecycleSink{})}
	owner := &processResourceOwner{register: func(func() error) { t.Fatal("refused binding registered a closer") }}
	err := configureExternalBillingLifecycle(owner, opts)
	if !errors.Is(err, ErrAuthoritativeBillingRequired) {
		t.Fatalf("error = %v, want ErrAuthoritativeBillingRequired", err)
	}
}
