package featurebundle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
)

// --- Stubs for scalar and exclusive characterization ---

type charStubSubmitHook struct{ tag string }

func (h charStubSubmitHook) ID() string                      { return h.tag }
func (charStubSubmitHook) Order() int                        { return 0 }
func (charStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charStubTerminalProvider struct {
	tag string
}

func (p charStubTerminalProvider) ID() string { return p.tag }

func (charStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type charStubBadIDTerminalProvider struct {
	badID string
}

func (p charStubBadIDTerminalProvider) ID() string { return p.badID }

func (charStubBadIDTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type charStubPanicTerminalProvider struct{}

func (charStubPanicTerminalProvider) ID() string { panic("provider id explosion") }

func (charStubPanicTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func testBundle(fn func(cs *aipfeature.ContributionSet)) aipfeature.FeatureBundle {
	cs := aipfeature.NewContributionSet()
	if fn != nil {
		fn(cs)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
}

// --- Acceptance Criteria 1: Finalizer-Cap Merge Path ---

// TestFinalizerCap_MergePathMinReductionAndZeroSemantics pins requirement 4.4 and 1.2:
// - Positive values are min-reduced across merged bundles in registration order.
// - Zero values represent "unset" and do not reduce or set the finalizer cap.
// - Negative values are non-positive and ignored at merge time (rejected by FeatureBundle.Validate).
// - Equal positive values exhibit idempotence.
// - An empty bundle list or all-zero bundles result in 0.
func TestFinalizerCap_MergePathMinReductionAndZeroSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		bundles []aipfeature.FeatureBundle
		want    int
	}{
		{
			name: "single_positive_cap",
			bundles: []aipfeature.FeatureBundle{
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
			},
			want: 4096,
		},
		{
			name: "decreasing_sequence_min_reduction",
			bundles: []aipfeature.FeatureBundle{
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 8192)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
			},
			want: 1024,
		},
		{
			name: "increasing_sequence_min_reduction",
			bundles: []aipfeature.FeatureBundle{
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 8192)
				}),
			},
			want: 1024,
		},
		{
			name: "equal_value_idempotence",
			bundles: []aipfeature.FeatureBundle{
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
			},
			want: 2048,
		},
		{
			name: "interspersed_zeros_zero_as_unset",
			bundles: []aipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 4096)
				}),
				testBundle(nil),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 1024)
				}),
				testBundle(nil),
			},
			want: 1024,
		},
		{
			name: "interspersed_negatives_ignored",
			bundles: []aipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(func(cs *aipfeature.ContributionSet) {
					_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "test", 2048)
				}),
				testBundle(nil),
			},
			want: 2048,
		},
		{
			name: "all_zero_bundles",
			bundles: []aipfeature.FeatureBundle{
				testBundle(nil),
				testBundle(nil),
			},
			want: 0,
		},
		{
			name:    "empty_bundle_list",
			bundles: nil,
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gen, err := MergeBundlesGenerated(tt.bundles...)
			require.NoError(t, err)
			require.Equal(t, tt.want, aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes))
		})
	}

	t.Run("step_by_step_receiver_append_mutation", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		require.Equal(t, 0, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 1. First contribution sets value
		err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096)
		require.NoError(t, err)
		require.Equal(t, 4096, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 2. Larger contribution ignored (min-reduction)
		err = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", 8192)
		require.NoError(t, err)
		require.Equal(t, 4096, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 3. Smaller contribution reduces value
		err = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-3", 2048)
		require.NoError(t, err)
		require.Equal(t, 2048, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 4. Zero contribution ignored (unset)
		err = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-4", 0)
		require.NoError(t, err)
		require.Equal(t, 2048, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))

		// 5. Negative contribution fails validation
		err = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-5", -100)
		require.Error(t, err)
		require.Equal(t, 2048, aipfeature.Get(cs.Freeze(), aipfeature.PlaneToolCallFinalizationMaxArgsBytes))
	})
}

// --- Acceptance Criteria 2: Exclusive Terminal Decision Provider ---

// TestTerminalDecision_ExclusiveOccupationAndConflictText pins requirement 1.2, 4.2, and 5.1:
// - Zero providers leaves slot nil.
// - Single valid provider occupies slot.
// - Second distinct provider fails with exact ErrExclusiveConflict wrapping and %q and %q formatting.
// - Candidate is discarded on conflict.
// - Same-provider re-contribution is characterized (returns conflict error with duplicate IDs).
// - Fail-before-mutate: invalid or conflicting provider contribution leaves candidate completely unmodified.
func TestTerminalDecision_ExclusiveOccupationAndConflictText(t *testing.T) {
	t.Parallel()

	provA := charStubTerminalProvider{tag: "alg.provider.a"}
	provB := charStubTerminalProvider{tag: "alg.provider.b"}

	t.Run("zero_providers_leaves_slot_nil", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "test", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "h1"}})
			}),
		)
		require.NoError(t, err)
		require.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("single_valid_provider_occupies_slot", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
		)
		require.NoError(t, err)
		require.Equal(t, provA, aipfeature.Get(gen.Frozen, aipfeature.PlaneTerminalDecisionProvider))
	})

	t.Run("distinct_providers_conflict_exact_text_and_type", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provB))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
		require.ErrorIs(t, err, aipfeature.ErrTerminalDecisionProviderConflict)
		require.Contains(t, err.Error(), `"`+provA.ID()+`" and "`+provB.ID()+`"`)
		require.Equal(t, GeneratedMergeSurface{}, gen)
	})

	t.Run("same_provider_recontribution_characterization", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provA))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
		require.ErrorIs(t, err, aipfeature.ErrTerminalDecisionProviderConflict)
		require.Contains(t, err.Error(), `"`+provA.ID()+`" and "`+provA.ID()+`"`)
		require.Equal(t, GeneratedMergeSurface{}, gen)
	})

	t.Run("fail_before_mutate_on_exclusive_conflict", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "plugin-a", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "hook-init"}})
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-a", terminaldecision.Provider(provA))
			}),
			testBundle(func(cs *aipfeature.ContributionSet) {
				_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "plugin-b", []sdkhooks.SubmitHook{charStubSubmitHook{tag: "hook-new"}})
				_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(provB))
			}),
		)
		require.Error(t, err)
		require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
		require.Equal(t, GeneratedMergeSurface{}, gen, "candidate must not be returned when exclusive provider conflicts")
	})

	t.Run("fail_before_mutate_on_invalid_incoming_provider", func(t *testing.T) {
		t.Parallel()
		badProv := charStubBadIDTerminalProvider{badID: ""}
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(badProv))
		require.Error(t, err)
		require.Nil(t, aipfeature.Get(cs.Freeze(), aipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider is invalid")
	})

	t.Run("fail_before_mutate_on_typed_nil_provider", func(t *testing.T) {
		t.Parallel()
		var typedNil *charStubTerminalProvider
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(typedNil))
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		require.Nil(t, aipfeature.Get(cs.Freeze(), aipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider is typed nil")
	})

	t.Run("fail_before_mutate_on_panicking_provider", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "plugin-b", terminaldecision.Provider(charStubPanicTerminalProvider{}))
		require.Error(t, err)
		require.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		require.Nil(t, aipfeature.Get(cs.Freeze(), aipfeature.PlaneTerminalDecisionProvider), "candidate must not be mutated when incoming provider panics during ID()")
	})
}

// TestTerminalDecision_ProviderIdentityValidation pins valid vs invalid provider IDs.
func TestTerminalDecision_ProviderIdentityValidation(t *testing.T) {
	t.Parallel()

	validIDs := []string{
		"alg.breach.v1",
		"provider-1",
		"my_provider",
		"vendor.loop-guard.v2",
	}
	for _, id := range validIDs {
		t.Run("valid_"+id, func(t *testing.T) {
			t.Parallel()
			prov := charStubTerminalProvider{tag: id}
			cs := aipfeature.NewContributionSet()
			err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "test", terminaldecision.Provider(prov))
			require.NoError(t, err)
			require.Equal(t, prov, aipfeature.Get(cs.Freeze(), aipfeature.PlaneTerminalDecisionProvider))
		})
	}

	invalidCases := []struct {
		name     string
		provider terminaldecision.Provider
	}{
		{"empty_id", charStubBadIDTerminalProvider{badID: ""}},
		{"whitespace_only_id", charStubBadIDTerminalProvider{badID: "   \t\n"}},
		{"invalid_utf8", charStubBadIDTerminalProvider{badID: "\xff\xfe\xfd"}},
		{"exceeds_max_bytes", charStubBadIDTerminalProvider{badID: strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1)}},
		{"panicking_provider", charStubPanicTerminalProvider{}},
		{"typed_nil_provider", (*charStubTerminalProvider)(nil)},
	}
	for _, tc := range invalidCases {
		t.Run("invalid_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cs := aipfeature.NewContributionSet()
			err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "test", tc.provider)
			require.Error(t, err)
		})
	}
}
