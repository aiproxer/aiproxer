package runtimebundle

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/uptrace/bun"
)

// persistenceRuntime holds the continuity (B2BUA-wrapped) store and the secure-
// session runtime produced by [buildPersistenceRuntime].
type persistenceRuntime struct {
	Store         b2bua.Store
	OverrideStore routeoverride.Store
	SecureSession *secureSessionRuntime
}

// buildPersistenceRuntime opens the continuity store (postgres handles shared via
// the process pool registry), wraps it with the control-plane B2BUA projection,
// then builds the secure-session runtime. Each acquired closer is registered with
// the process owner before any later fallible construction step. Continuity errors
// get "runtimebundle: %w"; secure-session errors are returned unwrapped (the
// helper attaches context).
func buildPersistenceRuntime(owner *processResourceOwner, bctx buildContext, cp *controlPlaneRuntime, bundle *metrics.Bundle) (*persistenceRuntime, error) {
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	cfg, parent, log := bctx.Cfg, bctx.Parent, bctx.Log
	store, storeCloser, err := openContinuityStore(parent, cfg, bctx.PostgresPools, bctx.DualPlaneMigrator)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	if storeCloser != nil {
		owner.Own(storeCloser)
	}
	overrideStore, _ := routeoverride.AsStore(store)
	store = cp.wrapB2BUA(store)

	ssRun, err := buildSecureSessionRuntime(secureSessionBuildInput{
		StartupContext:        parent,
		Cfg:                   cfg,
		B2B:                   store,
		Log:                   log,
		Bundle:                bundle,
		ControlPlaneStoreWrap: cp.wrapSecureSession,
		PostgresPools:         bctx.PostgresPools,
		DualPlaneMigrator:     bctx.DualPlaneMigrator,
	})
	if err != nil {
		return nil, err
	}
	if ssRun.closer != nil {
		owner.Own(ssRun.closer)
	}
	return &persistenceRuntime{Store: store, OverrideStore: overrideStore, SecureSession: ssRun}, nil
}

func borrowContinuityDB(s any) *bun.DB {
	for s != nil {
		if p, ok := s.(interface{ DB() *bun.DB }); ok {
			return p.DB()
		}
		u, ok := s.(interface{ Unwrap() any })
		if !ok {
			break
		}
		s = u.Unwrap()
	}
	return nil
}
