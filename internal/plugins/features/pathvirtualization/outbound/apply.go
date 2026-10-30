package outbound

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// apply derives from the pinned root, rewrites transactionally, and fails open.
// Root acquisition and SDK return values remain the adapters' responsibility.
func apply(call *aipapi.Call, root string, pass Pass, bind func(pathvirtualization.Mapping) virtualizer) Report {
	mapping, rootReason := pathvirtualization.DeriveMapping(root)
	if rootReason != pathvirtualization.SkipReasonNone {
		return Report{Pass: pass, Outcome: OutcomeProjectRootUnusable, RootReason: rootReason}
	}
	published, stats, err := bind(mapping).RewriteCall(call)
	if err != nil {
		// Failed candidates and their statistics must never escape.
		return Report{Pass: pass, Outcome: OutcomeTransformationFailed}
	}
	if call != nil && published != nil && published != call {
		*call = *published
	}
	return Report{Pass: pass, Outcome: OutcomeRewriterRan, Stats: stats}
}
