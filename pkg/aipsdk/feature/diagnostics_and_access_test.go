package feature_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type dummyHandler interface {
	HandlerID() string
}

type testDummyHandler struct {
	id string
}

func (h testDummyHandler) HandlerID() string {
	return h.id
}

type dummySubmitHook struct {
	id  string
	ord int
}

func (h dummySubmitHook) ID() string                     { return h.id }
func (h dummySubmitHook) Order() int                     { return h.ord }
func (h dummySubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h dummySubmitHook) Handle(ctx context.Context, call *aipapi.Call, meta *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type dummyTerminalProvider struct {
	id string
}

func (p dummyTerminalProvider) ID() string { return p.id }
func (p dummyTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

func TestPlaneDeclarationValidation_DiagnosticsCompleteness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		plane     feature.PlaneDeclaration
		wantError bool
		errTarget error
		errSubstr string
	}{
		{
			name: "StageID set without Materialize function is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_missing_materialize",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID: "stage.test",
					// Materialize is nil
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "diagnostics StageID is set but Materialize function is nil",
		},
		{
			name: "Materialize function provided without StageID is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_missing_stage_id",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID: "",
					Materialize: func(v []string) []feature.DiagnosticOccupant {
						return []feature.DiagnosticOccupant{{Label: "item"}}
					},
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "diagnostics StageID must not be empty when diagnostics metadata is provided",
		},
		{
			name: "Privileges provided without StageID is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_privileges_no_stage",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID: "",
					Privileges: func(v []string) feature.PrivilegeProjection {
						return feature.PrivilegeProjection{Flags: []string{"priv1"}}
					},
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "diagnostics StageID must not be empty when diagnostics metadata is provided",
		},
		{
			name: "CoalesceGroup provided without StageID is rejected",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_coalesce_no_stage",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID:       "",
					CoalesceGroup: "group.test",
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "diagnostics StageID must not be empty when diagnostics metadata is provided",
		},
		{
			name: "complete diagnostics descriptor passes validation",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_valid",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID:       feature.StageIDSubmit,
					CoalesceGroup: "submit_group",
					Order:         10,
					Materialize: func(v []string) []feature.DiagnosticOccupant {
						occupants := make([]feature.DiagnosticOccupant, 0, len(v))
						for _, item := range v {
							occupants = append(occupants, feature.DiagnosticOccupant{
								Label: "hook:" + item,
							})
						}
						return occupants
					},
					Privileges: func(v []string) feature.PrivilegeProjection {
						if len(v) > 0 {
							return feature.PrivilegeProjection{Flags: []string{"auxiliary_requests"}}
						}
						return feature.PrivilegeProjection{}
					},
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: false,
		},
		{
			name: "zero order with stage ID fails validation",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_zero_order",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID: feature.StageIDSubmit,
					Order:   0,
					Materialize: func(v []string) []feature.DiagnosticOccupant {
						return nil
					},
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "Order must be > 0",
		},
		{
			name: "negative order with stage ID fails validation",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_neg_order",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					StageID: feature.StageIDSubmit,
					Order:   -5,
					Materialize: func(v []string) []feature.DiagnosticOccupant {
						return nil
					},
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "Order must be > 0",
		},
		{
			name: "positive order without stage ID fails validation",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_order_no_stage",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Diagnostics: feature.DiagnosticDescriptor[[]string]{
					Order: 10,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: true,
			errTarget: feature.ErrInvalidPlane,
			errSubstr: "diagnostics StageID must not be empty when diagnostics metadata is provided",
		},
		{
			name: "omitted diagnostics descriptor passes validation",
			plane: feature.Plane[[]string]{
				ID:           "test.diag_omitted",
				Multiplicity: feature.MultOrdered,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			},
			wantError: false,
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

func TestPlaneDeclarationValidation_NilPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		policy    feature.NilPolicy
		wantError bool
	}{
		{name: "NilNotApplicable is valid", policy: feature.NilNotApplicable, wantError: false},
		{name: "NilReject is valid", policy: feature.NilReject, wantError: false},
		{name: "NilSkip is valid", policy: feature.NilSkip, wantError: false},
		{name: "invalid nil policy is rejected", policy: feature.NilPolicy(99), wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plane := feature.Plane[[]string]{
				ID:           "test.nil_policy",
				Multiplicity: feature.MultOrdered,
				NilPolicy:    tc.policy,
				Rules: feature.SourceRules{
					Feature: feature.CombConcatenate,
				},
				Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
					return append(cur, inc...), nil
				},
			}
			err := plane.ValidateDeclaration()
			if tc.wantError {
				require.Error(t, err)
				require.ErrorIs(t, err, feature.ErrInvalidPlane)
				assert.Contains(t, err.Error(), "invalid nil policy")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNilPolicy_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "not_applicable", feature.NilNotApplicable.String())
	assert.Equal(t, "reject", feature.NilReject.String())
	assert.Equal(t, "skip", feature.NilSkip.String())
	assert.Equal(t, "NilPolicy(99)", feature.NilPolicy(99).String())
}

func TestContribute_NilPolicy_Reject(t *testing.T) {
	t.Parallel()

	rejectPlane := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.reject_nil_interface",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilReject,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s := feature.NewContributionSet()

	// Contributing untyped nil interface is rejected by NilReject policy
	var nilHandler dummyHandler = nil
	err := feature.Contribute(s, rejectPlane, "plugin-1", nilHandler)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrNilContribution)

	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-1", attrErr.PluginID)
	assert.Equal(t, "test.reject_nil_interface", attrErr.PlaneID)
	assert.Contains(t, err.Error(), "nil contribution rejected by policy")

	// Fail-before-mutate: set is unmodified
	assert.False(t, s.Has("test.reject_nil_interface"))

	// Contributing a typed nil using IsNil detector is also rejected
	rejectPlaneWithIsNil := feature.BindGeneratedTestPlane(feature.Plane[*testDummyHandler]{
		ID:           "test.reject_typed_nil",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilReject,
		IsNil: func(v *testDummyHandler) bool {
			return v == nil
		},
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc *testDummyHandler) (*testDummyHandler, error) {
			return inc, nil
		},
	})

	var typedNil *testDummyHandler = nil
	err = feature.Contribute(s, rejectPlaneWithIsNil, "plugin-2", typedNil)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrNilContribution)
	assert.False(t, s.Has("test.reject_typed_nil"))
}

func TestContribute_NilPolicy_Skip(t *testing.T) {
	t.Parallel()

	skipPlane := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.skip_nil_interface",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilSkip,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s := feature.NewContributionSet()

	// Initial valid contribution
	var validHandler dummyHandler = testDummyHandler{id: "handler-1"}
	err := feature.Contribute(s, skipPlane, "plugin-1", validHandler)
	require.NoError(t, err)
	assert.True(t, s.Has("test.skip_nil_interface"))

	// Contributing nil with NilSkip succeeds silently and does not modify the set
	var nilHandler dummyHandler = nil
	err = feature.Contribute(s, skipPlane, "plugin-2", nilHandler)
	require.NoError(t, err)

	// Frozen set still retains handler-1 only
	frozen := s.Freeze()
	got := feature.Get(frozen, skipPlane)
	require.NotNil(t, got)
	assert.Equal(t, "handler-1", got.HandlerID())
}

func TestContribute_NilPolicy_AppliedBeforeValidation(t *testing.T) {
	t.Parallel()

	validateCalled := false
	skipPlane := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.nil_before_validate",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilSkip,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Validate: func(v dummyHandler) error {
			validateCalled = true
			if v == nil {
				return errors.New("validate saw nil")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s := feature.NewContributionSet()
	var nilHandler dummyHandler = nil

	// Contributing nil with NilSkip should NOT trigger Validate
	err := feature.Contribute(s, skipPlane, "plugin-1", nilHandler)
	require.NoError(t, err)
	assert.False(t, validateCalled, "Validate should not be called when nil is skipped by NilPolicy")
}

func TestDiagnosticsDescriptor_MaterializeAndPrivileges(t *testing.T) {
	t.Parallel()

	plane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.diag_exec",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Diagnostics: feature.DiagnosticDescriptor[[]string]{
			StageID:       feature.StageIDSubmit,
			CoalesceGroup: "group.hooks",
			Order:         10,
			Materialize: func(v []string) []feature.DiagnosticOccupant {
				occupants := make([]feature.DiagnosticOccupant, 0, len(v))
				for _, s := range v {
					occupants = append(occupants, feature.DiagnosticOccupant{
						Label:    "submit_hook:" + s,
						PluginID: "plugin-test",
					})
				}
				return occupants
			},
			Privileges: func(v []string) feature.PrivilegeProjection {
				if len(v) > 0 {
					return feature.PrivilegeProjection{Flags: []string{"raw_capture", "auxiliary_requests"}}
				}
				return feature.PrivilegeProjection{}
			},
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	})

	s := feature.NewContributionSet()
	err := feature.Contribute(s, plane, "plugin-test", []string{"hook-alpha", "hook-beta"})
	require.NoError(t, err)

	frozen := s.Freeze()
	val := feature.Get(frozen, plane)

	// Verify diagnostics materialization observes the retained frozen value
	occupants := plane.MaterializeOccupants(val)
	require.Len(t, occupants, 2)
	assert.Equal(t, "submit_hook:hook-alpha", occupants[0].Label)
	assert.Equal(t, "submit_hook:hook-beta", occupants[1].Label)

	privs := plane.ProjectPrivileges(val)
	assert.Equal(t, []string{"raw_capture", "auxiliary_requests"}, privs.Flags)
}

func TestContribute_NilPolicy_InterfaceTypedNil(t *testing.T) {
	t.Parallel()

	// Case 1: Plane without IsNil callback cannot detect typed-nil pointer inside interface value
	planeWithoutIsNil := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.nil_no_callback",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilReject,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Validate: func(v dummyHandler) error {
			if v == nil {
				return errors.New("validate received nil")
			}
			// When typed-nil pointer is boxed, calling HandlerID or type checking would panic or fail
			if th, ok := v.(*testDummyHandler); ok && th == nil {
				return errors.New("validate caught boxed typed nil")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s := feature.NewContributionSet()

	// Untyped nil interface is caught by untyped anyVal == nil fast path
	var untypedNil dummyHandler = nil
	err := feature.Contribute(s, planeWithoutIsNil, "plugin-1", untypedNil)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrNilContribution)

	// Typed nil (*testDummyHandler)(nil) inside interface is NOT caught by anyVal == nil fast path,
	// so it bypasses NilPolicy and reaches Validate
	var typedNil dummyHandler = (*testDummyHandler)(nil)
	err = feature.Contribute(s, planeWithoutIsNil, "plugin-2", typedNil)
	require.Error(t, err)
	// Error came from Validate, NOT from NilPolicy ErrNilContribution
	assert.False(t, errors.Is(err, feature.ErrNilContribution))
	assert.Contains(t, err.Error(), "validate caught boxed typed nil")

	// Case 2: Plane with IsNil callback properly detects boxed typed-nil interface values
	planeWithIsNil := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.nil_with_callback",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilReject,
		IsNil: func(v dummyHandler) bool {
			if v == nil {
				return true
			}
			if th, ok := v.(*testDummyHandler); ok && th == nil {
				return true
			}
			return false
		},
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Validate: func(v dummyHandler) error {
			return errors.New("validate should not be called")
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s2 := feature.NewContributionSet()
	err = feature.Contribute(s2, planeWithIsNil, "plugin-3", typedNil)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrNilContribution)
	assert.Contains(t, err.Error(), "nil contribution rejected by policy")
	assert.False(t, s2.Has("test.nil_with_callback"))

	// Case 3: Plane with IsNil and NilSkip skips typed-nil silently
	planeWithSkip := feature.BindGeneratedTestPlane(feature.Plane[dummyHandler]{
		ID:           "test.nil_skip_with_callback",
		Multiplicity: feature.MultOrdered,
		NilPolicy:    feature.NilSkip,
		IsNil: func(v dummyHandler) bool {
			if v == nil {
				return true
			}
			if th, ok := v.(*testDummyHandler); ok && th == nil {
				return true
			}
			return false
		},
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc dummyHandler) (dummyHandler, error) {
			return inc, nil
		},
	})

	s3 := feature.NewContributionSet()
	err = feature.Contribute(s3, planeWithSkip, "plugin-4", typedNil)
	require.NoError(t, err)
	assert.False(t, s3.Has("test.nil_skip_with_callback"))
}

func TestPlaneDeclarationValidation_GeneratedIdentityRequiredWhenBound(t *testing.T) {
	t.Parallel()

	// Exclusive plane with generated access bound but missing generated.identity must fail validation
	exclusivePlaneMissingGenID := feature.Plane[testDummyHandler]{
		ID:           "test.bound.missing_identity",
		Multiplicity: feature.MultExclusive,
		Rules: feature.SourceRules{
			Feature: feature.CombExclusive,
		},
		Identity: func(v testDummyHandler) (string, bool) {
			return v.id, true
		},
		ValidateIdentity: func(id string) error {
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc testDummyHandler) (testDummyHandler, error) {
			return inc, nil
		},
	}

	exclusivePlaneMissingGenID = feature.BindGeneratedAccessForTest(
		exclusivePlaneMissingGenID,
		func(gc *feature.GeneratedContributionsForTest, source feature.SourceKind, pluginID string, v testDummyHandler) error {
			return nil
		},
		func(gf *feature.GeneratedFrozenForTest) testDummyHandler {
			return testDummyHandler{}
		},
		nil, // missing generated identity accessor!
	)

	err := exclusivePlaneMissingGenID.ValidateDeclaration()
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidPlane)
	assert.Contains(t, err.Error(), "generated identity accessor required")

	// When generated identity is provided, validation passes
	exclusivePlaneWithGenID := feature.BindGeneratedAccessForTest(
		exclusivePlaneMissingGenID,
		func(gc *feature.GeneratedContributionsForTest, source feature.SourceKind, pluginID string, v testDummyHandler) error {
			return nil
		},
		func(gf *feature.GeneratedFrozenForTest) testDummyHandler {
			return testDummyHandler{}
		},
		func(gf *feature.GeneratedFrozenForTest) (string, bool) {
			return "test-id", true
		},
	)
	err = exclusivePlaneWithGenID.ValidateDeclaration()
	require.NoError(t, err)
}

func TestGenericAccess_GeneratedBypassesMapStorage(t *testing.T) {
	t.Parallel()

	cset := feature.NewContributionSet()

	// Contribute to PlaneSubmitHooks (slice plane), PlaneTerminalDecisionProvider (exclusive plane),
	// and PlaneToolCallFinalizationMaxArgsBytes (scalar plane) using real production generated fields.
	err := feature.Contribute(cset, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		dummySubmitHook{id: "hook-1", ord: 1},
	})
	require.NoError(t, err)

	err = feature.Contribute(cset, feature.PlaneTerminalDecisionProvider, "plugin-alpha", terminaldecision.Provider(dummyTerminalProvider{
		id: "provider-alpha",
	}))
	require.NoError(t, err)

	err = feature.Contribute(cset, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-args", 1024)
	require.NoError(t, err)

	frozen := cset.Freeze()

	// Direct typed Get and FrozenIdentity resolve through generated storage with zero runtime map discovery
	submitHooks := feature.Get(frozen, feature.PlaneSubmitHooks)
	require.Len(t, submitHooks, 1)
	assert.Equal(t, "hook-1", submitHooks[0].ID())

	termProvider := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, termProvider)
	assert.Equal(t, "provider-alpha", termProvider.ID())

	termID, ok := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "provider-alpha", termID)

	maxArgs := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 1024, maxArgs)
}

func TestGenericAccess_FreezingImmutability(t *testing.T) {
	t.Parallel()

	cset := feature.NewContributionSet()

	// Contribute to PlaneSubmitHooks
	err := feature.Contribute(cset, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		dummySubmitHook{id: "h1"},
		dummySubmitHook{id: "h2"},
	})
	require.NoError(t, err)

	// Contribute to PlaneTerminalDecisionProvider
	err = feature.Contribute(cset, feature.PlaneTerminalDecisionProvider, "plugin-alpha", terminaldecision.Provider(dummyTerminalProvider{id: "provider-alpha"}))
	require.NoError(t, err)

	// Freeze first snapshot
	frozen1 := cset.Freeze()

	// Verify frozen1 state
	assert.Len(t, feature.Get(frozen1, feature.PlaneSubmitHooks), 2)
	assert.Equal(t, "provider-alpha", feature.Get(frozen1, feature.PlaneTerminalDecisionProvider).ID())
	id1, ok1 := feature.FrozenIdentity(frozen1, feature.PlaneTerminalDecisionProvider)
	assert.True(t, ok1)
	assert.Equal(t, "provider-alpha", id1)

	// Mutate cset via another contribution to PlaneSubmitHooks
	err = feature.Contribute(cset, feature.PlaneSubmitHooks, "plugin-2", []hooks.SubmitHook{
		dummySubmitHook{id: "h3"},
	})
	require.NoError(t, err)

	// Second contribution to exclusive plane fails with conflict error in Contribute (before closure)
	err = feature.Contribute(cset, feature.PlaneTerminalDecisionProvider, "plugin-beta", terminaldecision.Provider(dummyTerminalProvider{id: "provider-beta"}))
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.Contains(t, err.Error(), `"provider-alpha" and "provider-beta"`)

	// Freeze second snapshot
	frozen2 := cset.Freeze()

	// PROVE IMMUTABILITY: frozen1 is completely unmodified by subsequent contributions to cset!
	assert.Len(t, feature.Get(frozen1, feature.PlaneSubmitHooks), 2)
	assert.Equal(t, "h1", feature.Get(frozen1, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, "h2", feature.Get(frozen1, feature.PlaneSubmitHooks)[1].ID())
	assert.Equal(t, "provider-alpha", feature.Get(frozen1, feature.PlaneTerminalDecisionProvider).ID())

	// frozen2 reflects the new hooks contribution
	assert.Len(t, feature.Get(frozen2, feature.PlaneSubmitHooks), 3)
	assert.Equal(t, "h1", feature.Get(frozen2, feature.PlaneSubmitHooks)[0].ID())
	assert.Equal(t, "h2", feature.Get(frozen2, feature.PlaneSubmitHooks)[1].ID())
	assert.Equal(t, "h3", feature.Get(frozen2, feature.PlaneSubmitHooks)[2].ID())
	assert.Equal(t, "provider-alpha", feature.Get(frozen2, feature.PlaneTerminalDecisionProvider).ID())
}

type dummyLTHandler struct {
	id  string
	ord int
}

func (h dummyLTHandler) ID() string                   { return h.id }
func (h dummyLTHandler) Order() int                   { return h.ord }
func (dummyLTHandler) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (dummyLTHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (dummyLTHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

func TestPlaneLocalTurnHandlers_DiagnosticsDescriptor(t *testing.T) {
	t.Parallel()

	assert.Equal(t, feature.StageIDPreRequest, feature.PlaneLocalTurnHandlers.Diagnostics.StageID)
	assert.Equal(t, "", feature.PlaneLocalTurnHandlers.Diagnostics.CoalesceGroup)

	var typedNil *dummyLTHandler
	handlers := []localturn.Handler{
		dummyLTHandler{id: "lt-z", ord: 20},
		nil,
		dummyLTHandler{id: "lt-a", ord: 10},
		typedNil,
		dummyLTHandler{id: "lt-m", ord: 10},
	}

	require.NotNil(t, feature.PlaneLocalTurnHandlers.Diagnostics.Materialize, "Materialize function must not be nil")
	occupants := feature.PlaneLocalTurnHandlers.Diagnostics.Materialize(handlers)
	require.Len(t, occupants, 3, "Materialize must filter untyped nil and boxed typed-nil")
	assert.Equal(t, "local_turn:lt-a", occupants[0].Label)
	assert.Equal(t, "local_turn:lt-m", occupants[1].Label)
	assert.Equal(t, "local_turn:lt-z", occupants[2].Label)

	// Deterministic JSON byte comparison
	jsonBytes, err := json.Marshal(occupants)
	require.NoError(t, err)
	expectedJSON := `[{"Label":"local_turn:lt-a","PluginID":"","Privileges":null},{"Label":"local_turn:lt-m","PluginID":"","Privileges":null},{"Label":"local_turn:lt-z","PluginID":"","Privileges":null}]`
	assert.Equal(t, expectedJSON, string(jsonBytes))

	// Empty / all nil input returns nil or empty occupants
	assert.Empty(t, feature.PlaneLocalTurnHandlers.Diagnostics.Materialize(nil))
	assert.Empty(t, feature.PlaneLocalTurnHandlers.Diagnostics.Materialize([]localturn.Handler{nil, typedNil}))
}

func TestPrivilegeConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "raw_capture", feature.PrivilegeRawCapture)
	assert.Equal(t, "auxiliary_requests", feature.PrivilegeAuxiliaryRequests)
	assert.Equal(t, "auth_provider", feature.PrivilegeAuthProvider)
	assert.Equal(t, "completion_gate", feature.PrivilegeCompletionGate)
}
