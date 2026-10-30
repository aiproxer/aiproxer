package runtimebundle

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// secretGuardTestRuntime carries the planes-derived secret-guard plane and
// inventory for composition unit tests. It mirrors the production extraction
// in secretGuardFromPlanes so tests pin the same channel generic code uses.
type secretGuardTestRuntime struct {
	Plane     extensions.SecretGuardPlane
	Inventory *diag.InventorySecretGuard
}

// testBuildSecretGuardRuntime is a test helper for unit tests testing secret guard runtime composition.
func testBuildSecretGuardRuntime(cfg *config.Config, log *slog.Logger, opts *BuildOptions, regs []aipsdk.Registration) (*secretGuardTestRuntime, error) {
	if opts == nil {
		return nil, nil
	}
	mode := accessmode.ModeSingleUser
	if cfg != nil {
		var err error
		mode, err = cfg.EffectiveAccessMode()
		if err != nil {
			return nil, err
		}
	}
	// A nil logger intentionally reaches secretguardcompose through a zero
	// facade value (the historical degenerate-facade contract): the compose
	// layer raises the pinned audit error only when audit is required, while
	// explicit-observer configs succeed. A non-nil logger takes the production
	// NewProcess constructor, which owns nothing at this checkpoint.
	var fh *featurehost.Runtime
	if log != nil {
		var err error
		if fh, err = featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: log}); err != nil {
			return nil, err
		}
	} else {
		fh = &featurehost.Runtime{}
	}
	var genHostRegs []featurehost.Registration
	if opts.Production.FeatureHostRegistrations != nil {
		genHostRegs = append(genHostRegs, opts.Production.FeatureHostRegistrations...)
	}
	if opts.Testing.FeatureHostRegistrations != nil {
		genHostRegs = append(genHostRegs, opts.Testing.FeatureHostRegistrations...)
	}
	out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:     regs,
		HostRegistrations: genHostRegs,
		Planes:            opts.FeaturePlanes,
		AccessMode:        mode,
	})
	if err != nil {
		if unwrapped := errors.Unwrap(err); unwrapped != nil {
			return nil, unwrapped
		}
		return nil, err
	}
	plane, inv := secretGuardFromPlanes(out.Planes)
	return &secretGuardTestRuntime{
		Plane:     plane,
		Inventory: inv,
	}, nil
}

// bindSecretGuardAudit is a test helper alias for secret guard composition with a
// discard logger fallback when log is nil.
func bindSecretGuardAudit(cfg *config.Config, opts *BuildOptions, regs []aipsdk.Registration, log *slog.Logger) (*secretGuardTestRuntime, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	}
	return testBuildSecretGuardRuntime(cfg, log, opts, regs)
}
