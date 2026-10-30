package runtime

import (
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

func decisionViewsFromRequestMeta(meta request.RequestMeta, attempt execview.AttemptView) execctx.Views {
	return execctx.Views{
		Principal:   meta.Principal,
		Scope:       meta.Scope,
		Session:     meta.Session,
		Attempt:     attempt,
		Workspace:   meta.Workspace,
		Annotations: meta.Annotations,
	}
}
