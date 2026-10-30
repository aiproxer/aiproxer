package feature_test

import (
	"context"
	"fmt"
	"log"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type exampleTransform struct {
	id string
}

func (t exampleTransform) ID() string                     { return t.id }
func (t exampleTransform) Order() int                     { return 10 }
func (t exampleTransform) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (t exampleTransform) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, svc request.Services) error {
	return nil
}

type exampleLifecycle struct {
	id string
}

func (l exampleLifecycle) Start(ctx context.Context) error { return nil }
func (l exampleLifecycle) Stop(ctx context.Context) error  { return nil }

func ExampleContribute() {
	cs := feature.NewContributionSet()
	xform := exampleTransform{id: "example.transform"}

	if err := feature.Contribute(cs, feature.PlaneRequestTransforms, "example.plugin", []request.Transform{xform}); err != nil {
		log.Fatalf("contribute failed: %v", err)
	}

	bundle := feature.BundleFromPlanes(cs.Freeze(), nil)
	if err := bundle.Validate(); err != nil {
		log.Fatalf("bundle validation failed: %v", err)
	}

	transforms := feature.Get(bundle.PlaneSet, feature.PlaneRequestTransforms)
	fmt.Printf("%d %s\n", len(transforms), transforms[0].ID())

	// Output:
	// 1 example.transform
}

func ExampleGet_absent() {
	var empty feature.FrozenPlaneSet

	// Reading an unpopulated or absent plane returns the zero value for that plane type.
	transforms := feature.Get(empty, feature.PlaneRequestTransforms)
	fmt.Println(len(transforms))

	// Output:
	// 0
}

func ExampleBundleFromPlanes() {
	lifecycles := []plugin.Lifecycle{exampleLifecycle{id: "original"}}

	bundle := feature.BundleFromPlanes(feature.FrozenPlaneSet{}, lifecycles)

	// Mutate the local slice to demonstrate that BundleFromPlanes defensively cloned it at call time.
	lifecycles[0] = exampleLifecycle{id: "mutated"}

	if lc, ok := bundle.Lifecycles[0].(exampleLifecycle); ok {
		fmt.Println(lc.id)
	}

	// Output:
	// original
}
