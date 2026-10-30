package service

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		model := resolveRouteModel(i.kind, inv)
		if model != "" {
			call.Route.Selector = i.kind + ":" + model
		}
		cand := routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}}
		switch {
		case i.http != nil:
			return i.http.Open(ctx, &call, cand)
		case i.app != nil:
			return i.app.Open(ctx, &call)
		default:
			return nil, fmt.Errorf("codex connector: not configured")
		}
	})
}

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
)
