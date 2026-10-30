package runtimebundle

import (
	"context"
)

func bindSharedMutableProcessServices(ctx context.Context, ps *ProcessServices, shared *sharedMutableRuntime) error {
	ps.sharedMutable = shared
	ps.ALegLifecycle = shared.ALegLifecycle
	ps.ExtensionState = shared.ExtensionState
	ps.AffinityStore = &processAffinityHandle{reg: shared.affinity}
	ps.CandidateHealth = shared.underlyingHealth
	return nil
}
