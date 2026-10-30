package feature_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackageDoc_RequiredContractAnchors(t *testing.T) {
	t.Parallel()

	// Read and parse doc.go to extract the package comment
	docPath := "doc.go"
	if _, err := os.Stat(docPath); os.IsNotExist(err) {
		// Fallback for when running from repository root or another working directory
		docPath = filepath.Join("pkg", "aipsdk", "feature", "doc.go")
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, docPath, nil, parser.ParseComments)
	require.NoError(t, err, "failed to parse doc.go")

	var docText string
	if node.Doc != nil {
		docText = node.Doc.Text()
	}

	normalized := strings.Join(strings.Fields(docText), " ")

	requiredAnchors := []string{
		// Public construction, read, and lifecycle
		"Construct a mutable set using [NewContributionSet]",
		"[Contribute] tags contributions with [SourceFeature]",
		"[ContributionSet.Freeze] to produce an immutable [FrozenPlaneSet]",
		"Wrap the frozen planes and any optional plugin lifecycles into a [FeatureBundle] by calling [BundleFromPlanes]",
		"[BundleFromPlanes] assigns [SchemaVersionV1]",
		"[FrozenPlaneSet] using the generic package-level function [Get]",

		// Declaration contract
		"ID: A globally unique, non-empty, stable string identifier",
		"Multiplicity: Must be [MultOrdered]",
		"[MultExclusive]",
		"Rules ([SourceRules]): Explicit per-source combination rules for [SourceFeature], [SourceHost], and [SourceGenerationBinder]",
		"NilPolicy: Defines handling of nil contributions",
		"explicit IsNil predicate must be provided to detect typed-nil pointers boxed in interfaces without runtime reflection",
		"Validate: Optional validator function for incoming contribution values",
		"Combine: Folding function that combines incoming values",
		"Identity & ValidateIdentity: Required for exclusive planes and replace-by-identity sources",
		"ExclusiveConflictError: Optional compatibility error; valid only when at least one source rule is [CombExclusive]; conflict still preserves generic exclusive conflict classification",
		"Diagnostics ([DiagnosticDescriptor]): Configures operator inventory and privilege projection",
		"StageID: Identifies a legal lifecycle stage",
		"Materialize: Creates diagnostic occupants",
		"Privileges: Projects privilege flags",
		"CoalesceGroup: Groups compatible stage occupancy",
		"Order: Gives deterministic ordering",
		"When a StageID is set, Materialize must be non-nil, Order must be > 0",
		"If StageID is absent, descriptor metadata must be empty",
		"RequestMaterializer & RequestBorrow: An optional sorting/materialization transform",
		"[Plane.ValidateDeclaration] and [ValidateManifest] enforce these rules",

		// Generated policy and exclusivity
		"pkg/aipsdk/feature/plane_manifest.go is the canonical hand-authored catalog",
		"pkg/aipsdk/feature/plane_generated.go is checked in and must not be edited manually",
		"eliminate runtime reflection, unsafe type conversions, and request-path key lookup",

		// Registration policy
		"The feature implementation package lives under internal/plugins/features/<feature>",
		"Standard distribution wiring in internal/standardplugins/features_install.go provides only explicit registration and adaptation",
		"The sole registration table edit is adding exactly one FeatureRegistration row to internal/standardplugins/standard_table.go in StandardBundle().Features",
		"Do not add feature-specific branches or types to core/runtime or any other registry",
		"Optional executable backend connectors use an independent gRPC manifest discovery mechanism and are out of this feature-registration path",

		// Closed manifest, policy authority, and standard boundary
		"Note that [FeatureBundle] contains no per-plane named fields or slices",
		"No dynamic planes in v1: Arbitrary unbound or dynamically declared planes are not supported",
		"Contributing through an ungenerated or unbound plane fails immediately before candidate mutation with [ErrUngeneratedPlane]",
		"Canonical generated-policy authority: Exported [Plane] descriptors (such as [PlaneSubmitHooks]) act as typed descriptors and identifiers",
		"Copying or mutating exported fields on a [Plane] descriptor (e.g. copying PlaneX and changing its rules or combiner) does not redefine the plane",
		"Adding a new extension plane requires an upstream manifest and platform change",
		"Feature plugins own their configuration decoding and bundle construction via a feature-owned constructor or factory",
		"neither internal/core nor internal/infra/runtimebundle imports concrete feature packages",
	}

	for _, anchor := range requiredAnchors {
		t.Run(anchor, func(t *testing.T) {
			t.Parallel()
			assert.Truef(t, strings.Contains(normalized, anchor),
				"package documentation in %s missing required contract anchor %q", docPath, anchor)
		})
	}

	// Distinct command line checks to prevent substring false proof
	lines := strings.Split(docText, "\n")
	var hasGenerateCmd, hasCheckCmd bool
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "go run ./scripts/generate-feature-planes.go" {
			hasGenerateCmd = true
		}
		if trimmed == "go run ./scripts/generate-feature-planes.go -check" {
			hasCheckCmd = true
		}
	}

	t.Run("go run ./scripts/generate-feature-planes.go", func(t *testing.T) {
		t.Parallel()
		assert.True(t, hasGenerateCmd, "package documentation missing distinct generate command line")
	})
	t.Run("go run ./scripts/generate-feature-planes.go -check", func(t *testing.T) {
		t.Parallel()
		assert.True(t, hasCheckCmd, "package documentation missing distinct generate check command line")
	})
}
