package runtimebundle

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/stretchr/testify/require"
)

type wiringHandler struct {
	id  string
	ord int
}

func (h wiringHandler) ID() string                        { return h.id }
func (h wiringHandler) Order() int                        { return h.ord }
func (h wiringHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h wiringHandler) Match(_ context.Context, _ aipapi.Call, _ localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h wiringHandler) Handle(_ context.Context, _ localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{Text: "ok"}, nil
}

func TestLocalTurn_ProductionWiring_SortedFrozen(t *testing.T) {
	t.Parallel()
	// Two handlers unordered, expect snapshot sorted by Order then ID
	hA := wiringHandler{id: "b", ord: 2}
	hB := wiringHandler{id: "a", ord: 1}
	hC := wiringHandler{id: "c", ord: 1}
	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "b1", []localturn.Handler{hA})
	}, nil)
	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "b2", []localturn.Handler{hB, hC})
	}, nil)
	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	handlers := aipfeature.Get(gen.Frozen, aipfeature.PlaneLocalTurnHandlers)
	require.Len(t, handlers, 3)

	// Build snapshot via production path buildRuntimeSnapshot with FeaturePlanes
	opts := &BuildOptions{
		FeaturePlanes: gen.Frozen,
	}
	bus := hooks.New(hooks.Config{})
	snap := buildRuntimeSnapshot(bus, &config.Config{}, opts, time.Now, nil, nil, policydecision.NoopObserver{}, extensions.SecretGuardPlane{}, nil)
	got := snap.LocalTurnHandlers()
	require.Len(t, got, 3)

	// Sorted frozen: a(1), c(1), b(2)
	require.Equal(t, "a", got[0].ID())
	require.Equal(t, "c", got[1].ID())
	require.Equal(t, "b", got[2].ID())

	// Frozen: mutating source slice must not affect snapshot
	got[0] = wiringHandler{id: "mut", ord: 99}
	got2 := snap.LocalTurnHandlers()
	require.Equal(t, "a", got2[0].ID(), "frozen violated after source mutate")
}
