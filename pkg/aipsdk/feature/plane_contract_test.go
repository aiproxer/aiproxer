package feature_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type testProvider struct {
	id string
}

func (p testProvider) ID() string {
	return p.id
}

func TestPlaneDeclarationValidation_RuleCompleteness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		plane     feature.PlaneDeclaration
		wantError bool
		errTarget error
		errSubstr string
	}{
		{
			name: "empty plane ID is rejected",
			plane: feature.Plane[[]string]{
				ID:           "",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "plane ID must not be empty",
		},
		{
			name: "unknown multiplicity is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.unknown_mult",
				Multiplicity: feature.MultUnknown,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "invalid multiplicity",
		},
		{
			name: "all sources CombUnsupported is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.no_sources",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature:          feature.CombUnsupported,
					Host:             feature.CombUnsupported,
					GenerationBinder: feature.CombUnsupported,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "at least one source rule must be supported",
		},
		{
			name: "exclusive multiplicity with concatenate rule is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.exclusive_concat",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Identity: func(v []string) (string, bool) {
					if len(v) > 0 {
						return v[0], true
					}
					return "", false
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "exclusive plane cannot use concatenate rule",
		},
		{
			name: "exclusive multiplicity with reduce rule is rejected",
			plane: feature.Plane[int]{
				ID:           "test.exclusive_reduce",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombReduce,
				},
				Identity: func(v int) (string, bool) {
					return fmt.Sprintf("%d", v), true
				},
				Combine: func(source feature.SourceKind, cur, inc int) (int, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "exclusive plane cannot use reduce rule",
		},
		{
			name: "ordered multiplicity with exclusive rule is rejected",
			plane: feature.Plane[string]{
				ID:           "test.ordered_exclusive",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombExclusive,
				},
				Identity: func(v string) (string, bool) {
					return v, true
				},
				Combine: func(source feature.SourceKind, cur, inc string) (string, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "ordered plane cannot use exclusive rule",
		},
		{
			name: "exclusive rule without identity extractor is rejected",
			plane: feature.Plane[testProvider]{
				ID:           "test.exclusive_no_identity",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombExclusive,
				},
				Identity: nil,
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "identity extractor required",
		},
		{
			name: "exclusive plane without ValidateIdentity is rejected",
			plane: feature.Plane[testProvider]{
				ID:           "test.exclusive_no_validate_identity",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombExclusive,
				},
				Identity: func(v testProvider) (string, bool) {
					return v.id, true
				},
				ValidateIdentity: nil,
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "cached identity validator required",
		},
		{
			name: "replace by identity rule without identity extractor is rejected",
			plane: feature.Plane[testProvider]{
				ID:           "test.replace_no_identity",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					GenerationBinder: feature.CombReplaceByIdentity,
				},
				Identity: nil,
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "identity extractor required",
		},
		{
			name: "replace by identity plane without ValidateIdentity is rejected",
			plane: feature.Plane[testProvider]{
				ID:           "test.replace_no_validate_identity",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					GenerationBinder: feature.CombReplaceByIdentity,
				},
				Identity: func(v testProvider) (string, bool) {
					return v.id, true
				},
				ValidateIdentity: nil,
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "cached identity validator required",
		},
		{
			name: "supported source with nil Combine is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.nil_combine",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: nil,
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "Combine function must not be nil",
		},
		{
			name: "undefined combination rule on Feature source is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.invalid_comb_feature",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.Combination(99),
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "invalid combination rule Combination(99) on source feature",
		},
		{
			name: "undefined combination rule on Host source is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.invalid_comb_host",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
					Host:    feature.Combination(99),
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "invalid combination rule Combination(99) on source host",
		},
		{
			name: "undefined combination rule on GenerationBinder source is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.invalid_comb_genbinder",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature:          feature.CombConcatenate,
					GenerationBinder: feature.Combination(99),
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "invalid combination rule Combination(99) on source generation_binder",
		},
		{
			name: "valid ordered concatenation plane passes",
			plane: feature.Plane[[]string]{
				ID:           "test.valid_ordered_concat",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
					Host:    feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: false,
		},
		{
			name: "valid ordered reduce plane passes",
			plane: feature.Plane[int]{
				ID:           "test.valid_ordered_reduce",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombReduce,
				},
				Combine: func(source feature.SourceKind, cur, inc int) (int, error) {
					if cur == 0 || (inc > 0 && inc < cur) {
						return inc, nil
					}
					return cur, nil
				},
			},
			wantError: false,
		},
		{
			name: "valid exclusive plane with identity passes",
			plane: feature.Plane[testProvider]{
				ID:           "test.valid_exclusive",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombExclusive,
				},
				Identity: func(v testProvider) (string, bool) {
					if v.id == "" {
						return "", false
					}
					return v.id, true
				},
				ValidateIdentity: func(id string) error {
					return nil
				},
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: false,
		},
		{
			name: "valid replace by identity plane with identity passes",
			plane: feature.Plane[testProvider]{
				ID:           "test.valid_replace_by_identity",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					GenerationBinder: feature.CombReplaceByIdentity,
				},
				Identity: func(v testProvider) (string, bool) {
					if v.id == "" {
						return "", false
					}
					return v.id, true
				},
				ValidateIdentity: func(id string) error {
					return nil
				},
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
			},
			wantError: false,
		},
		{
			name: "valid exclusive plane with identity and ExclusiveConflictError passes",
			plane: feature.Plane[testProvider]{
				ID:           "test.valid_exclusive_with_sentinel",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					Feature: feature.CombExclusive,
				},
				Identity: func(v testProvider) (string, bool) {
					if v.id == "" {
						return "", false
					}
					return v.id, true
				},
				ValidateIdentity: func(id string) error {
					return nil
				},
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
				ExclusiveConflictError: errors.New("sentinel conflict error"),
			},
			wantError: false,
		},
		{
			name: "ordered plane with ExclusiveConflictError is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.ordered_with_exclusive_conflict_error",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
				ExclusiveConflictError: errors.New("sentinel conflict error"),
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "ExclusiveConflictError requires at least one exclusive source rule",
		},
		{
			name: "exclusive multiplicity with non-exclusive rules and ExclusiveConflictError is rejected",
			plane: feature.Plane[testProvider]{
				ID:           "test.exclusive_no_comb_exclusive_with_sentinel",
				Multiplicity: feature.MultExclusive,
				Rules: feature.SourceRules{
					GenerationBinder: feature.CombReplaceByIdentity,
				},
				Identity: func(v testProvider) (string, bool) {
					if v.id == "" {
						return "", false
					}
					return v.id, true
				},
				ValidateIdentity: func(id string) error {
					return nil
				},
				Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
					return inc, nil
				},
				ExclusiveConflictError: errors.New("sentinel conflict error"),
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "ExclusiveConflictError requires at least one exclusive source rule",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.plane.ValidateDeclaration()
			if tc.wantError {
				require.Error(t, err)
				if tc.errTarget != nil {
					require.ErrorIs(t, err, tc.errTarget)
				}
				if tc.errSubstr != "" {
					assert.Contains(t, err.Error(), tc.errSubstr)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateManifest(t *testing.T) {
	t.Parallel()

	validPlane1 := feature.Plane[[]string]{
		ID:           "plane.1",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	}
	validPlane2 := feature.Plane[int]{
		ID:           "plane.2",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombReduce,
		},
		Combine: func(source feature.SourceKind, cur, inc int) (int, error) {
			return inc, nil
		},
	}

	t.Run("valid distinct planes pass", func(t *testing.T) {
		t.Parallel()
		err := feature.ValidateManifest(validPlane1, validPlane2)
		require.NoError(t, err)
	})

	t.Run("nil plane declaration is rejected", func(t *testing.T) {
		t.Parallel()
		err := feature.ValidateManifest(validPlane1, nil)
		require.ErrorIs(t, err, feature.ErrInvalidPlane)
	})

	t.Run("duplicate plane ID is rejected", func(t *testing.T) {
		t.Parallel()
		dupPlane := feature.Plane[string]{
			ID:           "plane.1", // duplicate of validPlane1 ID
			Multiplicity: feature.MultOrdered,
			Rules: feature.SourceRules{
				Feature: feature.CombConcatenate,
			},
			Combine: func(source feature.SourceKind, cur, inc string) (string, error) {
				return cur + inc, nil
			},
		}
		err := feature.ValidateManifest(validPlane1, dupPlane)
		require.ErrorIs(t, err, feature.ErrInvalidPlane)
		assert.Contains(t, err.Error(), "duplicate plane ID \"plane.1\"")
	})

	t.Run("invalid plane in manifest is rejected", func(t *testing.T) {
		t.Parallel()
		invalidPlane := feature.Plane[int]{
			ID:           "plane.invalid",
			Multiplicity: feature.MultUnknown,
			Rules: feature.SourceRules{
				Feature: feature.CombReduce,
			},
			Combine: func(source feature.SourceKind, cur, inc int) (int, error) {
				return inc, nil
			},
		}
		err := feature.ValidateManifest(validPlane1, invalidPlane)
		require.ErrorIs(t, err, feature.ErrInvalidPlane)
	})

	t.Run("invalid combination in manifest plane is rejected", func(t *testing.T) {
		t.Parallel()
		invalidPlane := feature.Plane[[]string]{
			ID:           "plane.invalid_comb",
			Multiplicity: feature.MultOrdered,
			Rules: feature.SourceRules{
				Feature: feature.Combination(99),
			},
			Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
				return append(cur, inc...), nil
			},
		}
		err := feature.ValidateManifest(validPlane1, invalidPlane)
		require.ErrorIs(t, err, feature.ErrInvalidPlane)
		assert.Contains(t, err.Error(), "invalid combination rule Combination(99)")
	})
}

func TestContribute_AttributedErrors(t *testing.T) {
	t.Parallel()

	planeConcat := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.hooks",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Validate: func(v []string) error {
			if slices.Contains(v, "invalid") {
				return errors.New("cannot contain invalid item")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	planeNoFeature := feature.BindGeneratedTestPlane(feature.Plane[string]{
		ID:           "test.host_only",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Host: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc string) (string, error) {
			return cur + inc, nil
		},
	})

	t.Run("nil contribution set returns error", func(t *testing.T) {
		t.Parallel()
		var s *feature.ContributionSet
		err := feature.Contribute(s, planeConcat, "plugin-a", []string{"ok"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil ContributionSet")
	})

	t.Run("empty plugin ID returns attributed error wrapping ErrInvalidContribution", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.Contribute(s, planeConcat, "", []string{"ok"})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrInvalidContribution)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "", attrErr.PluginID)
		assert.Equal(t, "test.hooks", attrErr.PlaneID)
	})

	t.Run("unsupported feature source returns attributed error wrapping ErrUnsupportedSource", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.Contribute(s, planeNoFeature, "plugin-a", "some-val")
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "plugin-a", attrErr.PluginID)
		assert.Equal(t, "test.host_only", attrErr.PlaneID)
		assert.Contains(t, err.Error(), "plugin \"plugin-a\" plane \"test.host_only\"")
	})

	t.Run("validation failure returns attributed error wrapping ErrInvalidContribution", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.Contribute(s, planeConcat, "plugin-bad", []string{"valid", "invalid"})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrInvalidContribution)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "plugin-bad", attrErr.PluginID)
		assert.Equal(t, "test.hooks", attrErr.PlaneID)
		assert.Contains(t, err.Error(), "cannot contain invalid item")

		// Fail-before-mutate: set is still empty
		assert.False(t, s.Has("test.hooks"))
	})

	t.Run("unbound plane returns attributed error wrapping ErrUngeneratedPlane", func(t *testing.T) {
		t.Parallel()
		planeUnbound := feature.Plane[[]string]{
			ID:           "test.unbound",
			Multiplicity: feature.MultOrdered,
			Rules: feature.SourceRules{
				Feature: feature.CombConcatenate,
			},
			Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
				return append(cur, inc...), nil
			},
		}
		s := feature.NewContributionSet()
		err := feature.Contribute(s, planeUnbound, "plugin-a", []string{"val"})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrUngeneratedPlane)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "plugin-a", attrErr.PluginID)
		assert.Equal(t, "test.unbound", attrErr.PlaneID)
	})
}

func TestContribute_ExclusiveConflict_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	exclusivePlane := feature.BindGeneratedTestPlane(feature.Plane[testProvider]{
		ID:           "test.terminal_decision_provider",
		Multiplicity: feature.MultExclusive,
		Rules: feature.SourceRules{
			Feature: feature.CombExclusive,
		},
		Identity: func(v testProvider) (string, bool) {
			if v.id == "" {
				return "", false
			}
			return v.id, true
		},
		ValidateIdentity: func(id string) error {
			if id == "" {
				return errors.New("empty identity")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc testProvider) (testProvider, error) {
			return inc, nil
		},
	})

	s := feature.NewContributionSet()

	// First contribution from plugin-alpha succeeds
	provAlpha := testProvider{id: "provider-alpha"}
	err := feature.Contribute(s, exclusivePlane, "plugin-alpha", provAlpha)
	require.NoError(t, err)

	// Validate state before second contribution
	frozen := s.Freeze()
	gotVal := feature.Get(frozen, exclusivePlane)
	assert.Equal(t, "provider-alpha", gotVal.ID())
	gotID, ok := feature.FrozenIdentity(frozen, exclusivePlane)
	assert.True(t, ok)
	assert.Equal(t, "provider-alpha", gotID)

	// Second distinct contribution from plugin-beta must fail with conflict error
	provBeta := testProvider{id: "provider-beta"}
	err = feature.Contribute(s, exclusivePlane, "plugin-beta", provBeta)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)

	// Verify AttributedError attributes
	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-beta", attrErr.PluginID)
	assert.Equal(t, "test.terminal_decision_provider", attrErr.PlaneID)

	// Verify error message preserves BOTH validated provider IDs in %q and %q shape
	errStr := err.Error()
	assert.Contains(t, errStr, `"provider-alpha" and "provider-beta"`)

	// Fail-before-mutate: candidate ContributionSet remains untouched (still holds provider-alpha only)
	frozenAfter := s.Freeze()
	gotValAfter := feature.Get(frozenAfter, exclusivePlane)
	assert.Equal(t, "provider-alpha", gotValAfter.ID())
	gotIDAfter, okAfter := feature.FrozenIdentity(frozenAfter, exclusivePlane)
	assert.True(t, okAfter)
	assert.Equal(t, "provider-alpha", gotIDAfter)

	// Attempting duplicate contribution of the same identity also fails with conflict error
	err = feature.Contribute(s, exclusivePlane, "plugin-alpha-2", provAlpha)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.Contains(t, err.Error(), `"provider-alpha" and "provider-alpha"`)

	// Set is still unmodified
	assert.Equal(t, "provider-alpha", feature.Get(s.Freeze(), exclusivePlane).ID())
}

func TestContribute_ScalarMinReduce_RegistrationOrder(t *testing.T) {
	t.Parallel()

	minReducePlane := feature.BindGeneratedTestPlane(feature.Plane[int]{
		ID:           "test.tool_call_finalization_max_args_bytes",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombReduce,
		},
		Validate: func(v int) error {
			if v < 0 {
				return errors.New("must be >= 0")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc int) (int, error) {
			if inc <= 0 {
				return cur, nil
			}
			if cur <= 0 || inc < cur {
				return inc, nil
			}
			return cur, nil
		},
	})

	tests := []struct {
		name          string
		contributions []struct {
			pluginID string
			val      int
		}
		wantResult int
	}{
		{
			name: "decreasing sequence",
			contributions: []struct {
				pluginID string
				val      int
			}{
				{"plugin-1", 1024},
				{"plugin-2", 512},
				{"plugin-3", 256},
			},
			wantResult: 256,
		},
		{
			name: "increasing sequence",
			contributions: []struct {
				pluginID string
				val      int
			}{
				{"plugin-1", 256},
				{"plugin-2", 512},
				{"plugin-3", 1024},
			},
			wantResult: 256,
		},
		{
			name: "mixed sequence with zero (unset) contribution ignored",
			contributions: []struct {
				pluginID string
				val      int
			}{
				{"plugin-1", 1024},
				{"plugin-2", 0},
				{"plugin-3", 512},
				{"plugin-4", 2048},
				{"plugin-5", 256},
				{"plugin-6", 4096},
			},
			wantResult: 256,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := feature.NewContributionSet()
			for _, c := range tc.contributions {
				err := feature.Contribute(s, minReducePlane, c.pluginID, c.val)
				require.NoError(t, err)
			}
			frozen := s.Freeze()
			got := feature.Get(frozen, minReducePlane)
			assert.Equal(t, tc.wantResult, got)
		})
	}
}

func TestContribute_OrderedConcatenation(t *testing.T) {
	t.Parallel()

	concatPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.submit_hooks",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()

	err := feature.Contribute(s, concatPlane, "plugin-1", []string{"hook1", "hook2"})
	require.NoError(t, err)

	err = feature.Contribute(s, concatPlane, "plugin-2", []string{"hook3"})
	require.NoError(t, err)

	err = feature.Contribute(s, concatPlane, "plugin-3", []string{"hook4", "hook5"})
	require.NoError(t, err)

	frozen := s.Freeze()
	got := feature.Get(frozen, concatPlane)
	assert.Equal(t, []string{"hook1", "hook2", "hook3", "hook4", "hook5"}, got)
}

func TestContribute_FallibleCombine_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	falliblePlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.fallible",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			for _, item := range inc {
				if strings.Contains(item, "error") {
					return nil, errors.New("combine rejected error item")
				}
			}
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()

	// Initial valid contribution
	err := feature.Contribute(s, falliblePlane, "plugin-1", []string{"good-1", "good-2"})
	require.NoError(t, err)

	// Second contribution fails in Combine
	err = feature.Contribute(s, falliblePlane, "plugin-2", []string{"good-3", "error-item"})
	require.Error(t, err)

	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-2", attrErr.PluginID)
	assert.Equal(t, "test.fallible", attrErr.PlaneID)
	assert.Contains(t, err.Error(), "combine rejected error item")

	// Fail-before-mutate check: set still contains only plugin-1's contribution
	frozen := s.Freeze()
	got := feature.Get(frozen, falliblePlane)
	assert.Equal(t, []string{"good-1", "good-2"}, got)
}

func TestContribute_MutatingCombiner_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	// A plane with a combiner that mutates 'cur' in-place before returning an error.
	mutatingPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.mutating_combiner",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			if len(inc) > 0 && inc[0] == "bad" {
				if len(cur) > 0 {
					cur[0] = "MUTATED_ON_ERROR"
				}
				return nil, errors.New("combiner failed intentionally")
			}
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()

	// Initial valid contribution
	err := feature.Contribute(s, mutatingPlane, "plugin-1", []string{"original-1", "original-2"})
	require.NoError(t, err)

	// Verify initial state
	frozen1 := s.Freeze()
	assert.Equal(t, []string{"original-1", "original-2"}, feature.Get(frozen1, mutatingPlane))

	// Second contribution from plugin-2 triggers mutating combiner failure
	err = feature.Contribute(s, mutatingPlane, "plugin-2", []string{"bad"})
	require.Error(t, err)
	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-2", attrErr.PluginID)
	assert.Equal(t, "test.mutating_combiner", attrErr.PlaneID)

	// Verify fail-before-mutate: s still holds the uncorrupted original values!
	frozen2 := s.Freeze()
	got := feature.Get(frozen2, mutatingPlane)
	assert.Equal(t, []string{"original-1", "original-2"}, got)
	assert.NotEqual(t, "MUTATED_ON_ERROR", got[0])
}

func TestContribute_CallerSliceMutation_Isolation(t *testing.T) {
	t.Parallel()

	concatPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.slice_isolation",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()

	inputSlice := []string{"val-1", "val-2"}
	err := feature.Contribute(s, concatPlane, "plugin-1", inputSlice)
	require.NoError(t, err)

	// Caller mutates the original slice after contributing
	inputSlice[0] = "MUTATED_AFTER_CONTRIBUTE"

	// Frozen set must NOT be affected by caller's post-contribute mutation
	frozen := s.Freeze()
	got := feature.Get(frozen, concatPlane)
	assert.Equal(t, []string{"val-1", "val-2"}, got)
}

func TestFreeze_Aliasing_Isolation(t *testing.T) {
	t.Parallel()

	concatPlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.freeze_isolation",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()

	// Initial slice with explicit spare capacity (len=3, cap=5)
	origSlice := make([]string, 3, 5)
	origSlice[0] = "item-1"
	origSlice[1] = "item-2"
	origSlice[2] = "item-3"

	err := feature.Contribute(s, concatPlane, "plugin-1", origSlice)
	require.NoError(t, err)

	frozen1 := s.Freeze()
	got1 := feature.Get(frozen1, concatPlane)
	assert.Equal(t, []string{"item-1", "item-2", "item-3"}, got1)

	// Direct index-mutation on original slice's backing array after Freeze must not affect frozen1
	origSlice[0] = "MUTATED_ORIGINAL_INDEX_0"
	got1AfterOrigMut := feature.Get(frozen1, concatPlane)
	assert.Equal(t, []string{"item-1", "item-2", "item-3"}, got1AfterOrigMut)

	// Mutating the slice retrieved from frozen1 must not corrupt s or subsequent frozen snapshots
	got1[0] = "MUTATED_FROZEN1_GET"

	// Further contribute to s after Freeze
	err = feature.Contribute(s, concatPlane, "plugin-2", []string{"item-4"})
	require.NoError(t, err)

	// frozen2 reflects the new contribution and is uncorrupted by mutations to got1 or origSlice
	frozen2 := s.Freeze()
	got2 := feature.Get(frozen2, concatPlane)
	assert.Equal(t, []string{"item-1", "item-2", "item-3", "item-4"}, got2)

	// Mutating got2 in-place must not affect subsequent reads from s or frozen3
	got2[0] = "MUTATED_FROZEN2_GET"
	frozen3 := s.Freeze()
	got3 := feature.Get(frozen3, concatPlane)
	assert.Equal(t, []string{"item-1", "item-2", "item-3", "item-4"}, got3)
}

func TestEnums_StringMethods(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "feature", feature.SourceFeature.String())
	assert.Equal(t, "host", feature.SourceHost.String())
	assert.Equal(t, "generation_binder", feature.SourceGenerationBinder.String())
	assert.Equal(t, "SourceKind(99)", feature.SourceKind(99).String())

	assert.Equal(t, "ordered", feature.MultOrdered.String())
	assert.Equal(t, "exclusive", feature.MultExclusive.String())
	assert.Equal(t, "Multiplicity(99)", feature.Multiplicity(99).String())

	assert.Equal(t, "unsupported", feature.CombUnsupported.String())
	assert.Equal(t, "concatenate", feature.CombConcatenate.String())
	assert.Equal(t, "exclusive", feature.CombExclusive.String())
	assert.Equal(t, "reduce", feature.CombReduce.String())
	assert.Equal(t, "replace_by_identity", feature.CombReplaceByIdentity.String())
	assert.Equal(t, "Combination(99)", feature.Combination(99).String())
}

func TestContributeSource_Semantics(t *testing.T) {
	t.Parallel()

	planeMultiSource := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.multi_source",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
			Host:    feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	planeFeatureOnly := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.feature_only",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	t.Run("source host supported succeeds and attributes contributor", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.ContributeSource(s, planeMultiSource, feature.SourceHost, "host-contributor", []string{"host-val"})
		require.NoError(t, err)

		frozen := s.Freeze()
		assert.Equal(t, []string{"host-val"}, feature.Get(frozen, planeMultiSource))
	})

	t.Run("source host unsupported returns attributed error wrapping ErrUnsupportedSource", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.ContributeSource(s, planeFeatureOnly, feature.SourceHost, "host-contributor", []string{"host-val"})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)

		var attrErr *feature.AttributedError
		require.True(t, errors.As(err, &attrErr))
		assert.Equal(t, "host-contributor", attrErr.PluginID)
		assert.Equal(t, "test.feature_only", attrErr.PlaneID)
	})

	t.Run("source feature on multi source succeeds", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.ContributeSource(s, planeMultiSource, feature.SourceFeature, "plugin-1", []string{"feat-val"})
		require.NoError(t, err)
		err = feature.ContributeSource(s, planeMultiSource, feature.SourceHost, "host-1", []string{"host-val"})
		require.NoError(t, err)

		frozen := s.Freeze()
		assert.Equal(t, []string{"feat-val", "host-val"}, feature.Get(frozen, planeMultiSource))
	})

	t.Run("standard planes traffic and usage observers support host source", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.ContributeSource(s, feature.PlaneTrafficObservers, feature.SourceHost, "host", []traffic.Observer{nil})
		require.NoError(t, err)
		err = feature.ContributeSource(s, feature.PlaneUsageObservers, feature.SourceHost, "host", []usage.Observer{nil})
		require.NoError(t, err)
	})

	t.Run("standard planes raw capture and traffic redactors reject host source", func(t *testing.T) {
		t.Parallel()
		s := feature.NewContributionSet()
		err := feature.ContributeSource(s, feature.PlaneRawCaptureSinks, feature.SourceHost, "host", []traffic.RawCaptureSink{nil})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)

		err = feature.ContributeSource(s, feature.PlaneTrafficRedactors, feature.SourceHost, "host", []traffic.Redactor{nil})
		require.Error(t, err)
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)
	})
}
