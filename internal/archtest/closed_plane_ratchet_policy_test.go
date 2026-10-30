package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedPlaneArchitectureRatchets_PolicyBindingAdversarialCases(t *testing.T) {
	t.Parallel()

	t.Run("rejects mismatched PlaneX.generated binding", func(t *testing.T) {
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
	PlaneSubmitHooks.generated = canonicalPlaneRequestPartHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "mismatched binding: PlaneSubmitHooks.generated must be bound to canonicalPlaneSubmitHooksAccess") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report mismatched binding")
	})

	t.Run("rejects mismatched policy in canonical access binding", func(t *testing.T) {
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
		policy: canonicalPlaneRequestPartHooksPolicy,
	}
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "mismatched policy in canonicalPlaneSubmitHooksAccess") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report mismatched policy in access binding")
	})

	// --- Duplicate init adversarial tests ---

	t.Run("rejects duplicate canonical policy initialization in same init", func(t *testing.T) {
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
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess

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
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "duplicate canonical policy initialization for PlaneSubmitHooks") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report duplicate canonical policy initialization")
	})

	t.Run("rejects duplicate canonical policy initialization across multiple inits", func(t *testing.T) {
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
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}

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
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "duplicate canonical policy initialization for PlaneSubmitHooks") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report duplicate canonical policy across multiple inits")
	})

	t.Run("rejects duplicate canonical access binding in init", func(t *testing.T) {
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
			if strings.Contains(f.Detail, "duplicate canonical access binding for PlaneSubmitHooks") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report duplicate canonical access binding")
	})

	t.Run("rejects duplicate PlaneX.generated binding in init", func(t *testing.T) {
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
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "duplicate PlaneSubmitHooks.generated binding in init()") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report duplicate PlaneSubmitHooks.generated binding")
	})

	// --- Later reassignment adversarial tests ---

	t.Run("rejects reassignment of canonical policy later in init", func(t *testing.T) {
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
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess

	canonicalPlaneSubmitHooksPolicy = nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "forbidden reassignment of canonicalPlaneSubmitHooksPolicy") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report forbidden reassignment of canonicalPlaneSubmitHooksPolicy")
	})

	t.Run("rejects reassignment of canonical policy in runtime function", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func tamper() {
	canonicalPlaneSubmitHooksPolicy = nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "forbidden reassignment of canonicalPlaneSubmitHooksPolicy") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report forbidden reassignment in runtime function")
	})

	t.Run("rejects reassignment of canonical access in runtime function", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func tamper() {
	canonicalPlaneSubmitHooksAccess = generatedAccess[[]hooks.SubmitHook]{}
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "forbidden reassignment of canonicalPlaneSubmitHooksAccess") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report forbidden access reassignment in runtime function")
	})

	t.Run("rejects reassignment of PlaneX.generated in runtime function", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func tamper() {
	PlaneSubmitHooks.generated = canonicalPlaneSubmitHooksAccess
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		found := false
		for _, f := range findings {
			if strings.Contains(f.Detail, "forbidden reassignment of PlaneSubmitHooks.generated") {
				found = true
				break
			}
		}
		assert.True(t, found, "must report forbidden PlaneX.generated reassignment in runtime function")
	})
}
