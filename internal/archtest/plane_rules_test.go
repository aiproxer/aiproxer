package archtest

import (
	"strings"
	"testing"
)

// TestForbiddenMirrorPredicate_FeatureBundleField verifies that hand-authored
// plane fields in FeatureBundle are rejected when their wave is completed,
// while whitelisted non-plane fields and generated files pass.
func TestForbiddenMirrorPredicate_FeatureBundleField(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

type FeatureBundle struct {
	SchemaVersion int
	SubmitHooks   []hooks.SubmitHook
}
`
	findings := scanSyntheticSource(t, "pkg/aipsdk/feature/bundle.go", forbiddenSrc, Wave5c_Residual)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden FeatureBundle field finding for SubmitHooks at Wave5c")
	}
	if findings[0].ShapeKind != MirrorFeatureBundleField || findings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// At Wave1 (and Wave0), FeatureBundle SDK contract fields are allowed until Wave5c residual removal
	findingsW1 := scanSyntheticSource(t, "pkg/aipsdk/feature/bundle.go", forbiddenSrc, Wave1_HookBus)
	if len(findingsW1) != 0 {
		t.Fatalf("expected 0 findings at Wave1 for FeatureBundle SDK field, got %+v", findingsW1)
	}

	// Whitelisted fields (SchemaVersion, Lifecycles, contributions) pass at all waves
	allowedSrc := `package feature
import aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"

type FeatureBundle struct {
	SchemaVersion int
	Lifecycles    []aipplugin.Lifecycle
	contributions ContributionSet
}
`
	allowedFindings := scanSyntheticSource(t, "pkg/aipsdk/feature/bundle.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed FeatureBundle fields, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_NamedTransportField verifies that hand-authored
// plane fields in named/handwritten transport structs (such as HandwrittenPlaneTransport or
// GeneratedMergeSurface) are rejected when their wave is completed.
func TestForbiddenMirrorPredicate_NamedTransportField(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package featurebundle
import "github.com/aiproxer/aiproxer/pkg/aipsdk/request"

type HandwrittenPlaneTransport struct {
	RequestTransforms []request.Transform
}
`
	findings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", forbiddenSrc, Wave3_RequestShaping)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden transport field finding for RequestTransforms at Wave3")
	}
	if findings[0].ShapeKind != MirrorNamedTransportField || findings[0].PlaneID != "request_transforms" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// Whitelisted fields pass on GeneratedMergeSurface
	allowedSrc := `package featurebundle
import (
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type GeneratedMergeSurface struct {
	Lifecycles []aipplugin.Lifecycle
	frozen     aipfeature.FrozenPlaneSet
}
`
	allowedFindings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed GeneratedMergeSurface fields, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_AppendBranch verifies that per-plane append branches
// in handwritten transports are rejected regardless of receiver type name.
func TestForbiddenMirrorPredicate_AppendBranch(t *testing.T) {
	t.Parallel()

	forbiddenSrc := `package featurebundle
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func (m *HandwrittenPlaneTransport) Append(b aipfeature.FeatureBundle) error {
	m.SubmitHooks = append(m.SubmitHooks, b.SubmitHooks...)
	return nil
}
`
	findings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", forbiddenSrc, Wave1_HookBus)
	if len(findings) == 0 {
		t.Fatalf("expected forbidden AppendBranch finding for SubmitHooks at Wave1")
	}
	if findings[0].ShapeKind != MirrorAppendBranch || findings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}

	// Lifecycle appending is allowed
	allowedSrc := `package featurebundle
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func (m *HandwrittenPlaneTransport) Append(b aipfeature.FeatureBundle) error {
	m.Lifecycles = append(m.Lifecycles, b.Lifecycles...)
	return nil
}
`
	allowedFindings := scanSyntheticSource(t, "internal/featurebundle/merge_surface.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed Lifecycle append, got %+v", allowedFindings)
	}
}

// TestForbiddenMirrorPredicate_ProjectionBranches verifies that per-plane field
// projections in hooksConfigFromTransport are rejected.
func TestForbiddenMirrorPredicate_ProjectionBranches(t *testing.T) {
	t.Parallel()

	// hooksConfigFromTransport
	hooksSrc := `package runtimebundle
import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
)

func hooksConfigFromTransport(t featurebundle.HandwrittenPlaneTransport) hooks.Config {
	return hooks.Config{
		SubmitHooks: t.SubmitHooks,
	}
}
`
	findingsHooks := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", hooksSrc, Wave1_HookBus)
	if len(findingsHooks) == 0 {
		t.Fatalf("expected forbidden ProjectionBranch finding for SubmitHooks in hooksConfigFromTransport")
	}
	if findingsHooks[0].ShapeKind != MirrorProjectionBranch || findingsHooks[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding: %+v", findingsHooks[0])
	}
}

// TestForbiddenMirrorPredicate_GenerationOperations verifies that generation operation
// fields and accessors not delegating to Get are rejected, while thin delegates pass.
func TestForbiddenMirrorPredicate_GenerationOperations(t *testing.T) {
	t.Parallel()

	// Direct struct field on generationOperations
	forbiddenFieldSrc := `package runtimebundle
import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

type generationOperations struct {
	terminalDecisionProvider terminaldecision.Provider
}
`
	findingsField := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", forbiddenFieldSrc, Wave5b_LocalTurnTerminal)
	if len(findingsField) == 0 {
		t.Fatalf("expected forbidden generationOperations field finding for terminalDecisionProvider at Wave5b")
	}
	if findingsField[0].ShapeKind != MirrorGenerationOpField || findingsField[0].PlaneID != "terminal_decision_provider" {
		t.Fatalf("unexpected finding: %+v", findingsField[0])
	}

	// Non-delegating accessor method
	nonDelegatingSrc := `package runtimebundle
import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	return b.operations.terminalDecisionProvider
}
`
	findingsMethod := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", nonDelegatingSrc, Wave5b_LocalTurnTerminal)
	if len(findingsMethod) == 0 {
		t.Fatalf("expected forbidden non-delegating method finding for TerminalDecisionProvider at Wave5b")
	}
	if findingsMethod[0].ShapeKind != MirrorGenerationOpField {
		t.Fatalf("unexpected finding: %+v", findingsMethod[0])
	}

	// Thin delegate calling aipfeature.Get passes
	thinDelegateSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	return aipfeature.Get(b.operations.frozen, aipfeature.PlaneTerminalDecisionProvider)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", thinDelegateSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for thin delegate method, got %+v", allowedFindings)
	}

	// Nil-safe thin delegate calling aipfeature.Get passes
	nilSafeThinDelegateSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func (b *GenerationBundle) TerminalDecisionProvider() terminaldecision.Provider {
	if b == nil {
		return nil
	}
	return aipfeature.Get(b.operations.frozen, aipfeature.PlaneTerminalDecisionProvider)
}
`
	allowedNilSafeFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/generation_bundle.go", nilSafeThinDelegateSrc, Wave5c_Residual)
	if len(allowedNilSafeFindings) != 0 {
		t.Fatalf("expected 0 findings for nil-safe thin delegate method, got %+v", allowedNilSafeFindings)
	}
}

// TestForbiddenMirrorPredicate_GeneratedFileWhitelist verifies that generated files
// are completely whitelisted from mirror checks.
func TestForbiddenMirrorPredicate_GeneratedFileWhitelist(t *testing.T) {
	t.Parallel()

	generatedSrc := `// Code generated by generate-feature-planes.go. DO NOT EDIT.
package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

type FeatureBundle struct {
	SubmitHooks []hooks.SubmitHook
}
`
	findings := scanSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", generatedSrc, Wave5c_Residual)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for generated file, got %+v", findings)
	}
}

// TestForbiddenMirrorPredicate_HookProjectionsHandwrittenAndSpoofingRejected verifies that
// adversarial and handwritten hook projections (including former allowlist symbols
// HooksConfigFromGenerated and HooksConfigFromFrozen) reading hook planes via struct
// fields or Get are strictly rejected under Wave1.
func TestForbiddenMirrorPredicate_HookProjectionsHandwrittenAndSpoofingRejected(t *testing.T) {
	t.Parallel()

	// 1. Adversarial HooksConfigFromTransport reading HandwrittenPlaneTransport.SubmitHooks is REJECTED
	adversarialUpperSrc := `package runtimebundle
import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
)

func HooksConfigFromTransport(t featurebundle.HandwrittenPlaneTransport) hooks.Config {
	return hooks.Config{
		SubmitHooks: t.SubmitHooks,
	}
}
`
	findingsUpper := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", adversarialUpperSrc, Wave1_HookBus)
	if len(findingsUpper) == 0 {
		t.Fatalf("expected forbidden finding for uppercase adversarial HooksConfigFromTransport at Wave1")
	}
	if findingsUpper[0].ShapeKind != MirrorProjectionBranch || findingsUpper[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding for HooksConfigFromTransport: %+v", findingsUpper[0])
	}

	// 2. Spoofed variants HooksConfigFromTransport2 and HooksConfigfromX are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "HooksConfigFromTransport2"},
		{name: "casing spoof", funcName: "HooksConfigfromX"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
)

func ` + tc.funcName + `(t featurebundle.HandwrittenPlaneTransport) hooks.Config {
	return hooks.Config{
		SubmitHooks: t.SubmitHooks,
	}
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", src, Wave1_HookBus)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden finding for spoofed %s (%s) at Wave1", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorProjectionBranch || findings[0].PlaneID != "submit_hooks" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Former allowlist functions (HooksConfigFromGenerated, HooksConfigFromFrozen) with handwritten Get are REJECTED
	formerAllowedHookSrc := `package runtimebundle
import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func HooksConfigFromGenerated(g featurebundle.GeneratedMergeSurface, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return HooksConfigFromFrozen(g.Frozen, p)
}

func HooksConfigFromFrozen(f aipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks: aipfeature.Get(f, aipfeature.PlaneSubmitHooks),
	}
}
`
	rejectedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_feature_hooks.go", formerAllowedHookSrc, Wave1_HookBus)
	if len(rejectedFindings) == 0 {
		t.Fatalf("expected forbidden finding for handwritten HooksConfigFromFrozen at Wave1 when no allowlist exists")
	}
	if rejectedFindings[0].ShapeKind != MirrorProjectionBranch || rejectedFindings[0].PlaneID != "submit_hooks" {
		t.Fatalf("unexpected finding for former allowed hook projection: %+v", rejectedFindings[0])
	}
}

// TestForbiddenMirrorsAbsent_ProductionBaseline verifies that at the current repository
// baseline wave (ActiveMigrationWave = Wave0_Baseline), the production repository has zero
// forbidden mirror violations.
func TestForbiddenMirrorsAbsent_ProductionBaseline(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	findings, err := ScanForbiddenMirrors(root, ActiveMigrationWave)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) > 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("forbidden mirrors present at active wave (%d):\n%s", len(findings), b.String())
	}
}
