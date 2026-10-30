package archtest

import (
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func loadTurnRecvASTFilesFromFSContext(ctx context.Context, fs archtestFS) ([]turnRecvASTFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	relDir := "internal/core/runtime"
	entries, err := fs.ReadDir(relDir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", relDir, err)
	}
	fset := token.NewFileSet()
	var files []turnRecvASTFile
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		relPath := relDir + "/" + entry.Name()
		content, err := fs.ReadFile(relPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relPath, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, relPath, content, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		imports := make(map[string]string)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("unquote import in %s: %w", entry.Name(), err)
			}
			alias := filepath.Base(importPath)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias != "_" && alias != "." {
				imports[alias] = importPath
			}
		}
		files = append(files, turnRecvASTFile{RelPath: relPath, AST: file, FSet: fset, Imports: imports})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, nil
}

func loadTurnRecvASTFilesFromFS(fs archtestFS) ([]turnRecvASTFile, error) {
	return loadTurnRecvASTFilesFromFSContext(context.Background(), fs)
}

func loadTurnRecvASTFilesContext(ctx context.Context, root string) ([]turnRecvASTFile, error) {
	return loadTurnRecvASTFilesFromFSContext(ctx, &workingTreeFS{root: root})
}

func loadTurnRecvASTFiles(root string) ([]turnRecvASTFile, error) {
	return loadTurnRecvASTFilesContext(context.Background(), root)
}

func nodeText(node ast.Node) string {
	var b strings.Builder
	if err := format.Node(&b, token.NewFileSet(), node); err != nil {
		return fmt.Sprintf("%T", node)
	}
	return b.String()
}

func syncTypesFor(typeText string) []string {
	var types []string
	for _, candidate := range []string{"sync.Mutex", "sync.RWMutex", "sync.Once", "sync.WaitGroup", "atomic.Bool", "atomic.Int32", "atomic.Int64", "atomic.Uint32", "atomic.Uint64", "atomic.Uintptr", "atomic.Pointer"} {
		if strings.Contains(typeText, candidate) {
			types = append(types, candidate)
		}
	}
	return types
}

func isRetryRecvReceiver(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "retryRecvStream"
}

func turnRecvReceiverName(expr ast.Expr) string {
	return "s"
}

func turnRecvFieldCategory(name string) string {
	switch name {
	case "facts", "baseline", "aLegID", "traceID", "compactionOpenMeta", "recvViews", "recvViewsOK", "routePrefs", "boundRegistry", "boundRegistryOK", "boundCatalog", "boundCatalogOK", "nativeResolver", "modelViewID", "modelViewIDOK", "secureTurn", "secureTurnOK":
		return "immutable_request_fact"
	case "attempt", "innerMu", "inner", "bleg", "cand", "authority", "attemptTerm", "accounting", "toolFinal", "promptCacheSource", "promptCacheController":
		return "current_attempt_state"
	case "recovery", "budget", "ttft", "sel", "requestSize", "session", "excluded", "rng", "lastHardReject", "lastHardTransportReject", "lastAdmissionErr", "isContextLimitExhaustion", "transformExcludes", "affinityKey", "affinitySet", "affinityCommitOnce", "recoverPolicy", "interleaved", "suppressThinker", "suppressVisibleMemo", "lastParallelFailure", "isInterleavedThinker":
		return "recovery_routing_state"
	case "responsePipeline", "seenEvents", "visibleText", "customer", "secureRecvRecordingHardStop", "gateBuf", "gateDrain", "gateLive", "recoverDrain", "lastAuthorityUsage", "lastCustomerUsage", "toolClass", "eventsMu", "usageMu", "finalStreamObs", "internalUsageKeys", "committedTools":
		return "response_pipeline_state"
	case "terminal", "committed", "finished", "endOnce", "metering", "requestAuth", "tokenAccountingFinalized", "aScope", "holdALegEnd", "termMu", "requestTerm", "billingLegMu", "billingLegRecorded", "billingCallClosureMu", "billingCallClosureSuccess", "billingAccountID", "billingCustomerPricing", "billingChargePolicy", "billingIdentityStamped", "billingCallID", "billingCallState", "keepwarmArmOnce":
		return "request_terminal_state"
	case "executor", "bus", "cachedCtxMu", "lastParent", "cachedCtx":
		return "infrastructure_compatibility_state"
	default:
		return "infrastructure_compatibility_state"
	}
}

func turnRecvMethodResponsibility(name string) string {
	switch {
	case name == "Recv" || name == "Close":
		return "eventstream_control"
	case name == "loadInner" || name == "storeInner" || name == "takeAndNilInner" || name == "lifecycleAttempt":
		return "current_attempt_state"
	case strings.Contains(name, "Billing") || strings.Contains(name, "Authority") || strings.Contains(name, "TokenAccounting") || strings.Contains(name, "RequestAuthority") || strings.Contains(name, "Cancellation") || strings.Contains(name, "Terminal") || name == "finishALegScope" || name == "commitSuccessfulTurn":
		return "request_terminal_state"
	case strings.Contains(name, "Compaction") || strings.Contains(name, "Traffic") || strings.Contains(name, "ClientFacing") || strings.Contains(name, "Tool") || strings.Contains(name, "Gate") || strings.Contains(name, "Usage") || strings.Contains(name, "Event") || name == "beforeEmitClientFacing":
		return "response_pipeline_state"
	case strings.Contains(name, "Replacement") || strings.Contains(name, "Affinity") || strings.Contains(name, "IdleContext") || strings.Contains(name, "DecisionEvidence") || name == "now":
		return "recovery_routing_state"
	case strings.Contains(name, "Context") || name == "recvHookMeta" || name == "viewsFor" || name == "completionSnapshot" || name == "completionGatesFromContext":
		return "immutable_request_fact"
	default:
		return "infrastructure_compatibility_state"
	}
}

func scanTurnRecvStateCopies(files []turnRecvASTFile, fieldNames map[string]bool) []turnRecvStateCopy {
	var copies []turnRecvStateCopy
	for _, file := range files {
		for _, decl := range file.AST.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil || !turnRecvStateCopyFunction(funcDecl.Name.Name) {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != len(assign.Rhs) {
					return true
				}
				for i := range assign.Lhs {
					left, leftOK := fieldSelectorText(assign.Lhs[i], fieldNames)
					right, rightOK := fieldSelectorText(assign.Rhs[i], fieldNames)
					if !leftOK && !rightOK {
						continue
					}
					copies = append(copies, turnRecvStateCopy{File: file.RelPath, Function: funcDecl.Name.Name, Line: file.FSet.Position(assign.Pos()).Line, Kind: "assignment", Left: left, Right: right})
				}
				return true
			})
			if funcDecl.Name.Name != "assemble" && funcDecl.Name.Name != "assembleExecutorStream" {
				continue
			}
			ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok || !isRetryRecvType(literal.Type) {
					return true
				}
				for _, element := range literal.Elts {
					keyValue, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := keyValue.Key.(*ast.Ident)
					if !ok || !fieldNames[key.Name] {
						continue
					}
					copies = append(copies, turnRecvStateCopy{File: file.RelPath, Function: funcDecl.Name.Name, Line: file.FSet.Position(keyValue.Pos()).Line, Kind: "assembly_literal", Left: key.Name, Right: nodeText(keyValue.Value)})
				}
				return true
			})
		}
	}
	sort.Slice(copies, func(i, j int) bool {
		if copies[i].File != copies[j].File {
			return copies[i].File < copies[j].File
		}
		if copies[i].Line != copies[j].Line {
			return copies[i].Line < copies[j].Line
		}
		if copies[i].Kind != copies[j].Kind {
			return copies[i].Kind < copies[j].Kind
		}
		return copies[i].Left < copies[j].Left
	})
	return copies
}

func turnRecvStateCopyFunction(name string) bool {
	switch name {
	case "assemble", "assembleExecutorStream", "tryReplacementIteration", "copyBoundModelViews":
		return true
	default:
		return false
	}
}

func fieldSelectorText(expr ast.Expr, fieldNames map[string]bool) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || !fieldNames[selector.Sel.Name] {
		return "", false
	}
	return nodeText(selector), true
}

func isRetryRecvType(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "retryRecvStream"
}

func turnRecvMirroredFacts() []turnRecvMirror {
	return []turnRecvMirror{
		{Fact: "output_commitment", Authorities: []string{"retryRecvStream.committed", "authorityLifecycle.outputCommitted", "ttftBudget.committed"}, Evidence: []string{"markCommitted stores committed atomic.Bool", "markCommitted calls authority.markOutputCommitted", "markOutputCommitted calls ttft.markCommitted"}},
		{Fact: "finished", Authorities: []string{"retryRecvStream.finished", "streamTerminal request owner state"}, Evidence: []string{"markFinished stores finished atomic.Bool", "runStreamTerminal owns request terminal claim"}},
		{Fact: "attempt_terminal", Authorities: []string{"retryRecvStream.attemptTerm", "authorityLifecycle.terminal", "streamTerminal attempt owner state"}, Evidence: []string{"resetAttemptTerminal replaces attemptTerm during retry", "authority lifecycle has independent settled/released terminal state"}},
		{Fact: "request_context", Authorities: []string{"retryRecvStream.recvViews/secureTurn/routePrefs/model views", "retryRecvStream.cachedCtx/lastParent", "retryRecvStream.metering/requestAuth"}, Evidence: []string{"recvExecContext projects pinned values into cached context", "bare Recv callers rely on stream snapshots", "request-scoped authorities are reattached from stream fields"}},
	}
}

func evaluateTurnRecvTarget(current turnRecvCurrentInventory, target turnRecvTargetTopology) []string {
	var findings []string
	if current.FieldCount > target.FacadeMaxDirectFields {
		findings = append(findings, fmt.Sprintf("direct fields=%d > target max %d", current.FieldCount, target.FacadeMaxDirectFields))
	}
	if current.SyncPrimitiveCount > target.FacadeMaxSyncPrimitives {
		findings = append(findings, fmt.Sprintf("sync primitives=%d > target max %d", current.SyncPrimitiveCount, target.FacadeMaxSyncPrimitives))
	}
	if current.MethodCount > target.FacadeMaxReceiverMethods {
		findings = append(findings, fmt.Sprintf("receiver methods=%d > target max %d", current.MethodCount, target.FacadeMaxReceiverMethods))
	}
	if current.CrossDomainMethodCount > target.FacadeMaxCrossDomainMethods {
		findings = append(findings, fmt.Sprintf("cross-domain receiver methods=%d > target max %d", current.CrossDomainMethodCount, target.FacadeMaxCrossDomainMethods))
	}
	if current.DomainPackageFanoutCount > target.FacadeMaxDomainPackageFanout {
		findings = append(findings, fmt.Sprintf("direct domain package fan-out=%d > target max %d", current.DomainPackageFanoutCount, target.FacadeMaxDomainPackageFanout))
	}
	if current.ExecutorReachability.FieldPresent {
		findings = append(findings, "façade retains broad *Executor field")
	}
	if current.ExecutorReachability.MethodCount > target.FacadeMaxExecutorReachableMethods {
		findings = append(findings, fmt.Sprintf("*Executor-reachable receiver methods=%d > target max %d", current.ExecutorReachability.MethodCount, target.FacadeMaxExecutorReachableMethods))
	}
	if current.StateCopyAssignmentCount > target.FacadeMaxStateCopyAssignments {
		findings = append(findings, fmt.Sprintf("assembly/replacement state-copy assignments=%d > target max %d", current.StateCopyAssignmentCount, target.FacadeMaxStateCopyAssignments))
	}
	for _, forbidden := range target.ForbiddenDirectFields {
		for _, field := range current.Fields {
			if field.Name == forbidden {
				findings = append(findings, "forbidden direct field remains: "+forbidden)
				break
			}
		}
	}
	return findings
}
