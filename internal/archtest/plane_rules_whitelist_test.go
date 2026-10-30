package archtest

import (
	"go/ast"
	"strings"
	"testing"
)

// TestAllowedStageConsumers_AllWhitelistedEntriesExercised verifies that every single
// entry in AllowedStageConsumers is exercised via a synthetic fixture, proving that
// each entry is allowed when placed in its authorized location, rejected outside it,
// and rejected when not thinly delegating (Finding 3 fixture b).
func TestAllowedStageConsumers_AllWhitelistedEntriesExercised(t *testing.T) {
	t.Parallel()

	type fixture struct {
		qualSym   string
		relPath   string
		planeID   string
		planeExpr string
		wave      MigrationWave
		isMethod  bool
		funcName  string
	}

	fixtures := []fixture{
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).CompletionGates",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "completion_gates",
			planeExpr: "aipfeature.PlaneCompletionGates",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "CompletionGates",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).RequestTransforms",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "request_transforms",
			planeExpr: "aipfeature.PlaneRequestTransforms",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "RequestTransforms",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).PreRequestHandlers",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "pre_request_handlers",
			planeExpr: "aipfeature.PlanePreRequestHandlers",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "PreRequestHandlers",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).RouteHintProviders",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "route_hint_providers",
			planeExpr: "aipfeature.PlaneRouteHintProviders",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "RouteHintProviders",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).AttemptTransforms",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "attempt_transforms",
			planeExpr: "aipfeature.PlaneAttemptTransforms",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "AttemptTransforms",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).SessionOpeners",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "session_openers",
			planeExpr: "aipfeature.PlaneSessionOpeners",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "SessionOpeners",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).Workspace",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "workspace_resolvers",
			planeExpr: "aipfeature.PlaneWorkspaceResolvers",
			wave:      Wave3_RequestShaping,
			isMethod:  true,
			funcName:  "Workspace",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ToolCatalogFilters",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "tool_catalog_filters",
			planeExpr: "aipfeature.PlaneToolCatalogFilters",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ToolCatalogFilters",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPolicies",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "tool_call_policies",
			planeExpr: "aipfeature.PlaneToolCallPolicies",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ToolCallPolicies",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPoliciesExecution",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "tool_call_policies",
			planeExpr: "aipfeature.PlaneToolCallPolicies",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ToolCallPoliciesExecution",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizers",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "tool_call_finalizers",
			planeExpr: "aipfeature.PlaneToolCallFinalizers",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ToolCallFinalizers",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizersExecution",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "tool_call_finalizers",
			planeExpr: "aipfeature.PlaneToolCallFinalizers",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ToolCallFinalizersExecution",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuards",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "secret_guards",
			planeExpr: "aipfeature.PlaneSecretGuards",
			wave:      Wave5a_GuardsCompaction,
			isMethod:  true,
			funcName:  "SecretGuards",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuardsExecution",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "secret_guards",
			planeExpr: "aipfeature.PlaneSecretGuards",
			wave:      Wave5a_GuardsCompaction,
			isMethod:  true,
			funcName:  "SecretGuardsExecution",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).CompactionObservers",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "compaction_observers",
			planeExpr: "aipfeature.PlaneCompactionObservers",
			wave:      Wave5a_GuardsCompaction,
			isMethod:  true,
			funcName:  "CompactionObservers",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).CompactionPreservers",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "compaction_preservers",
			planeExpr: "aipfeature.PlaneCompactionPreservers",
			wave:      Wave5a_GuardsCompaction,
			isMethod:  true,
			funcName:  "CompactionPreservers",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlers",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "local_turn_handlers",
			planeExpr: "aipfeature.PlaneLocalTurnHandlers",
			wave:      Wave5b_LocalTurnTerminal,
			isMethod:  true,
			funcName:  "LocalTurnHandlers",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlersExecution",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "local_turn_handlers",
			planeExpr: "aipfeature.PlaneLocalTurnHandlers",
			wave:      Wave5b_LocalTurnTerminal,
			isMethod:  true,
			funcName:  "LocalTurnHandlersExecution",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProvider",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "terminal_decision_provider",
			planeExpr: "aipfeature.PlaneTerminalDecisionProvider",
			wave:      Wave5b_LocalTurnTerminal,
			isMethod:  true,
			funcName:  "TerminalDecisionProvider",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProviderIdentity",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "terminal_decision_provider",
			planeExpr: "aipfeature.PlaneTerminalDecisionProvider",
			wave:      Wave5b_LocalTurnTerminal,
			isMethod:  true,
			funcName:  "TerminalDecisionProviderIdentity",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).SessionClassifier",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "session_classifier",
			planeExpr: "aipfeature.PlaneSessionClassifier",
			wave:      Wave5c_Residual,
			isMethod:  true,
			funcName:  "SessionClassifier",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ControlToolProvider",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "control_tool_provider",
			planeExpr: "aipfeature.PlaneControlToolProvider",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ControlToolProvider",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).ControlToolProviderIdentity",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "control_tool_provider",
			planeExpr: "aipfeature.PlaneControlToolProvider",
			wave:      Wave4_Tools,
			isMethod:  true,
			funcName:  "ControlToolProviderIdentity",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).TrafficObserver",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "traffic_observers",
			planeExpr: "aipfeature.PlaneTrafficObservers",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "TrafficObserver",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).UsageObserver",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "usage_observers",
			planeExpr: "aipfeature.PlaneUsageObservers",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "UsageObserver",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).RawCapture",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "raw_capture_sinks",
			planeExpr: "aipfeature.PlaneRawCaptureSinks",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "RawCapture",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).TrafficRedactors",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "traffic_redactors",
			planeExpr: "aipfeature.PlaneTrafficRedactors",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "TrafficRedactors",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).StreamObserverFactories",
			relPath:   "internal/core/extensions/snapshot.go",
			planeID:   "stream_observer_factories",
			planeExpr: "aipfeature.PlaneStreamObserverFactories",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "StreamObserverFactories",
		},
		{
			qualSym:   "internal/core/extensions.(*RequestRuntimeSnapshot).TrafficPortBundle",
			relPath:   "internal/core/extensions/seam_views.go",
			planeID:   "traffic_observers",
			planeExpr: "aipfeature.PlaneTrafficObservers",
			wave:      Wave2_Observers,
			isMethod:  true,
			funcName:  "TrafficPortBundle",
		},
		{
			qualSym:   "internal/core/extensions.CompletionGatesFromContext",
			relPath:   "internal/core/extensions/seam_views.go",
			planeID:   "completion_gates",
			planeExpr: "aipfeature.PlaneCompletionGates",
			wave:      Wave3_RequestShaping,
			isMethod:  false,
			funcName:  "CompletionGatesFromContext",
		},
	}

	if len(fixtures) != len(AllowedStageConsumers) {
		t.Fatalf("fixtures count (%d) does not match AllowedStageConsumers count (%d)", len(fixtures), len(AllowedStageConsumers))
	}

	for _, fix := range fixtures {
		t.Run(fix.qualSym, func(t *testing.T) {
			t.Parallel()

			if !IsAllowedStageConsumer(fix.qualSym) {
				t.Fatalf("expected %q to be in AllowedStageConsumers", fix.qualSym)
			}

			var allowedSrc string
			if fix.isMethod {
				allowedSrc = `package extensions
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) ` + fix.funcName + `() any {
	return aipfeature.Get(s.frozen, ` + fix.planeExpr + `)
}
`
			} else {
				allowedSrc = `package extensions
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func ` + fix.funcName + `(s *RequestRuntimeSnapshot) any {
	return aipfeature.Get(s.frozen, ` + fix.planeExpr + `)
}
`
			}

			// (a) Scanned at authorized path: PASSES (0 findings)
			passFindings := scanSyntheticSource(t, fix.relPath, allowedSrc, fix.wave)
			if len(passFindings) != 0 {
				t.Fatalf("expected 0 findings for authorized stage consumer %q at %s, got: %+v", fix.qualSym, fix.relPath, passFindings)
			}

			// (b) Scanned at unauthorized path: REJECTED
			rejectFindings := scanSyntheticSource(t, "internal/custom/unauthorized.go", allowedSrc, fix.wave)
			if len(rejectFindings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for %q at unauthorized path", fix.qualSym)
			}
			if rejectFindings[0].ShapeKind != MirrorStageConsumer || rejectFindings[0].PlaneID != fix.planeID {
				t.Fatalf("unexpected finding at unauthorized path: %+v", rejectFindings[0])
			}

			// (c) Scanned at authorized path with non-thin delegate: REJECTED
			var nonThinSrc string
			if fix.isMethod {
				nonThinSrc = `package extensions
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) ` + fix.funcName + `() any {
	if s != nil { return nil }
	return aipfeature.Get(s.frozen, ` + fix.planeExpr + `)
}
`
			} else {
				nonThinSrc = `package extensions
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func ` + fix.funcName + `(s *RequestRuntimeSnapshot) any {
	if s != nil { return nil }
	return aipfeature.Get(s.frozen, ` + fix.planeExpr + `)
}
`
			}
			nonThinFindings := scanSyntheticSource(t, fix.relPath, nonThinSrc, fix.wave)
			if len(nonThinFindings) == 0 {
				t.Fatalf("expected forbidden stage consumer finding for non-thin delegate of %q", fix.qualSym)
			}
			if !strings.Contains(nonThinFindings[0].Detail, "does not thinly delegate to Get") {
				t.Fatalf("expected detail to mention non-thin delegation for %q, got: %q", fix.qualSym, nonThinFindings[0].Detail)
			}

			// (d) Scanned at authorized path with nil-safe thin delegate: PASSES for methods
			if fix.isMethod {
				nilSafeMethodSrc := `package extensions
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) ` + fix.funcName + `() any {
	if s == nil { return nil }
	return aipfeature.Get(s.frozen, ` + fix.planeExpr + `)
}
`
				nilSafeFindings := scanSyntheticSource(t, fix.relPath, nilSafeMethodSrc, fix.wave)
				if len(nilSafeFindings) != 0 {
					t.Fatalf("expected 0 findings for nil-safe authorized stage consumer %q at %s, got: %+v", fix.qualSym, fix.relPath, nilSafeFindings)
				}
			}
		})
	}
}

// TestAllowedStageConsumers_ProductionSourcesScanned verifies that every entry
// in AllowedStageConsumers corresponds to an actual function/method scanned in production source.
func TestAllowedStageConsumers_ProductionSourcesScanned(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	foundSymbols := make(map[string]bool)
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if !strings.HasPrefix(rel, "internal/core/extensions/") {
			return nil
		}
		fset, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		_ = fset
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				sym := QualifiedSymbol(rel, fd)
				if AllowedStageConsumers[sym] {
					foundSymbols[sym] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to scan production files: %v", err)
	}

	for allowed := range AllowedStageConsumers {
		if !foundSymbols[allowed] {
			t.Errorf("entry in AllowedStageConsumers does not correspond to any actual production function scanned: %q", allowed)
		}
	}
}
