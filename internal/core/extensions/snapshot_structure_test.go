package extensions_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestRuntimeSnapshot_NoRawPlaneFields ensures RequestRuntimeSnapshot contains
// only allowed host/config/service fields and no raw feature-plane fields.
func TestRequestRuntimeSnapshot_NoRawPlaneFields(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	snapshotGoPath := filepath.Join(filepath.Dir(thisFile), "snapshot.go")

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, snapshotGoPath, nil, 0)
	require.NoError(t, err, "failed to parse snapshot.go")

	var snapshotStruct *ast.StructType
	ast.Inspect(node, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "RequestRuntimeSnapshot" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if ok {
			snapshotStruct = st
			return false
		}
		return true
	})
	require.NotNil(t, snapshotStruct, "RequestRuntimeSnapshot struct not found in snapshot.go")

	allowedFields := []string{
		"hookBus",
		"state",
		"aux",
		"obs",
		"usageObs",
		"raw",
		"ws",
		"secretGuardPlane",
		"policyObserver",
		"timeoutBudget",
		"timeoutGuard",
		"featurePlanes",
		"gen",
	}

	var foundFields []string
	var forbiddenFields []string
	for _, field := range snapshotStruct.Fields.List {
		for _, name := range field.Names {
			foundFields = append(foundFields, name.Name)
			if !slices.Contains(allowedFields, name.Name) {
				forbiddenFields = append(forbiddenFields, name.Name)
			}
		}
	}

	assert.Empty(t, forbiddenFields, "RequestRuntimeSnapshot has raw plane fields that must be removed in task 9.1: %v (found all fields: %v)", forbiddenFields, foundFields)
}

// TestSnapshotOptions_NoRawPlaneFields verifies that SnapshotOptions contains only allowed
// non-plane fields and no raw feature-plane fields.
func TestSnapshotOptions_NoRawPlaneFields(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	snapshotGoPath := filepath.Join(filepath.Dir(thisFile), "snapshot.go")

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, snapshotGoPath, nil, 0)
	require.NoError(t, err, "failed to parse snapshot.go")

	var optionsStruct *ast.StructType
	ast.Inspect(node, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "SnapshotOptions" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if ok {
			optionsStruct = st
			return false
		}
		return true
	})
	require.NotNil(t, optionsStruct, "SnapshotOptions struct not found in snapshot.go")

	allowedOptionsFields := []string{
		"State",
		"Aux",
		"TrafficObserver",
		"UsageObserver",
		"RawCapture",
		"Workspace",
		"SecretGuardPlane",
		"PolicyObserver",
		"TimeoutBudgetSource",
		"FeaturePlanes",
		"Generation",
	}

	var foundFields []string
	var forbiddenFields []string
	for _, field := range optionsStruct.Fields.List {
		for _, name := range field.Names {
			foundFields = append(foundFields, name.Name)
			if !slices.Contains(allowedOptionsFields, name.Name) {
				forbiddenFields = append(forbiddenFields, name.Name)
			}
		}
	}

	assert.Empty(t, forbiddenFields, "SnapshotOptions has raw plane fields that must be removed in task 9.1: %v (found all fields: %v)", forbiddenFields, foundFields)
	assert.ElementsMatch(t, allowedOptionsFields, foundFields, "SnapshotOptions fields must exactly match the allowlist")
}

type stubStructSGGuard struct{ id string }

func (s stubStructSGGuard) ID() string                         { return s.id }
func (stubStructSGGuard) Order() int                           { return 0 }
func (stubStructSGGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (stubStructSGGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

// TestRequestRuntimeSnapshot_SecretGuardPlane_GuardsStoredNil proves that the internal stored
// secretGuardPlane.Guards field is nil on RequestRuntimeSnapshot, while both public and execution
// accessors return the materialized guards from featurePlanes.
func TestRequestRuntimeSnapshot_SecretGuardPlane_GuardsStoredNil(t *testing.T) {
	t.Parallel()
	g1 := stubStructSGGuard{id: "g1"}
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSecretGuards, "test", []secretguard.Guard{g1})
	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		SecretGuardPlane: extensions.SecretGuardPlane{
			Guards:        []secretguard.Guard{g1},
			ConfigVersion: "v1",
		},
		FeaturePlanes: cset.Freeze(),
	})

	val := reflect.ValueOf(snap).Elem().FieldByName("secretGuardPlane").FieldByName("Guards")
	require.True(t, val.IsNil(), "internal secretGuardPlane.Guards must be stored as nil")

	publicPlane := snap.SecretGuardPlane()
	require.Len(t, publicPlane.Guards, 1)
	assert.Equal(t, "g1", publicPlane.Guards[0].ID())
	assert.Equal(t, "v1", publicPlane.ConfigVersion)

	execPlane := snap.SecretGuardExecutionPlane()
	require.Len(t, execPlane.Guards, 1)
	assert.Equal(t, "g1", execPlane.Guards[0].ID())
	assert.Equal(t, "v1", execPlane.ConfigVersion)
}
