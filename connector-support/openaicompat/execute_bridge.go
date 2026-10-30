package openaicompat

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ExecuteOpts configures ForwardExecute model/flavor resolution.
type ExecuteOpts struct {
	DefaultModel  string
	ResolveModel  func(inv backendplugin.Invocation, call aipapi.Call) string
	ResolveFlavor func(call aipapi.Call) Flavor
	Open          func(ctx context.Context, call aipapi.Call, model string, flavor Flavor) (aipapi.ManagedEventStream, error)
}

// ForwardExecute drives a plugin Execute stream from an OpenAI-compatible Open.
// Cancellation and event pumping are delegated to backendplugin.ForwardExecute.
func ForwardExecute(stream backendplugin.ExecuteStream, opts ExecuteOpts) error {
	if opts.Open == nil {
		return fmt.Errorf("openaicompat: Open is required")
	}
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		model := strings.TrimSpace(inv.CanonicalModelID)
		if opts.ResolveModel != nil {
			model = opts.ResolveModel(inv, call)
		}
		if model == "" {
			model = opts.DefaultModel
		}
		flavor := FlavorChat
		if opts.ResolveFlavor != nil {
			flavor = opts.ResolveFlavor(call)
		}
		return opts.Open(ctx, call, model, flavor)
	})
}
