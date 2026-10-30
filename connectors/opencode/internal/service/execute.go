package service

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		if i == nil || i.router == nil {
			return nil, fmt.Errorf("opencode: instance not configured")
		}
		resolved, err := i.resolveModel(ctx, inv)
		if err != nil {
			return nil, err
		}
		// Quota-aware pre-output selection across ordered host-issued keys:
		// the first usable key stays preferred, auth-invalid and
		// quota-exhausted keys advance, and pool exhaustion maps to a
		// recoverable pre-output error. Provider-specific failures return
		// as-is with existing transport behavior.
		return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			return i.router.OpenWithKey(ctx, call, resolved, cred.Secret)
		})
	})
}
