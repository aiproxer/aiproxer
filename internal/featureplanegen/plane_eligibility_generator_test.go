package featureplanegen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eligibilityManifest builds a minimal synthetic SDK manifest declaring the
// given planes in order. Each entry is an ordered plane with a closed
// request-access annotation; no identity/diag metadata is needed for the
// eligibility derivation contract.
func eligibilityManifest(t *testing.T, planes ...[2]string) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("package feature\n")
	sb.WriteString("import \"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy\"\n")
	for i, p := range planes {
		fmt.Fprintf(&sb, "var PlaneSynth%d = Plane[toolpolicy.Policy]{\n", i)
		fmt.Fprintf(&sb, "\tID: %q, Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n", p[0])
		sb.WriteString("\tNilPolicy: NilReject, RequestAccess: " + p[1] + ",\n")
		fmt.Fprintf(&sb, "\tCombine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },\n")
		sb.WriteString("}\n")
	}
	sb.WriteString("var StandardPlanes = []any{")
	for i := range planes {
		fmt.Fprintf(&sb, "PlaneSynth%d,", i)
	}
	sb.WriteString("}\n")
	return []byte(sb.String())
}

// TestGenerateWireEligibilityCode_DerivesOrderAndAccess pins Requirement 1.1:
// every declared identifier and its request-access class appears in the
// emitted core-local catalog in StandardPlanes order, with no second
// handwritten eligibility entry. An appended synthetic declaration produces a
// serving catalog entry through the emitter alone.
func TestGenerateWireEligibilityCode_DerivesOrderAndAccess(t *testing.T) {
	t.Parallel()

	out, err := GenerateWireEligibilityCode(eligibilityManifest(t,
		[2]string{"zeta_plane", "RequestBodyCanonicalRequired"},
		[2]string{"alpha_plane", "RequestBodyMetadataOnly"},
		[2]string{"mid_plane", "RequestBodyResponseOnly"},
		[2]string{"wire_plane", "RequestBodyWireContract"},
	))
	require.NoError(t, err)

	code := string(out)
	for _, want := range []string{
		"wireEligibilityPlaneOrder",
		"WireEligibilityPlaneCount",
		"standardPlaneV1Access",
		`"zeta_plane"`, `"alpha_plane"`, `"mid_plane"`, `"wire_plane"`,
		"PlaneAccessCanonicalRequired",
		"PlaneAccessMetadataOnly",
		"PlaneAccessResponseOnly",
		"PlaneAccessWireContract",
		"[32 - WireEligibilityPlaneCount]",
	} {
		assert.Contains(t, code, want, "emitted catalog must contain %q", want)
	}

	// Stable order: emitted identifiers follow StandardPlanes declaration
	// order, not alphabetical order.
	zeta := strings.Index(code, `"zeta_plane"`)
	alpha := strings.Index(code, `"alpha_plane"`)
	mid := strings.Index(code, `"mid_plane"`)
	wire := strings.Index(code, `"wire_plane"`)
	require.True(t, zeta >= 0 && alpha >= 0 && mid >= 0 && wire >= 0, "all declared IDs must be emitted")
	assert.True(t, zeta < alpha && alpha < mid && mid < wire,
		"emitted order must follow declaration order (zeta=%d alpha=%d mid=%d wire=%d)", zeta, alpha, mid, wire)

	// Deterministic: the same manifest renders identical bytes.
	again, err := GenerateWireEligibilityCode(eligibilityManifest(t,
		[2]string{"zeta_plane", "RequestBodyCanonicalRequired"},
		[2]string{"alpha_plane", "RequestBodyMetadataOnly"},
		[2]string{"mid_plane", "RequestBodyResponseOnly"},
		[2]string{"wire_plane", "RequestBodyWireContract"},
	))
	require.NoError(t, err)
	assert.Equal(t, string(out), string(again), "emitter must be deterministic")
}

// TestGenerateWireEligibilityCode_RejectsInvalid pins Requirements 1.2/1.6:
// malformed or over-capacity input rejects before installing replacement
// artifacts.
func TestGenerateWireEligibilityCode_RejectsInvalid(t *testing.T) {
	t.Parallel()

	t.Run("missing access", func(t *testing.T) {
		t.Parallel()
		manifest := "package feature\n" +
			"import \"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy\"\n" +
			"var PlaneSynth0 = Plane[toolpolicy.Policy]{\n" +
			"\tID: \"lonely_plane\", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n" +
			"\tNilPolicy: NilReject,\n" +
			"\tCombine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },\n" +
			"}\n" +
			"var StandardPlanes = []any{PlaneSynth0}\n"
		_, err := GenerateWireEligibilityCode([]byte(manifest))
		require.Error(t, err)
	})

	t.Run("unclassified access", func(t *testing.T) {
		t.Parallel()
		_, err := GenerateWireEligibilityCode(eligibilityManifest(t,
			[2]string{"vague_plane", "RequestBodyAccessUnclassified"},
		))
		require.Error(t, err)
	})

	t.Run("unknown access", func(t *testing.T) {
		t.Parallel()
		_, err := GenerateWireEligibilityCode(eligibilityManifest(t,
			[2]string{"strange_plane", "RequestBodyTimeTravel"},
		))
		require.Error(t, err)
	})

	t.Run("duplicate identifier", func(t *testing.T) {
		t.Parallel()
		_, err := GenerateWireEligibilityCode(eligibilityManifest(t,
			[2]string{"dup_plane", "RequestBodyCanonicalRequired"},
			[2]string{"dup_plane", "RequestBodyMetadataOnly"},
		))
		require.Error(t, err)
	})

	t.Run("undeclared inventory member", func(t *testing.T) {
		t.Parallel()
		manifest := "package feature\n" +
			"import \"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy\"\n" +
			"var PlaneSynth0 = Plane[toolpolicy.Policy]{\n" +
			"\tID: \"real_plane\", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n" +
			"\tNilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired,\n" +
			"\tCombine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },\n" +
			"}\n" +
			"var StandardPlanes = []any{PlaneSynth0, PlaneGhost}\n"
		_, err := GenerateWireEligibilityCode([]byte(manifest))
		require.Error(t, err)
	})

	t.Run("declared but not inventoried", func(t *testing.T) {
		t.Parallel()
		manifest := string(eligibilityManifest(t,
			[2]string{"real_plane", "RequestBodyCanonicalRequired"},
		))
		// Declare a second plane without adding it to StandardPlanes.
		manifest = strings.Replace(manifest,
			"var StandardPlanes",
			"var PlaneOrphan = Plane[toolpolicy.Policy]{\n"+
				"\tID: \"orphan_plane\", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n"+
				"\tNilPolicy: NilReject, RequestAccess: RequestBodyMetadataOnly,\n"+
				"\tCombine: func(s SourceKind, c, in toolpolicy.Policy) (toolpolicy.Policy, error) { return in, nil },\n"+
				"}\nvar StandardPlanes",
			1)
		_, err := GenerateWireEligibilityCode([]byte(manifest))
		require.Error(t, err)
	})
}

// eligibilityManifestN builds a synthetic manifest with n sequentially named
// planes, alternating the accepted access classes.
func eligibilityManifestN(t *testing.T, n int) []byte {
	t.Helper()
	classes := []string{
		"RequestBodyCanonicalRequired",
		"RequestBodyMetadataOnly",
		"RequestBodyResponseOnly",
		"RequestBodyWireContract",
	}
	planes := make([][2]string, 0, n)
	for i := range n {
		planes = append(planes, [2]string{
			fmt.Sprintf("synth_plane_%02d", i),
			classes[i%len(classes)],
		})
	}
	return eligibilityManifest(t, planes...)
}

// TestGenerateWireEligibilityCode_CapacityBoundary pins Requirement 1.6: the
// 32-bit blocker mask bounds the inventory. A full 32-plane inventory renders;
// a 33rd plane fails generation rather than truncating a plane.
func TestGenerateWireEligibilityCode_CapacityBoundary(t *testing.T) {
	t.Parallel()

	out, err := GenerateWireEligibilityCode(eligibilityManifestN(t, 32))
	require.NoError(t, err, "a 32-plane inventory fits the 32-bit blocker mask")
	assert.Contains(t, string(out), "[32 - WireEligibilityPlaneCount]")

	_, err = GenerateWireEligibilityCode(eligibilityManifestN(t, 33))
	require.Error(t, err, "a 33-plane inventory exceeds blocker capacity and must fail generation")
	assert.Contains(t, err.Error(), "32")
}

// TestGenerateWireEligibilityCode_AppendedPlanePreservesPrefix pins
// append-only growth: a 29-plane inventory emits 29 ordered entries with the
// first-28 prefix preserved in declaration order, and rendering stays
// deterministic.
func TestGenerateWireEligibilityCode_AppendedPlanePreservesPrefix(t *testing.T) {
	t.Parallel()

	out28, err := GenerateWireEligibilityCode(eligibilityManifestN(t, 28))
	require.NoError(t, err)
	out29, err := GenerateWireEligibilityCode(eligibilityManifestN(t, 29))
	require.NoError(t, err)

	code29 := string(out29)
	ids := make([]string, 0, 29)
	for i := range 29 {
		ids = append(ids, fmt.Sprintf("synth_plane_%02d", i))
	}
	for _, id := range ids {
		assert.Contains(t, code29, `"`+id+`"`, "emitted catalog must contain %q", id)
	}
	// Ordered entries: first occurrence of each ID follows declaration order.
	prev := -1
	for _, id := range ids {
		idx := strings.Index(code29, `"`+id+`"`)
		require.GreaterOrEqual(t, idx, 0, "declared ID %q must be emitted", id)
		assert.Greater(t, idx, prev, "emitted order must follow declaration order at %q", id)
		prev = idx
	}
	// Prefix preserved: the 28-plane prefix order matches the 29-plane prefix.
	code28 := string(out28)
	prev28, prev29 := -1, -1
	for i := range 28 {
		id := fmt.Sprintf("synth_plane_%02d", i)
		i28 := strings.Index(code28, `"`+id+`"`)
		i29 := strings.Index(code29, `"`+id+`"`)
		require.GreaterOrEqual(t, i28, 0, "28-plane catalog must contain %q", id)
		require.GreaterOrEqual(t, i29, 0, "29-plane catalog must contain %q", id)
		assert.Greater(t, i28, prev28, "28-plane order must follow declaration order at %q", id)
		assert.Greater(t, i29, prev29, "29-plane prefix must follow declaration order at %q", id)
		prev28, prev29 = i28, i29
	}

	// Deterministic: the same 29-plane manifest renders identical bytes.
	again, err := GenerateWireEligibilityCode(eligibilityManifestN(t, 29))
	require.NoError(t, err)
	assert.Equal(t, string(out29), string(again), "emitter must be deterministic")
}
