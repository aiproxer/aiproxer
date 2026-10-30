package continuation

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// MaterializeCall resolves proxy-owned continuation state into a fresh
// item-authority call. Client continuation values are consumed by the store;
// the core-owned A-leg identity remains available to continuity stages.
func MaterializeCall(ctx context.Context, input aipcont.MaterializeInput, base aipapi.Call) (aipapi.Call, aipcont.MaterializedTrajectory, error) {
	trajectory, err := aipcont.Materialize(ctx, input)
	if err != nil {
		return aipapi.Call{}, aipcont.MaterializedTrajectory{}, err
	}
	out := aipapi.CloneCall(base)
	out.Items = aipcont.CloneItems(trajectory.Items)
	out.Messages = nil
	out.Instructions = nil
	out.Session.ClientSessionID = ""
	out.Session.ContinuityKey = ""
	out.Session.AuthoritativeSessionID = ""
	out.Session.ResumeToken = ""
	aipcont.MarkMaterialized(&out)
	return out, trajectory, nil
}
