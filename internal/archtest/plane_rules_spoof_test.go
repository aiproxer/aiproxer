package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneRules_SpoofPackageBareGetRejected tests that a spoof sub-package under pkg/aipsdk/feature
// (such as pkg/aipsdk/feature/spoof) attempting to call bare Get is rejected because directory prefix
// matching has been replaced with exact directory matching (Finding 5).
func TestPlaneRules_SpoofPackageBareGetRejected(t *testing.T) {
	t.Parallel()

	spoofSrc := `package spoof

import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func SpoofRead(s aipfeature.FrozenPlaneSet) any {
	return aipfeature.Get(s, aipfeature.PlaneToolCallPolicies)
}
`
	findings := scanSyntheticSource(t, "pkg/aipsdk/feature/spoof/spoof.go", spoofSrc, Wave4_Tools)
	require.NotEmpty(t, findings, "spoof package under pkg/aipsdk/feature/ must NOT be allowed to call bare Get")
	assert.Equal(t, MirrorStageConsumer, findings[0].ShapeKind)
}

// TestPlaneRules_ForeignPackageRequestExecutionRejected tests that isRequestExecutionViewCall
// returns false when RequestExecution is invoked on a foreign package rather than aipsdk/feature (Finding 5).
func TestPlaneRules_ForeignPackageRequestExecutionRejected(t *testing.T) {
	t.Parallel()

	foreignSrc := `package extensions

import foreign "github.com/aiproxer/aiproxer/internal/foreignpkg"

func ForeignCaller(s foreign.SomeType) any {
	return foreign.RequestExecution(s).ToolCallPolicies()
}
`
	fset, f, err := ParseGoSource("internal/core/extensions/snapshot.go", []byte(foreignSrc))
	require.NoError(t, err)

	var callExpr *ast.CallExpr
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == "ForeignCaller" {
			if ret, ok := fd.Body.List[0].(*ast.ReturnStmt); ok {
				callExpr, _ = ret.Results[0].(*ast.CallExpr)
			}
		}
	}
	require.NotNil(t, callExpr, "callExpr must be found")
	assert.False(t, isRequestExecutionViewCall("internal/core/extensions/snapshot.go", callExpr, f), "foreign RequestExecution must not be recognized as aipsdk/feature call")
	_ = fset
}

// TestPlaneRules_UnauthorizedStageAccessorExecutionRejected tests that an unauthorized stage accessor
// attempting to call thin execution views without whitelist authorization is flagged.
func TestPlaneRules_UnauthorizedStageAccessorExecutionRejected(t *testing.T) {
	t.Parallel()

	unauthSrc := `package extensions

import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	planes aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) UnauthorizedPoliciesExecution() any {
	return aipfeature.RequestExecution(s.planes).ToolCallPolicies()
}
`
	findings := scanSyntheticSource(t, "internal/core/extensions/snapshot.go", unauthSrc, Wave4_Tools)
	require.NotEmpty(t, findings, "unauthorized stage accessor using RequestExecutionView must be rejected")
	assert.Equal(t, MirrorStageConsumer, findings[0].ShapeKind)
}

// TestForbiddenMirrorPredicate_ArbitraryStructWithPlaneFieldOutsideFeaturebundle verifies that
// an arbitrary struct (e.g. PlaneSnapshot) defined outside internal/featurebundle and without
// 'transport' in its name is rejected when carrying a known plane field past its wave (Requirement B).
func TestForbiddenMirrorPredicate_ArbitraryStructWithPlaneFieldOutsideFeaturebundle(t *testing.T) {
	t.Parallel()

	snapshotSrc := `package runtime
import "github.com/aiproxer/aiproxer/pkg/aipsdk/request"

type PlaneSnapshot struct {
	RequestTransforms []request.Transform
}
`
	findings := scanSyntheticSource(t, "internal/core/runtime/snapshot.go", snapshotSrc, Wave3_RequestShaping)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden transport field finding for arbitrary PlaneSnapshot struct outside featurebundle")
	}
	if findings[0].ShapeKind != MirrorNamedTransportField || findings[0].PlaneID != "request_transforms" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}

func TestForbiddenMirrorPredicate_SessionClassifierTransportFields(t *testing.T) {
	t.Parallel()

	src := `package runtimebundle
import "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"

type HandwrittenPlaneTransport struct {
	SessionClassifier   sessionclassification.Classifier
	sessionClassifier   sessionclassification.Classifier
	sessionClassifierID string
}
`
	findings := scanSyntheticSource(t, "internal/infra/runtimebundle/session_transport.go", src, Wave5c_Residual)
	if len(findings) != 3 {
		t.Fatalf("expected findings for all classifier mirror fields, got %+v", findings)
	}
	found := map[string]bool{
		"SessionClassifier":   false,
		"sessionClassifier":   false,
		"sessionClassifierID": false,
	}
	for _, finding := range findings {
		if finding.ShapeKind != MirrorNamedTransportField || finding.PlaneID != "session_classifier" {
			t.Fatalf("unexpected classifier transport finding: %+v", finding)
		}
		if _, ok := found[finding.Identifier]; !ok {
			t.Fatalf("unexpected classifier field identifier: %+v", finding)
		}
		found[finding.Identifier] = true
	}
	for identifier, seen := range found {
		if !seen {
			t.Errorf("scanner did not detect classifier mirror field %q", identifier)
		}
	}
}

func TestForbiddenMirrorPredicate_UnauthorizedSessionClassifierRead(t *testing.T) {
	t.Parallel()

	src := `package custom
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func UnauthorizedClassifierRead(f aipfeature.FrozenPlaneSet) any {
	return aipfeature.Get(f, aipfeature.PlaneSessionClassifier)
}
`
	findings := scanSyntheticSource(t, "internal/custom/classifier_read.go", src, Wave5c_Residual)
	if len(findings) == 0 {
		t.Fatal("expected unauthorized session classifier read to be rejected")
	}
	if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != "session_classifier" {
		t.Fatalf("unexpected unauthorized classifier read finding: %+v", findings[0])
	}
}

// TestForbiddenMirrorPredicate_SpoofedLegitimateStructInWrongPath verifies that spoofing a legitimate
// allowlisted struct name (e.g. HostContributions, ProductionOptions, Options) in an unauthorized package/path
// is rejected (Requirement B).
func TestForbiddenMirrorPredicate_SpoofedLegitimateStructInWrongPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		path       string
		structName string
		src        string
		planeID    string
	}{
		{
			name:       "spoofed HostContributions in unauthorized package",
			path:       "internal/spoof/spoof.go",
			structName: "HostContributions",
			src: `package spoof
import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type HostContributions struct {
	TrafficObservers []traffic.Observer
}
`,
			planeID: "traffic_observers",
		},
		{
			name:       "spoofed ProductionOptions in unauthorized package",
			path:       "internal/spoof/prod_opts.go",
			structName: "ProductionOptions",
			src: `package spoof
import "github.com/aiproxer/aiproxer/pkg/aipsdk/usage"

type ProductionOptions struct {
	UsageObservers []usage.Observer
}
`,
			planeID: "usage_observers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanSyntheticSource(t, tc.path, tc.src, Wave2_Observers)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden finding for spoofed %s at %s", tc.structName, tc.path)
			}
			if findings[0].ShapeKind != MirrorNamedTransportField || findings[0].PlaneID != tc.planeID {
				t.Fatalf("unexpected finding for spoofed %s: %+v", tc.structName, findings[0])
			}
		})
	}
}

// TestForbiddenMirrorPredicate_AllowlistedLegitimateStructsPass verifies that actual legitimate
// allowlisted structs at their exact qualified locations produce zero findings (Requirement B).
func TestForbiddenMirrorPredicate_AllowlistedLegitimateStructsPass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
		src  string
	}{
		{
			name: "HostContributions in internal/featurebundle",
			path: "internal/featurebundle/merge_generated.go",
			src: `package featurebundle
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type HostContributions struct {
	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
}
`,
		},
		{
			name: "ProductionOptions in internal/infra/runtimebundle",
			path: "internal/infra/runtimebundle/production_options.go",
			src: `package runtimebundle
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type ProductionOptions struct {
	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
}
`,
		},
		{
			name: "Options in pkg/aipruntime",
			path: "pkg/aipruntime/options.go",
			src: `package aipruntime
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type Options struct {
	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
}
`,
		},
		{
			name: "HostCapabilities in pkg/aipsdk/controlplane",
			path: "pkg/aipsdk/controlplane/host_capabilities.go",
			src: `package controlplane

type HostCapabilities struct {
	TrafficObservers bool
	UsageObservers   bool
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			findings := scanSyntheticSource(t, tc.path, tc.src, Wave5c_Residual)
			if len(findings) != 0 {
				t.Fatalf("expected 0 findings for legitimate allowlisted struct %s, got: %+v", tc.name, findings)
			}
		})
	}
}

// TestForbiddenMirrorPredicate_KnownPlaneFieldAddedToAllowlistedStructFails verifies that adding an
// unallowed known plane field to FeatureBundle or GeneratedMergeSurface fails (Requirement B).
func TestForbiddenMirrorPredicate_KnownPlaneFieldAddedToAllowlistedStructFails(t *testing.T) {
	t.Parallel()

	t.Run("unallowed plane field in FeatureBundle fails at Wave5c", func(t *testing.T) {
		t.Parallel()
		src := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type FeatureBundle struct {
	TrafficObservers []traffic.Observer
}
`
		findings := scanSyntheticSource(t, "pkg/aipsdk/feature/bundle.go", src, Wave5c_Residual)
		if len(findings) == 0 {
			t.Fatalf("expected forbidden finding when known plane field is added to FeatureBundle at Wave5c")
		}
	})

	t.Run("unallowed plane field in GeneratedMergeSurface fails", func(t *testing.T) {
		t.Parallel()
		src := `package featurebundle
import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type GeneratedMergeSurface struct {
	TrafficObservers []traffic.Observer
}
`
		findings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", src, Wave2_Observers)
		if len(findings) == 0 {
			t.Fatalf("expected forbidden finding when known plane field is added to GeneratedMergeSurface")
		}
	})
}

// TestIsGeneratedFile_CanonicalHeaderAdversarial verifies Requirement A:
// Generated files are recognized strictly by the canonical Go generated marker comment
// ('// Code generated ... DO NOT EDIT.') occurring before the package clause.
// Filename suffixes, block comments, post-package comments, malformed markers, and string literals
// must never grant generated exemption.
func TestIsGeneratedFile_CanonicalHeaderAdversarial(t *testing.T) {
	t.Parallel()

	t.Run("suffix_generated_no_header_rejected_and_scanned", func(t *testing.T) {
		t.Parallel()
		src := `package spoof

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/spoof_generated.go"
		assert.False(t, IsGeneratedFile(path, []byte(src), nil), "file with _generated.go suffix but no header must NOT be recognized as generated")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.NotEmpty(t, findings, "file with _generated.go suffix but no header must be scanned and rejected")
		assert.Equal(t, MirrorNamedTransportField, findings[0].ShapeKind)
	})

	t.Run("marker_string_literal_not_exempt", func(t *testing.T) {
		t.Parallel()
		src := `package spoof

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

const marker = "// Code generated by tool. DO NOT EDIT."

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/literal_generated.go"
		assert.False(t, IsGeneratedFile(path, []byte(src), nil), "string literal with marker text must NOT be recognized as generated header")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.NotEmpty(t, findings, "marker string literal must not grant exemption from scan")
		assert.Equal(t, MirrorNamedTransportField, findings[0].ShapeKind)
	})

	t.Run("marker_comment_after_package_not_exempt", func(t *testing.T) {
		t.Parallel()
		src := `package spoof

// Code generated by tool. DO NOT EDIT.

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/after_package.go"
		assert.False(t, IsGeneratedFile(path, []byte(src), nil), "marker comment after package clause must NOT be recognized as generated")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.NotEmpty(t, findings, "comment after package must not grant exemption from scan")
		assert.Equal(t, MirrorNamedTransportField, findings[0].ShapeKind)
	})

	t.Run("block_comment_before_package_not_exempt", func(t *testing.T) {
		t.Parallel()
		src := `/* Code generated by tool. DO NOT EDIT. */
package spoof

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/block_comment.go"
		assert.False(t, IsGeneratedFile(path, []byte(src), nil), "block comment must NOT be recognized as canonical line comment generated marker")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.NotEmpty(t, findings, "block comment marker must not grant exemption from scan")
		assert.Equal(t, MirrorNamedTransportField, findings[0].ShapeKind)
	})

	t.Run("missing_trailing_period_not_exempt", func(t *testing.T) {
		t.Parallel()
		src := `// Code generated by tool. DO NOT EDIT
package spoof

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/missing_period.go"
		assert.False(t, IsGeneratedFile(path, []byte(src), nil), "marker missing canonical trailing period must NOT be recognized as generated")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.NotEmpty(t, findings, "malformed marker missing period must not grant exemption from scan")
		assert.Equal(t, MirrorNamedTransportField, findings[0].ShapeKind)
	})

	t.Run("canonical_header_before_package_exempt", func(t *testing.T) {
		t.Parallel()
		src := `// Code generated by tool. DO NOT EDIT.

package spoof

import "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"

type SpoofStruct struct {
	TrafficObservers []traffic.Observer
}
`
		path := "pkg/aipsdk/feature/valid_header.go"
		assert.True(t, IsGeneratedFile(path, []byte(src), nil), "canonical line comment header before package must be recognized as generated")
		findings := scanSyntheticSource(t, path, src, Wave2_Observers)
		require.Empty(t, findings, "canonical header before package must be exempt from mirror findings")
	})

	t.Run("plane_generated_recognized_and_merge_generated_scanned", func(t *testing.T) {
		t.Parallel()
		root := repoRoot(t)

		// 1. plane_generated.go has canonical header and is recognized as generated
		planeGenPath := "pkg/aipsdk/feature/plane_generated.go"
		planeGenSrc, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(planeGenPath)))
		require.NoError(t, err)
		assert.True(t, IsGeneratedFile(planeGenPath, planeGenSrc, nil), "actual plane_generated.go must be recognized as generated")

		// 2. merge_generated.go is handwritten (no header) and is scanned (not exempt)
		mergeGenPath := "internal/featurebundle/merge_generated.go"
		mergeGenSrc, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(mergeGenPath)))
		require.NoError(t, err)
		assert.False(t, IsGeneratedFile(mergeGenPath, mergeGenSrc, nil), "handwritten merge_generated.go must NOT be recognized as generated")
	})
}
