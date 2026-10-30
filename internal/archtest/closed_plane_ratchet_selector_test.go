package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedPlaneArchitectureRatchets_SelectorAdversarialCases(t *testing.T) {
	t.Parallel()

	t.Run("rejects PlaneX alias assignment in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	alias := PlaneSubmitHooks
	_ = alias
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "forbidden alias assignment of PlaneSubmitHooks")
	})

	t.Run("rejects PlaneX var declaration in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	var alias = PlaneSubmitHooks
	_ = alias
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "forbidden alias declaration of PlaneSubmitHooks")
	})

	t.Run("rejects PlaneX passing to function in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func register(p any) {}

func init() {
	register(PlaneSubmitHooks)
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "forbidden passing of PlaneSubmitHooks to register")
	})

	t.Run("rejects PlaneX policy selector assignment to variable in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	rules := PlaneSubmitHooks.Rules
	_ = rules
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "PlaneSubmitHooks.Rules")
	})

	t.Run("rejects PlaneX policy selector passed to function in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func checkRules(r SourceRules) {}

func init() {
	checkRules(PlaneSubmitHooks.Rules)
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "PlaneSubmitHooks.Rules")
	})

	t.Run("rejects PlaneX alias selector usage in init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	alias := PlaneSubmitHooks
	_ = alias.Rules
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		foundAlias := false
		foundSelector := false
		for _, f := range findings {
			if f.Rule == RuleClosedPlaneNoGlobalPlaneSelectors {
				if strings.Contains(f.Detail, "forbidden alias assignment") {
					foundAlias = true
				}
				if strings.Contains(f.Detail, "alias.Rules") {
					foundSelector = true
				}
			}
		}
		assert.True(t, foundAlias, "must detect alias assignment")
		assert.True(t, foundSelector, "must detect selector on alias")
	})

	t.Run("rejects PlaneX alias in runtime function", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func helper() {
	alias := PlaneSubmitHooks
	_ = alias
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "forbidden alias assignment of PlaneSubmitHooks")
	})

	t.Run("allows PlaneX selectors and declarations in non-generated file without false positives", func(t *testing.T) {
		t.Parallel()
		src := `package feature

var PlaneCustom = Plane[string]{
	ID: "custom",
}

func GetCustomID() string {
	return PlaneCustom.ID
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/custom_plane.go", src)
		assert.Empty(t, findings, "non-generated files must not trigger global plane selector ratchet")
	})

	t.Run("allows valid canonical policy initialization with arbitrary parentheses", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	(((canonicalPlaneSubmitHooksPolicy))) = (&generatedPolicy[[]hooks.SubmitHook]{
		planeID:                (((PlaneSubmitHooks))).ID,
		rules:                  ((PlaneSubmitHooks)).Rules,
		nilPolicy:              (PlaneSubmitHooks).NilPolicy,
		isNil:                  (PlaneSubmitHooks).IsNil,
		validate:               (PlaneSubmitHooks).Validate,
		validateIdentity:       (PlaneSubmitHooks).ValidateIdentity,
		combine:                (PlaneSubmitHooks).Combine,
		identity:               (PlaneSubmitHooks).Identity,
		exclusiveConflictError: (PlaneSubmitHooks).ExclusiveConflictError,
		requestMaterializer:    (PlaneSubmitHooks).RequestMaterializer,
		requestBorrow:          (PlaneSubmitHooks).RequestBorrow,
		hookTarget:             (PlaneSubmitHooks).HookTarget,
		requestAccess:          (PlaneSubmitHooks).RequestAccess,
		diagStageID:            ((PlaneSubmitHooks).Diagnostics).StageID,
		diagCoalesceGroup:      (PlaneSubmitHooks).Diagnostics.CoalesceGroup,
		diagOrder:              (PlaneSubmitHooks).Diagnostics.Order,
		diagMaterialize:        (PlaneSubmitHooks).Diagnostics.Materialize,
		diagPrivileges:         (PlaneSubmitHooks).Diagnostics.Privileges,
	})
	(((canonicalPlaneSubmitHooksAccess))) = (generatedAccess[[]hooks.SubmitHook]{
		policy: (canonicalPlaneSubmitHooksPolicy),
	})
	((PlaneSubmitHooks)).generated = (canonicalPlaneSubmitHooksAccess)
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		assert.Empty(t, findings, "parentheses in valid canonical policy init must be permitted")
	})

	t.Run("rejects forbidden PlaneX selector wrapped in parentheses outside init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func helper() {
	rules := (((PlaneSubmitHooks))).Rules
	_ = rules
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "PlaneSubmitHooks.Rules")
	})

	t.Run("rejects forbidden PlaneX alias wrapped in parentheses", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	alias := (((PlaneSubmitHooks)))
	_ = ((alias)).Rules
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
	})

	t.Run("rejects cross-plane direct field capture in canonical policy init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		planeID: PlaneRequestPartHooks.ID,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "cross-plane capture")
	})

	t.Run("rejects cross-plane diagnostics subfield capture in canonical policy init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		diagOrder: PlaneRequestPartHooks.Diagnostics.Order,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "cross-plane capture")
	})

	t.Run("rejects mismatched direct source field in canonical policy init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		planeID: PlaneSubmitHooks.Rules,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "mismatched source field")
	})

	t.Run("rejects mismatched diagnostics subfield in canonical policy init", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		diagOrder: PlaneSubmitHooks.Diagnostics.StageID,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "mismatched source field")
	})

	t.Run("rejects non-diagnostics selector on diagnostics destination field", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		diagStageID: PlaneSubmitHooks.ID,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "mismatched source field")
	})

	// --- Omission adversarial tests ---

	t.Run("rejects omission of expected fields in canonical policy composite literal", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		planeID: PlaneSubmitHooks.ID,
		rules:   PlaneSubmitHooks.Rules,
	}
	canonicalPlaneSubmitHooksAccess = generatedAccess[[]hooks.SubmitHook]{
		policy: canonicalPlaneSubmitHooksPolicy,
	}
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoGlobalPlaneSelectors, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "incomplete canonical policy initialization")
		assert.Contains(t, findings[0].Detail, "missing expected field")
	})

	t.Run("rejects omission of canonical policy initialization statement", func(t *testing.T) {
		t.Parallel()
		src := `package feature

var (
	canonicalPlaneSubmitHooksPolicy *generatedPolicy[[]hooks.SubmitHook]
	canonicalPlaneSubmitHooksAccess generatedAccess[[]hooks.SubmitHook]
)

func init() {
	canonicalPlaneSubmitHooksAccess = generatedAccess[[]hooks.SubmitHook]{
		policy: canonicalPlaneSubmitHooksPolicy,
	}
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "missing canonical policy initialization for PlaneSubmitHooks") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report missing canonical policy initialization")
	})

	// --- Missing binding adversarial tests ---

	t.Run("rejects omission of canonical access binding", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		planeID:                PlaneSubmitHooks.ID,
		rules:                  PlaneSubmitHooks.Rules,
		nilPolicy:              PlaneSubmitHooks.NilPolicy,
		isNil:                  PlaneSubmitHooks.IsNil,
		validate:               PlaneSubmitHooks.Validate,
		validateIdentity:       PlaneSubmitHooks.ValidateIdentity,
		combine:                PlaneSubmitHooks.Combine,
		identity:               PlaneSubmitHooks.Identity,
		exclusiveConflictError: PlaneSubmitHooks.ExclusiveConflictError,
		requestMaterializer:    PlaneSubmitHooks.RequestMaterializer,
		requestBorrow:          PlaneSubmitHooks.RequestBorrow,
		hookTarget:             PlaneSubmitHooks.HookTarget,
		diagStageID:            PlaneSubmitHooks.Diagnostics.StageID,
		diagCoalesceGroup:      PlaneSubmitHooks.Diagnostics.CoalesceGroup,
		diagOrder:              PlaneSubmitHooks.Diagnostics.Order,
		diagMaterialize:        PlaneSubmitHooks.Diagnostics.Materialize,
		diagPrivileges:         PlaneSubmitHooks.Diagnostics.Privileges,
	}
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "missing canonical access binding for PlaneSubmitHooks") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report missing canonical access binding")
	})

	t.Run("rejects omission of PlaneX.generated binding", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func init() {
	canonicalPlaneSubmitHooksPolicy = &generatedPolicy[[]hooks.SubmitHook]{
		planeID:                PlaneSubmitHooks.ID,
		rules:                  PlaneSubmitHooks.Rules,
		nilPolicy:              PlaneSubmitHooks.NilPolicy,
		isNil:                  PlaneSubmitHooks.IsNil,
		validate:               PlaneSubmitHooks.Validate,
		validateIdentity:       PlaneSubmitHooks.ValidateIdentity,
		combine:                PlaneSubmitHooks.Combine,
		identity:               PlaneSubmitHooks.Identity,
		exclusiveConflictError: PlaneSubmitHooks.ExclusiveConflictError,
		requestMaterializer:    PlaneSubmitHooks.RequestMaterializer,
		requestBorrow:          PlaneSubmitHooks.RequestBorrow,
		hookTarget:             PlaneSubmitHooks.HookTarget,
		diagStageID:            PlaneSubmitHooks.Diagnostics.StageID,
		diagCoalesceGroup:      PlaneSubmitHooks.Diagnostics.CoalesceGroup,
		diagOrder:              PlaneSubmitHooks.Diagnostics.Order,
		diagMaterialize:        PlaneSubmitHooks.Diagnostics.Materialize,
		diagPrivileges:         PlaneSubmitHooks.Diagnostics.Privileges,
	}
	canonicalPlaneSubmitHooksAccess = generatedAccess[[]hooks.SubmitHook]{
		policy: canonicalPlaneSubmitHooksPolicy,
	}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "missing PlaneSubmitHooks.generated binding") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report missing PlaneSubmitHooks.generated binding")
	})
}
