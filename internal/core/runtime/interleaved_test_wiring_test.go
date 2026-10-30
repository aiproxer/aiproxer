package runtime_test

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

type autoRegisteringConversationReader struct {
	store *conversationview.ReferenceStore
}

func (a *autoRegisteringConversationReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	_ = a.store.CreateALeg(ctx, aLegID)
	return a.store.Snapshot(ctx, aLegID)
}

func wireInterleavedTestSteering(ex *runtime.Executor) *conversationview.ReferenceStore {
	cv := conversationview.NewReferenceStore()
	ex.ConversationViewReader = &autoRegisteringConversationReader{store: cv}
	ex.SteeringWriterFactory = func(ctx context.Context, aLegID string, resolver runtime.SteeringWriterResolver) (steering.Writer, error) {
		_ = cv.CreateALeg(ctx, aLegID)
		var trajResolver sdkadapter.TrajectoryResolver
		if resolver != nil {
			trajResolver = sdkadapter.TrajectoryResolver(resolver)
		}
		return sdkadapter.NewWriter(cv, aLegID, trajResolver)
	}
	return cv
}
