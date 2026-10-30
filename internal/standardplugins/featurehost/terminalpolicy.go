package featurehost

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

type terminalPolicyReaderAdapter struct {
	store *sessionpolicy.Store
}

var _ runtime.TerminalPolicyReader = (*terminalPolicyReaderAdapter)(nil)

// NewTerminalPolicyReaderAdapter wraps a sessionpolicy.Store as a runtime.TerminalPolicyReader.
// If store is nil, the returned adapter safely returns the generation default without error.
func NewTerminalPolicyReaderAdapter(store *sessionpolicy.Store) runtime.TerminalPolicyReader {
	return &terminalPolicyReaderAdapter{store: store}
}

func (a *terminalPolicyReaderAdapter) Effective(ctx context.Context, in runtime.TerminalPolicyQuery) (runtime.TerminalPolicySnapshot, error) {
	if a == nil || a.store == nil {
		return runtime.TerminalPolicySnapshot{
			EffectiveEnabled: in.GenerationDefault,
			Revision:         0,
		}, nil
	}

	key := sessionpolicy.Key{
		SecureSessionIncarnation: in.SecureSessionIncarnation,
		ALegID:                   in.ALegID,
		FeatureID:                in.FeatureID,
	}
	authority := sessionpolicy.Authority{
		SecureSessionIncarnation: in.SecureSessionIncarnation,
		ALegID:                   in.ALegID,
		Authorized:               true,
	}

	snap, err := a.store.Snapshot(ctx, authority, key, in.GenerationDefault)
	if err != nil {
		return runtime.TerminalPolicySnapshot{}, err
	}

	return runtime.TerminalPolicySnapshot{
		EffectiveEnabled: snap.EffectiveEnabled,
		Revision:         snap.Revision,
	}, nil
}
