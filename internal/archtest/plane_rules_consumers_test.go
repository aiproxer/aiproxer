package archtest

import (
	"go/parser"
	"go/token"
	"testing"
)

// scanSyntheticSource parses src and runs ScanFileForForbiddenMirrors.
func scanSyntheticSource(t *testing.T, relPath, src string, wave MigrationWave) []MirrorFinding {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse synthetic source %s: %v", relPath, err)
	}
	return ScanFileForForbiddenMirrors(relPath, []byte(src), fset, f, wave)
}

// TestForbiddenMirrorPredicate_ObserverStageConsumersAllowlistAndSpoofing verifies that exact
// stage consumers (e.g. internal/infra/runtimebundle.buildRuntimeSnapshot) are allowed, while
// unauthorized or spoofed functions reading observer planes via Get are strictly rejected under Wave 2.
func TestForbiddenMirrorPredicate_ObserverStageConsumersAllowlistAndSpoofing(t *testing.T) {
	t.Parallel()

	// 1. Unauthorized function reading PlaneTrafficObservers via Get is REJECTED
	unauthorizedSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func unauthorizedObserverConsumer(f aipfeature.FrozenPlaneSet) []traffic.Observer {
	return aipfeature.Get(f, aipfeature.PlaneTrafficObservers)
}
`
	findingsUnauth := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthorizedSrc, Wave2_Observers)
	if len(findingsUnauth) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedObserverConsumer at Wave2")
	}
	if findingsUnauth[0].ShapeKind != MirrorStageConsumer || findingsUnauth[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding: %+v", findingsUnauth[0])
	}

	// 2. Spoofed variants (e.g. buildRuntimeSnapshot2, buildRuntimeSnapshot_evil) are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "buildRuntimeSnapshot2"},
		{name: "suffix spoof", funcName: "buildRuntimeSnapshot_evil"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func ` + tc.funcName + `(f aipfeature.FrozenPlaneSet) []traffic.Observer {
	return aipfeature.Get(f, aipfeature.PlaneTrafficObservers)
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_extension.go", src, Wave2_Observers)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for spoofed %s (%s) at Wave2", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != "traffic_observers" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Legitimate exact allowlist function (buildRuntimeSnapshot) is ALLOWED
	allowedSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

func buildRuntimeSnapshot(f aipfeature.FrozenPlaneSet) []traffic.Observer {
	return aipfeature.Get(f, aipfeature.PlaneTrafficObservers)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_extension.go", allowedSrc, Wave2_Observers)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed exact buildRuntimeSnapshot function, got: %+v", allowedFindings)
	}

	// 4. Same-name function in foreign package (foreign spoof) is REJECTED
	foreignFindings := scanSyntheticSource(t, "internal/foreign/spoof.go", allowedSrc, Wave2_Observers)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for foreign same-name spoof buildRuntimeSnapshot at Wave2")
	}
	if foreignFindings[0].ShapeKind != MirrorStageConsumer || foreignFindings[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding for foreign spoof: %+v", foreignFindings[0])
	}
}

// TestForbiddenMirrorPredicate_ForeignHookProjectionRejected verifies that a
// foreign function attempting to project hooks via Get is strictly rejected under Wave 1.
func TestForbiddenMirrorPredicate_ForeignHookProjectionRejected(t *testing.T) {
	t.Parallel()

	foreignHookSrc := `package foreign
import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func ForeignHookProjection(f aipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks: aipfeature.Get(f, aipfeature.PlaneSubmitHooks),
	}
}
`
	foreignFindings := scanSyntheticSource(t, "internal/foreign/fake_hooks.go", foreignHookSrc, Wave1_HookBus)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden finding for foreign hook projection at Wave1")
	}
}

// TestForbiddenMirrorPredicate_ExecutorRuntimeStageConsumersAllowlistAndSpoofing verifies that exact
// stage consumer internal/infra/runtimebundle.buildExecutorRuntime is allowed, while unauthorized or
// spoofed functions reading tool planes via Get are strictly rejected under Wave 4.
func TestForbiddenMirrorPredicate_ExecutorRuntimeStageConsumersAllowlistAndSpoofing(t *testing.T) {
	t.Parallel()

	// 1. Unauthorized function reading PlaneToolCallFinalizationMaxArgsBytes via Get is REJECTED
	unauthorizedSrc := `package runtimebundle
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func unauthorizedToolConsumer(f aipfeature.FrozenPlaneSet) int {
	return aipfeature.Get(f, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
}
`
	findingsUnauth := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthorizedSrc, Wave4_Tools)
	if len(findingsUnauth) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedToolConsumer at Wave4")
	}
	if findingsUnauth[0].ShapeKind != MirrorStageConsumer || findingsUnauth[0].PlaneID != "tool_call_finalization_max_args_bytes" {
		t.Fatalf("unexpected finding: %+v", findingsUnauth[0])
	}

	// 2. Spoofed variants (e.g. buildExecutorRuntime2, buildExecutorRuntime_evil) are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "buildExecutorRuntime2"},
		{name: "suffix spoof", funcName: "buildExecutorRuntime_evil"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func ` + tc.funcName + `(f aipfeature.FrozenPlaneSet) int {
	return aipfeature.Get(f, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_executor.go", src, Wave4_Tools)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for spoofed %s (%s) at Wave4", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != "tool_call_finalization_max_args_bytes" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Legitimate exact allowlist function (buildExecutorRuntime) is ALLOWED
	allowedSrc := `package runtimebundle
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func buildExecutorRuntime(f aipfeature.FrozenPlaneSet) int {
	return aipfeature.Get(f, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_executor.go", allowedSrc, Wave4_Tools)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed exact buildExecutorRuntime function, got: %+v", allowedFindings)
	}

	// 4. Same-name function in foreign package (foreign spoof) is REJECTED
	foreignFindings := scanSyntheticSource(t, "internal/foreign/fake_executor.go", allowedSrc, Wave4_Tools)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for foreign same-name spoof buildExecutorRuntime at Wave4")
	}
	if foreignFindings[0].ShapeKind != MirrorStageConsumer || foreignFindings[0].PlaneID != "tool_call_finalization_max_args_bytes" {
		t.Fatalf("unexpected finding for foreign spoof: %+v", foreignFindings[0])
	}
}

// TestForbiddenMirrorPredicate_UnauthorizedToolPlaneConsumersRejected verifies that unauthorized
// functions attempting to access any Wave 4 tool planes via Get are strictly rejected.
func TestForbiddenMirrorPredicate_UnauthorizedToolPlaneConsumersRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		planeName string
		planeExpr string
		planeID   string
	}{
		{planeName: "ToolCatalogFilters", planeExpr: "aipfeature.PlaneToolCatalogFilters", planeID: "tool_catalog_filters"},
		{planeName: "ToolCallPolicies", planeExpr: "aipfeature.PlaneToolCallPolicies", planeID: "tool_call_policies"},
		{planeName: "ToolCallFinalizers", planeExpr: "aipfeature.PlaneToolCallFinalizers", planeID: "tool_call_finalizers"},
		{planeName: "ToolCallFinalizationMaxArgsBytes", planeExpr: "aipfeature.PlaneToolCallFinalizationMaxArgsBytes", planeID: "tool_call_finalization_max_args_bytes"},
	}

	for _, tc := range cases {
		t.Run(tc.planeName, func(t *testing.T) {
			t.Parallel()
			src := `package unauthorized
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func ConsumeToolPlane(f aipfeature.FrozenPlaneSet) any {
	return aipfeature.Get(f, ` + tc.planeExpr + `)
}
`
			findings := scanSyntheticSource(t, "internal/unauthorized/consumer.go", src, Wave4_Tools)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for %s at Wave4", tc.planeName)
			}
			if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != tc.planeID {
				t.Fatalf("unexpected finding for %s: %+v", tc.planeName, findings[0])
			}
		})
	}
}

// TestForbiddenMirrorPredicate_SecretGuardRuntimeStageConsumersAllowlistAndSpoofing verifies that exact
// observer projection internal/infra/runtimebundle.buildSecretGuardRuntime is allowed, while unauthorized or
// spoofed functions reading PlaneSecretGuards via Get are strictly rejected under Wave 5a.
func TestForbiddenMirrorPredicate_SecretGuardRuntimeStageConsumersAllowlistAndSpoofing(t *testing.T) {
	t.Parallel()

	// 1. Unauthorized function reading PlaneSecretGuards via Get is REJECTED
	unauthorizedSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func unauthorizedSGConsumer(f aipfeature.FrozenPlaneSet) []secretguard.Guard {
	return aipfeature.Get(f, aipfeature.PlaneSecretGuards)
}
`
	findingsUnauth := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthorizedSrc, Wave5a_GuardsCompaction)
	if len(findingsUnauth) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedSGConsumer at Wave5a")
	}
	if findingsUnauth[0].ShapeKind != MirrorStageConsumer || findingsUnauth[0].PlaneID != "secret_guards" {
		t.Fatalf("unexpected finding: %+v", findingsUnauth[0])
	}

	// 2. Spoofed variants (e.g. buildSecretGuardRuntime2, buildSecretGuardRuntime_evil) are REJECTED
	spoofVariants := []struct {
		name     string
		funcName string
	}{
		{name: "numbered spoof", funcName: "buildSecretGuardRuntime2"},
		{name: "suffix spoof", funcName: "buildSecretGuardRuntime_evil"},
	}

	for _, tc := range spoofVariants {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func ` + tc.funcName + `(f aipfeature.FrozenPlaneSet) []secretguard.Guard {
	return aipfeature.Get(f, aipfeature.PlaneSecretGuards)
}
`
			findings := scanSyntheticSource(t, "internal/infra/runtimebundle/secret_guard_runtime.go", src, Wave5a_GuardsCompaction)
			if len(findings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for spoofed %s (%s) at Wave5a", tc.name, tc.funcName)
			}
			if findings[0].ShapeKind != MirrorStageConsumer || findings[0].PlaneID != "secret_guards" {
				t.Fatalf("unexpected finding for %s: %+v", tc.funcName, findings[0])
			}
		})
	}

	// 3. Legitimate exact allowlist function (buildSecretGuardRuntime) is ALLOWED
	allowedSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func buildSecretGuardRuntime(f aipfeature.FrozenPlaneSet) []secretguard.Guard {
	return aipfeature.Get(f, aipfeature.PlaneSecretGuards)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/secret_guard_runtime.go", allowedSrc, Wave5a_GuardsCompaction)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed exact buildSecretGuardRuntime function, got: %+v", allowedFindings)
	}

	// 4. Same-name function in foreign package (foreign spoof) is REJECTED
	foreignFindings := scanSyntheticSource(t, "internal/foreign/fake_secret_guard.go", allowedSrc, Wave5a_GuardsCompaction)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for foreign same-name spoof buildSecretGuardRuntime at Wave5a")
	}
	if foreignFindings[0].ShapeKind != MirrorStageConsumer || foreignFindings[0].PlaneID != "secret_guards" {
		t.Fatalf("unexpected finding for foreign spoof: %+v", foreignFindings[0])
	}
}

// TestForbiddenMirrorPredicate_CompactionStageConsumersAllowlistAndSpoofing verifies that exact
// stage consumer internal/infra/runtimebundle.buildRuntimeSnapshot is allowed to read compaction planes,
// while unauthorized or spoofed functions reading PlaneCompactionObservers or PlaneCompactionPreservers
// via Get are strictly rejected under Wave 5a.
func TestForbiddenMirrorPredicate_CompactionStageConsumersAllowlistAndSpoofing(t *testing.T) {
	t.Parallel()

	// 1. Unauthorized function reading PlaneCompactionObservers via Get is REJECTED
	unauthObsSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func unauthorizedCompactionObsConsumer(f aipfeature.FrozenPlaneSet) []compaction.Observer {
	return aipfeature.Get(f, aipfeature.PlaneCompactionObservers)
}
`
	findingsUnauthObs := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthObsSrc, Wave5a_GuardsCompaction)
	if len(findingsUnauthObs) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedCompactionObsConsumer at Wave5a")
	}
	if findingsUnauthObs[0].ShapeKind != MirrorStageConsumer || findingsUnauthObs[0].PlaneID != "compaction_observers" {
		t.Fatalf("unexpected finding: %+v", findingsUnauthObs[0])
	}

	// 2. Unauthorized function reading PlaneCompactionPreservers via Get is REJECTED
	unauthPresSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func unauthorizedCompactionPresConsumer(f aipfeature.FrozenPlaneSet) []compaction.Preserver {
	return aipfeature.Get(f, aipfeature.PlaneCompactionPreservers)
}
`
	findingsUnauthPres := scanSyntheticSource(t, "internal/infra/runtimebundle/unauthorized.go", unauthPresSrc, Wave5a_GuardsCompaction)
	if len(findingsUnauthPres) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for unauthorizedCompactionPresConsumer at Wave5a")
	}
	if findingsUnauthPres[0].ShapeKind != MirrorStageConsumer || findingsUnauthPres[0].PlaneID != "compaction_preservers" {
		t.Fatalf("unexpected finding: %+v", findingsUnauthPres[0])
	}

	// 3. Legitimate exact allowlist function (buildRuntimeSnapshot) is ALLOWED
	allowedSrc := `package runtimebundle
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func buildRuntimeSnapshot(f aipfeature.FrozenPlaneSet) ([]compaction.Observer, []compaction.Preserver) {
	return aipfeature.Get(f, aipfeature.PlaneCompactionObservers), aipfeature.Get(f, aipfeature.PlaneCompactionPreservers)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/infra/runtimebundle/build_extension.go", allowedSrc, Wave5a_GuardsCompaction)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed exact buildRuntimeSnapshot function, got: %+v", allowedFindings)
	}

	// 4. Same-name function in foreign package (foreign spoof) is REJECTED
	foreignFindings := scanSyntheticSource(t, "internal/foreign/fake_compaction.go", allowedSrc, Wave5a_GuardsCompaction)
	if len(foreignFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for foreign same-name spoof buildRuntimeSnapshot at Wave5a")
	}
}
