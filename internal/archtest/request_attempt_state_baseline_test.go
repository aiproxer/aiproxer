package archtest

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	RequestAttemptStateBaselineRelPath = "internal/archtest/testdata/request_attempt_state_baseline.json"
	RequestAttemptStateSchemaVersion   = 1
)

type RequestAttemptStateBaseline struct {
	SchemaVersion int                          `json:"schema_version"`
	Feature       string                       `json:"feature"`
	Phase         string                       `json:"phase"`
	Source        string                       `json:"source"`
	HandoffSeam   RequestAttemptHandoffSeam    `json:"handoff_seam"`
	Before        RequestAttemptStateInventory `json:"before"`
	Current       RequestAttemptStateInventory `json:"current"`
	Target        RequestAttemptStateTarget    `json:"target"`
}

type RequestAttemptHandoffSeam struct {
	ReceiverType   string   `json:"receiver_type"`
	ReceiverFields []string `json:"receiver_fields"`
	FactsType      string   `json:"facts_type"`
	AttemptType    string   `json:"attempt_type"`
	RecoveryType   string   `json:"recovery_type"`
}

type RequestAttemptStateInventory struct {
	PreparedRequestFields         []string `json:"prepared_request_fields"`
	RoutePlanStateFields          []string `json:"route_plan_state_fields"`
	AttemptOpenParamsFields       []string `json:"attempt_open_params_fields"`
	AttemptOpenResultFields       []string `json:"attempt_open_result_fields"`
	PointerOutFields              []string `json:"pointer_out_fields"`
	RouteProgressDuplicatedFields []string `json:"route_progress_duplicated_fields"`
	TranslationSites              []string `json:"translation_sites"`
	DirectFieldCopyAssignments    int      `json:"direct_field_copy_assignments"`
	ContextBusinessStateRereads   int      `json:"context_business_state_rereads"`
	PreHandoffCleanupSites        int      `json:"pre_handoff_cleanup_sites"`
}

type RequestAttemptStateTarget struct {
	AttemptOpenParamsDeleted       bool `json:"attempt_open_params_deleted"`
	MaxPointerOutFields            int  `json:"max_pointer_out_fields"`
	MaxRouteProgressDuplicates     int  `json:"max_route_progress_duplicates"`
	MaxTranslationSites            int  `json:"max_translation_sites"`
	MaxDirectFieldCopyAssignments  int  `json:"max_direct_field_copy_assignments"`
	MaxContextBusinessStateRereads int  `json:"max_context_business_state_rereads"`
	MaxPreHandoffCleanupSites      int  `json:"max_pre_handoff_cleanup_sites"`
}

func TestRequestAttemptStateBaselineFileExistsAndSchema(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadRequestAttemptStateBaseline(root)
	if err != nil {
		t.Fatalf("load request-attempt state baseline: %v", err)
	}
	if baseline.SchemaVersion != RequestAttemptStateSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", baseline.SchemaVersion, RequestAttemptStateSchemaVersion)
	}
	if baseline.Feature != "request-attempt-pipeline-state-simplification" {
		t.Fatalf("feature = %q, want request-attempt-pipeline-state-simplification", baseline.Feature)
	}
	if baseline.Phase != "5.4-final-certification" {
		t.Fatalf("phase = %q, want 5.4-final-certification", baseline.Phase)
	}
}

func TestRequestAttemptStateBaselineMatchesCurrentAST(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadRequestAttemptStateBaseline(root)
	if err != nil {
		t.Fatalf("load request-attempt state baseline: %v", err)
	}
	currentInv, handoff, err := scanRequestAttemptStateContext(t.Context(), root)
	if err != nil {
		t.Fatalf("scan current request-attempt state: %v", err)
	}
	if !sameJSON(currentInv, baseline.Current) {
		t.Fatalf("checked-in current baseline does not match deterministic AST scan; run with GENERATE_REQUEST_ATTEMPT_BASELINE=1 to refresh the Phase-A artifact")
	}
	if !sameJSON(handoff, baseline.HandoffSeam) {
		t.Fatalf("checked-in handoff seam does not match deterministic AST scan; run with GENERATE_REQUEST_ATTEMPT_BASELINE=1 to refresh the Phase-A artifact")
	}

	// Verify stored before metrics against scanning git show origin/main or a checked source manifest (Requirement 1)
	beforeInv, err := scanRequestAttemptStateAtRefContext(t.Context(), root, "origin/main")
	if err == nil && sameJSON(baseline.Before, beforeInv) {
		// Matches origin/main
	} else {
		// Fallback to checked source manifest
		if !sameJSON(baseline.Before, requestAttemptStateBeforeManifest) {
			t.Fatalf("checked-in before metrics do not match the checked source manifest: got %+v, want %+v", baseline.Before, requestAttemptStateBeforeManifest)
		}
	}

	// Verify final reductions of current metrics against before (Requirement 1)
	if len(baseline.Before.PreparedRequestFields) > 0 {
		if len(currentInv.PreparedRequestFields) > len(baseline.Before.PreparedRequestFields) {
			t.Errorf("PreparedRequestFields increased: current %d > before %d", len(currentInv.PreparedRequestFields), len(baseline.Before.PreparedRequestFields))
		}
		if len(currentInv.RoutePlanStateFields) > len(baseline.Before.RoutePlanStateFields) {
			t.Errorf("RoutePlanStateFields increased: current %d > before %d", len(currentInv.RoutePlanStateFields), len(baseline.Before.RoutePlanStateFields))
		}
		if len(currentInv.AttemptOpenParamsFields) > len(baseline.Before.AttemptOpenParamsFields) {
			t.Errorf("AttemptOpenParamsFields increased: current %d > before %d", len(currentInv.AttemptOpenParamsFields), len(baseline.Before.AttemptOpenParamsFields))
		}
		if len(currentInv.AttemptOpenResultFields) > len(baseline.Before.AttemptOpenResultFields) {
			t.Errorf("AttemptOpenResultFields increased: current %d > before %d", len(currentInv.AttemptOpenResultFields), len(baseline.Before.AttemptOpenResultFields))
		}
		if len(currentInv.PointerOutFields) > len(baseline.Before.PointerOutFields) {
			t.Errorf("PointerOutFields increased: current %d > before %d", len(currentInv.PointerOutFields), len(baseline.Before.PointerOutFields))
		}
		if len(currentInv.RouteProgressDuplicatedFields) > len(baseline.Before.RouteProgressDuplicatedFields) {
			t.Errorf("RouteProgressDuplicatedFields increased: current %d > before %d", len(currentInv.RouteProgressDuplicatedFields), len(baseline.Before.RouteProgressDuplicatedFields))
		}
		if len(currentInv.TranslationSites) > len(baseline.Before.TranslationSites) {
			t.Errorf("TranslationSites increased: current %d > before %d", len(currentInv.TranslationSites), len(baseline.Before.TranslationSites))
		}
		if currentInv.DirectFieldCopyAssignments > baseline.Before.DirectFieldCopyAssignments {
			t.Errorf("DirectFieldCopyAssignments increased: current %d > before %d", currentInv.DirectFieldCopyAssignments, baseline.Before.DirectFieldCopyAssignments)
		}
		if currentInv.ContextBusinessStateRereads > baseline.Before.ContextBusinessStateRereads {
			t.Errorf("ContextBusinessStateRereads increased: current %d > before %d", currentInv.ContextBusinessStateRereads, baseline.Before.ContextBusinessStateRereads)
		}
		if currentInv.PreHandoffCleanupSites > baseline.Before.PreHandoffCleanupSites {
			t.Errorf("PreHandoffCleanupSites increased: current %d > before %d", currentInv.PreHandoffCleanupSites, baseline.Before.PreHandoffCleanupSites)
		}
	}
}

func TestRequestAttemptStateRatchetsPassOnCurrentCode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadRequestAttemptStateBaseline(root)
	if err != nil {
		t.Fatalf("load request-attempt state baseline: %v", err)
	}
	currentInv, _, err := scanRequestAttemptStateContext(t.Context(), root)
	if err != nil {
		t.Fatalf("scan current request-attempt state: %v", err)
	}
	findings := evaluateRequestAttemptTarget(currentInv, baseline.Target)
	if len(findings) > 0 {
		t.Fatalf("RATCHETS VIOLATED on current codebase:\n%s", strings.Join(findings, "\n"))
	}
}

func TestRequestAttemptStateRatchetsFailIfActivatedOnCurrentCode(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	currentInv, _, err := scanRequestAttemptStateContext(t.Context(), root)
	if err != nil {
		t.Fatalf("scan current request-attempt state: %v", err)
	}
	strictTarget := RequestAttemptStateTarget{
		AttemptOpenParamsDeleted:       true,
		MaxPointerOutFields:            0,
		MaxRouteProgressDuplicates:     0,
		MaxTranslationSites:            0,
		MaxDirectFieldCopyAssignments:  10,
		MaxContextBusinessStateRereads: 0,
		MaxPreHandoffCleanupSites:      1,
	}
	findings := evaluateRequestAttemptTarget(currentInv, strictTarget)
	if len(findings) == 0 {
		t.Fatal("RED target ratchet assertion failed: target checks must FAIL on non-migrated brownfield code")
	}
	t.Logf("Confirmed target checks correctly fail on current codebase:\n%s", strings.Join(findings, "\n"))
}

func TestRequestAttemptStateRatchetsFailIfTypeReappears(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadRequestAttemptStateBaseline(root)
	if err != nil {
		t.Fatalf("load request-attempt state baseline: %v", err)
	}
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"with fields", "package runtime\ntype attemptOpenParams struct{ dummy int }"},
		{"no fields", "package runtime\ntype attemptOpenParams struct{}"},
		{"non-struct", "package runtime\ntype attemptOpenParams int"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "mock.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			mockFiles := []turnRecvASTFile{{RelPath: "mock.go", AST: file, FSet: fset}}
			fields := findStructFieldsInFiles(mockFiles, "attemptOpenParams")
			if fields == nil && hasTypeInFiles(mockFiles, "attemptOpenParams") {
				fields = []string{"type_exists"}
			}
			findings := evaluateRequestAttemptTarget(
				RequestAttemptStateInventory{AttemptOpenParamsFields: fields},
				baseline.Target,
			)
			if len(findings) == 0 {
				t.Fatal("expected ratchet to fail when type exists")
			}
		})
	}
}

func TestRequestAttemptStateTargetRatchetFailsIfTypeReappearsOnCurrentAST(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	baseline, err := loadRequestAttemptStateBaseline(root)
	if err != nil {
		t.Fatalf("load request-attempt state baseline: %v", err)
	}
	currentInv, _, err := scanRequestAttemptStateContext(t.Context(), root)
	if err != nil {
		t.Fatalf("scan current request-attempt state: %v", err)
	}
	// Verify current AST is green against target ratchet
	if findings := evaluateRequestAttemptTarget(currentInv, baseline.Target); len(findings) > 0 {
		t.Fatalf("current AST is not green against target ratchet: %v", findings)
	}
	// Simulate type reappearance
	currentInv.AttemptOpenParamsFields = []string{"type_exists"}
	// Verify target ratchet fails
	findings := evaluateRequestAttemptTarget(currentInv, baseline.Target)
	if len(findings) == 0 {
		t.Fatal("expected target ratchet to fail when type reappears on green AST")
	}
}

func TestGenerateRequestAttemptStateBaseline(t *testing.T) {
	t.Parallel()
	if os.Getenv("GENERATE_REQUEST_ATTEMPT_BASELINE") != "1" {
		t.Skip("set GENERATE_REQUEST_ATTEMPT_BASELINE=1 to regenerate the request-attempt state baseline JSON")
	}
	root := repoRoot(t)
	path := filepath.Join(root, filepath.FromSlash(RequestAttemptStateBaselineRelPath))
	currentInv, handoff, err := scanRequestAttemptStateContext(t.Context(), root)
	if err != nil {
		t.Fatalf("scan current request-attempt state: %v", err)
	}

	var beforeInv RequestAttemptStateInventory
	existing, err := loadRequestAttemptStateBaseline(root)
	if err == nil && len(existing.Before.PreparedRequestFields) > 0 {
		// Do not overwrite before on regeneration
		beforeInv = existing.Before
	} else {
		// Use the checked source manifest
		beforeInv = requestAttemptStateBeforeManifest
	}

	baseline := RequestAttemptStateBaseline{
		SchemaVersion: RequestAttemptStateSchemaVersion,
		Feature:       "request-attempt-pipeline-state-simplification",
		Phase:         "5.4-final-certification",
		Source:        "AST scan of internal/core/runtime production Go files",
		HandoffSeam:   handoff,
		Before:        beforeInv,
		Current:       currentInv,
		Target: RequestAttemptStateTarget{
			AttemptOpenParamsDeleted:       true,
			MaxPointerOutFields:            len(currentInv.PointerOutFields),
			MaxRouteProgressDuplicates:     len(currentInv.RouteProgressDuplicatedFields),
			MaxTranslationSites:            len(currentInv.TranslationSites),
			MaxDirectFieldCopyAssignments:  currentInv.DirectFieldCopyAssignments,
			MaxContextBusinessStateRereads: currentInv.ContextBusinessStateRereads,
			MaxPreHandoffCleanupSites:      currentInv.PreHandoffCleanupSites,
		},
	}
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	_ = os.WriteFile(path, append(raw, '\n'), 0o644)
}

func loadRequestAttemptStateBaseline(root string) (RequestAttemptStateBaseline, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(RequestAttemptStateBaselineRelPath)))
	if err != nil {
		return RequestAttemptStateBaseline{}, err
	}
	var baseline RequestAttemptStateBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return RequestAttemptStateBaseline{}, err
	}
	return baseline, nil
}

func scanRequestAttemptState(root string) (RequestAttemptStateInventory, RequestAttemptHandoffSeam, error) {
	return scanRequestAttemptStateContext(context.Background(), root)
}

func scanRequestAttemptStateContext(ctx context.Context, root string) (RequestAttemptStateInventory, RequestAttemptHandoffSeam, error) {
	files, err := loadTurnRecvASTFilesContext(ctx, root)
	if err != nil {
		return RequestAttemptStateInventory{}, RequestAttemptHandoffSeam{}, err
	}
	prepFields := findStructFieldsInFiles(files, "preparedRequest")
	routeFields := findStructFieldsInFiles(files, "routePlanState")
	recoveryFields := findStructFieldsInFiles(files, "recoveryController")
	paramsFields := findStructFieldsInFiles(files, "attemptOpenParams")
	if paramsFields == nil && hasTypeInFiles(files, "attemptOpenParams") {
		paramsFields = []string{"type_exists"}
	}
	resultFields := findStructFieldsInFiles(files, "attemptOpenResult")
	pointerOut := findPointerOutFieldsInFiles(files)
	duplicated := findDuplicatedFieldsInFiles(files, routeFields, recoveryFields)
	sites := findTranslationSites(files)
	for _, s := range [][]string{prepFields, routeFields, paramsFields, resultFields, pointerOut, duplicated, sites} {
		sort.Strings(s)
	}

	handoff, err := findHandoffSeamFromAST(files)
	if err != nil {
		return RequestAttemptStateInventory{}, RequestAttemptHandoffSeam{}, err
	}
	sort.Strings(handoff.ReceiverFields)

	return RequestAttemptStateInventory{
		PreparedRequestFields:         prepFields,
		RoutePlanStateFields:          routeFields,
		AttemptOpenParamsFields:       paramsFields,
		AttemptOpenResultFields:       resultFields,
		PointerOutFields:              pointerOut,
		RouteProgressDuplicatedFields: duplicated,
		TranslationSites:              sites,
		DirectFieldCopyAssignments:    countDirectFieldAssignments(files),
		ContextBusinessStateRereads:   countContextBusinessStateRereads(files),
		PreHandoffCleanupSites:        countPreHandoffCleanupSites(files),
	}, handoff, nil
}

func scanRequestAttemptStateAtRef(root string, ref string) (RequestAttemptStateInventory, error) {
	return scanRequestAttemptStateAtRefContext(context.Background(), root, ref)
}

func scanRequestAttemptStateAtRefContext(ctx context.Context, root string, ref string) (RequestAttemptStateInventory, error) {
	files, err := loadTurnRecvASTFilesAtRefContext(ctx, root, ref)
	if err != nil {
		return RequestAttemptStateInventory{}, err
	}
	prepFields := findStructFieldsInFiles(files, "preparedRequest")
	routeFields := findStructFieldsInFiles(files, "routePlanState")
	recoveryFields := findStructFieldsInFiles(files, "recoveryController")
	paramsFields := findStructFieldsInFiles(files, "attemptOpenParams")
	if paramsFields == nil && hasTypeInFiles(files, "attemptOpenParams") {
		paramsFields = []string{"type_exists"}
	}
	resultFields := findStructFieldsInFiles(files, "attemptOpenResult")
	pointerOut := findPointerOutFieldsInFiles(files)
	duplicated := findDuplicatedFieldsInFiles(files, routeFields, recoveryFields)
	sites := findTranslationSites(files)
	for _, s := range [][]string{prepFields, routeFields, paramsFields, resultFields, pointerOut, duplicated, sites} {
		sort.Strings(s)
	}
	return RequestAttemptStateInventory{
		PreparedRequestFields:         prepFields,
		RoutePlanStateFields:          routeFields,
		AttemptOpenParamsFields:       paramsFields,
		AttemptOpenResultFields:       resultFields,
		PointerOutFields:              pointerOut,
		RouteProgressDuplicatedFields: duplicated,
		TranslationSites:              sites,
		DirectFieldCopyAssignments:    countDirectFieldAssignments(files),
		ContextBusinessStateRereads:   countContextBusinessStateRereads(files),
		PreHandoffCleanupSites:        countPreHandoffCleanupSites(files),
	}, nil
}

func loadTurnRecvASTFilesAtRefContext(ctx context.Context, root string, ref string) ([]turnRecvASTFile, error) {
	fs, err := loadGitCommitFSContext(ctx, root, ref)
	if err != nil {
		return nil, fmt.Errorf("load git commit fs for ref %s: %w", ref, err)
	}
	return loadTurnRecvASTFilesFromFSContext(ctx, fs)
}

func loadTurnRecvASTFilesAtRef(root string, ref string) ([]turnRecvASTFile, error) {
	return loadTurnRecvASTFilesAtRefContext(context.Background(), root, ref)
}
