package runtimebundle

import (
	"context"
	"fmt"
	"strings"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
)

//nolint:revive // owner is the resource owner parameter
func buildTerminalWorkWithSetReconcile(owner *processResourceOwner, ctx context.Context, prod ProductionOptions, clock func() time.Time, bundle *metrics.Bundle, conc *concurrencyapp.Service, snapshotPub *snapshotgen.Publisher) (*terminalWorkRuntime, error) {
	twRT, err := buildTerminalWorkFromProduction(owner, prod, clock, bundle, snapshotPub)
	if err != nil {
		return nil, err
	}
	if err := reconcileUncertainLeaseSets(ctx, conc, twRT); err != nil {
		return nil, err
	}
	return twRT, nil
}

// reconcileUncertainLeaseSets scans uncertain sets conservatively and ensures
// durable release_lease_set intents exist so pending work resumes after restart.
func reconcileUncertainLeaseSets(ctx context.Context, conc *concurrencyapp.Service, tw *terminalWorkRuntime) error {
	if conc == nil {
		return nil
	}
	ctx = ctxOrBackground(ctx)
	ids, err := conc.ReconcileUncertainSets(ctx)
	if err != nil {
		return fmt.Errorf("runtimebundle: reconcile uncertain lease sets: %w", err)
	}
	if tw == nil || tw.Intents == nil {
		return nil
	}
	for _, setID := range ids {
		setID = strings.TrimSpace(setID)
		if setID == "" {
			continue
		}
		if err := tw.Intents.AcceptLeaseSetRelease(ctx, terminalworkapp.LeaseSetReleaseInput{
			RequestID:  "reconcile:" + setID,
			LeaseSetID: setID,
			Reason:     "startup_uncertain_reconcile",
		}); err != nil {
			return fmt.Errorf("runtimebundle: accept lease set release for %s: %w", setID, err)
		}
	}
	return nil
}
