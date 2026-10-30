package runtimebundle_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

func TestCandidateRuntime_GenerationOwnedClose_DoesNotCloseProcess(t *testing.T) {
	t.Parallel()
	var candidateClosed, processClosed atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.Add("candidate-resource", runtimebundle.PhaseClose, func(context.Context) error {
		candidateClosed.Add(1)
		return nil
	})
	cand := runtimebundle.NewCandidateRuntimeForTest(ledger)
	processCloser := func() error {
		processClosed.Add(1)
		return nil
	}
	_ = processCloser // process services stay outside generation ownership

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareOwned("cand", cand)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	// BeginShutdown+DetachActive (not a replacing Publish) avoids racing
	// Manager's automatic post-publish retirement scheduling against this
	// test's manual BeginClose/Close calls.
	m.BeginShutdown()
	m.DetachActive()
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if candidateClosed.Load() != 1 {
		t.Fatalf("candidate closes=%d", candidateClosed.Load())
	}
	if processClosed.Load() != 0 {
		t.Fatalf("process closes=%d", processClosed.Load())
	}
	if err := cand.Close(); err != nil {
		t.Fatal(err)
	}
	// CandidateRuntime.Close is idempotent; generation already closed it.
	if candidateClosed.Load() != 1 {
		t.Fatalf("idempotent candidate closes=%d", candidateClosed.Load())
	}
}
