package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type boundInterleavedCatalogRecorder struct {
	inner       *modelcatalog.CatalogResolverImpl
	mu          sync.Mutex
	generations []string
}

func (r *boundInterleavedCatalogRecorder) Resolve(ctx context.Context, c routing.AttemptCandidate, call aipapi.Call, caps aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	facts := r.inner.Resolve(ctx, c, call, caps)
	r.mu.Lock()
	r.generations = append(r.generations, facts.Snapshot.Generation)
	r.mu.Unlock()
	return facts
}

func TestBoundModel_InterleavedContinuationRetainsBoundCatalogOnBareContext(t *testing.T) {
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "bound-model-reservation",
			ReservedAmount: authorityInputAmount(9),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, from := setupInterleavedAuthorityContinuation(t, auth, "hidden")

	catalog := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"model-1": {Source: modelcatalog.FactSourceCatalog},
	})
	idxB := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"model-1": {Source: modelcatalog.FactSourceCatalog},
	})
	catalog.PublishSnapshot(modelcatalog.Snapshot{Generation: "catalog-A", Index: idxA})
	from = withTestRecvFacts(from, func(f recvTurnFacts) recvTurnFacts {
		f.boundCatalog = catalog.BoundView()
		f.boundCatalogOK = true
		return f
	})
	catalog.PublishSnapshot(modelcatalog.Snapshot{Generation: "catalog-B", Index: idxB})

	recorder := &boundInterleavedCatalogRecorder{inner: modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		catalog,
	)}
	ex.CatalogResolver = recorder

	rs, err := ex.openInterleavedExecutorContinuation(context.Background(), from, interleavedstate.State{})
	if err != nil {
		t.Fatalf("open continuation: %v", err)
	}
	defer func() { assert.NoError(t, rs.Close()) }()

	recorder.mu.Lock()
	got := append([]string(nil), recorder.generations...)
	recorder.mu.Unlock()
	if len(got) == 0 {
		t.Fatal("catalog resolver was not called")
	}
	for _, generation := range got {
		if generation != "catalog-A" {
			t.Fatalf("interleaved continuation used catalog %q, want bound catalog-A; observations=%v", generation, got)
		}
	}
}
