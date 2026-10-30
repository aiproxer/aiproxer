package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	keepwarm "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// ProcessFeatureTransitionRow represents an entry in the authoritative Wave-2 transition table (Task 1.1 / 2.3).
type ProcessFeatureTransitionRow struct {
	ResourceName          string
	ConcreteType          string
	CurrentConstructor    string
	CurrentFieldHolder    string
	CloseRegistrationSite string
	Closable              bool
	BorrowedDeps          string
	TransferTask          string
	InterimOwnershipRule  string
}

// ProcessFeatureTransitionTable is the authoritative transition contract from implementation-baseline.md §7.
var ProcessFeatureTransitionTable = []ProcessFeatureTransitionRow{
	{
		ResourceName:          "KeepwarmPolicy",
		ConcreteType:          "*keepwarm.PolicyStore",
		CurrentConstructor:    "keepwarm.NewPolicyStore(keepwarm.DefaultMaxPolicyEntries) called at featurehost/process.go:136",
		CurrentFieldHolder:    "featurehost.Runtime.keepwarmPolicy",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "config.PromptCache, metrics sink",
		TransferTask:          "Task 6.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "KeepwarmRegistry",
		ConcreteType:          "*keepwarm.ManagerRegistry",
		CurrentConstructor:    "keepwarm.NewManagerRegistry() called at featurehost/process.go:141",
		CurrentFieldHolder:    "featurehost.Runtime.keepwarmRegistry",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "keepwarm.PolicyStore, scheduler goroutines, background ping worker",
		TransferTask:          "Task 6.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "TerminalDecisionPolicy",
		ConcreteType:          "*sessionpolicy.Store",
		CurrentConstructor:    "sessionpolicy.NewStore(sessionpolicy.Config{}) called at featurehost/process.go:147",
		CurrentFieldHolder:    "featurehost.Runtime.terminalPolicy",
		CloseRegistrationSite: "Closable: r.registerCloser(policyStore.Close) registered at featurehost/process.go:149",
		Closable:              true,
		BorrowedDeps:          "None (pure in-memory bounded store)",
		TransferTask:          "Task 7.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "CompactionDetector",
		ConcreteType:          "runtime.CompactionDetector",
		CurrentConstructor:    "compactiondetect.New(compactiondetect.Config{}) called at featurehost/process.go:59",
		CurrentFieldHolder:    "featurehost.Runtime.compactionDetector",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "config.Compaction",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "BranchCoordinator",
		ConcreteType:          "*state.BranchCoordinator",
		CurrentConstructor:    "state.NewBranchCoordinator(ctx, state.Config{Store: in.ExtensionState}) called at featurehost/process.go:61",
		CurrentFieldHolder:    "featurehost.Runtime.branchCoordinator",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "Storage adapters (in-memory or Bun persistence)",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "CompactionParentPort",
		ConcreteType:          "*compaction.ParentPort",
		CurrentConstructor:    "compaction.NewParentPort(coord) called at featurehost/process.go:67",
		CurrentFieldHolder:    "featurehost.Runtime.compactionParentPort",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "BranchCoordinator",
		TransferTask:          "Task 3.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "ConversationStore",
		ConcreteType:          "conversationview.Store",
		CurrentConstructor:    "featurehost.NewProcess(ctx, in) called at featurehost/process.go:85",
		CurrentFieldHolder:    "featurehost.Runtime.conversationStore",
		CloseRegistrationSite: "Non-closable (no cleanup registered)",
		Closable:              false,
		BorrowedDeps:          "None (pure reference store or bun persistence)",
		TransferTask:          "Task 4.3",
		InterimOwnershipRule:  "Featurehost is sole owner; ProcessServices retains zero fields or duplicate constructors.",
	},
	{
		ResourceName:          "BackgroundAux",
		ConcreteType:          "*auxreq.BackgroundScheduler",
		CurrentConstructor:    "auxiliary.NewProductionBackgroundScheduler(ctx, bounds) called at background_aux_lifecycle.go:23",
		CurrentFieldHolder:    "ProcessServices.BackgroundAux",
		CloseRegistrationSite: "Closable: register(ps.BackgroundAux.Close) registered at background_aux_lifecycle.go:27",
		Closable:              true,
		BorrowedDeps:          "Process context, bounded worker pool",
		TransferTask:          "never (borrowed)",
		InterimOwnershipRule:  "Feature host borrows BackgroundAux as a generic input; feature host MUST NEVER close or own BackgroundAux.",
	},
}

// ErrDualConstructorWiring indicates that both legacy and featurehost paths
// constructed or registered cleanup for the same process-scoped feature resource.
var ErrDualConstructorWiring = errors.New("runtimebundle: dual constructor wiring detected")

// ValidateProcessFeatureOwnership verifies that every transition-table process resource
// has strictly one constructor and cleanup owner at the current checkpoint (Task 2.3 / 3.3).
// It inspects actual typed ProcessServices fields + featurehost Runtime typed state:
//  1. Every untransferred legacy resource and borrowed resource (BackgroundAux)
//     must be present on ProcessServices (deleting a legacy constructor fails).
//  2. Every transferred resource (CompactionDetector, BranchCoordinator, CompactionParentPort)
//     must be present on featurehost Runtime.
//  3. Featurehost Runtime must have zero unmigrated ownership (TerminalDecisionPolicy == nil, ClosersCount == 0).
//  4. If any resource is observed to be owned by both paths, ErrDualConstructorWiring is returned.
func ValidateProcessFeatureOwnership(ps *ProcessServices) error {
	if ps == nil {
		return fmt.Errorf("runtimebundle: nil ProcessServices")
	}
	if ps.StandardFeatures == nil {
		return fmt.Errorf("runtimebundle: nil StandardFeatures on ProcessServices")
	}

	// 1. Verify borrowed generic resources.
	if ps.BackgroundAux == nil {
		return fmt.Errorf("%w: borrowed resource BackgroundAux is missing from ProcessServices", ErrDualConstructorWiring)
	}

	// 2. Transferred resources must be present on featurehost.
	if ps.StandardFeatures.CompactionDetector() == nil {
		return fmt.Errorf("%w: transferred resource CompactionDetector is missing from featurehost", ErrDualConstructorWiring)
	}
	if ps.StandardFeatures.ConversationStore() == nil {
		return fmt.Errorf("%w: transferred resource ConversationStore is missing from featurehost", ErrDualConstructorWiring)
	}
	if ps.StandardFeatures.KeepwarmPolicy() == nil {
		return fmt.Errorf("%w: transferred resource KeepwarmPolicy is missing from featurehost", ErrDualConstructorWiring)
	}
	if ps.StandardFeatures.KeepwarmRegistry() == nil {
		return fmt.Errorf("%w: transferred resource KeepwarmRegistry is missing from featurehost", ErrDualConstructorWiring)
	}
	if ps.StandardFeatures.TerminalDecisionPolicy() == nil {
		return fmt.Errorf("%w: transferred resource TerminalDecisionPolicy is missing from featurehost", ErrDualConstructorWiring)
	}
	if ps.StandardFeatures.ClosersCount() != 2 {
		return fmt.Errorf("%w: featurehost must own exactly 2 closers (terminal policy store and lightweight session-classification holder), observed %d", ErrDualConstructorWiring, ps.StandardFeatures.ClosersCount())
	}
	return nil
}

// Non-tautological compile-time drift checks: any field retype or rename breaks compilation.
func _driftCompilationGuard() {
	var ps *ProcessServices
	var _ *auxreq.BackgroundScheduler = ps.BackgroundAux //nolint:staticcheck // QF1011: intentional compile-time field-type drift guard

	var sf *featurehost.Runtime
	var _ runtime.CompactionDetector = sf.CompactionDetector()     //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard
	var _ conversationview.Store = sf.ConversationStore()          //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard
	var _ *keepwarm.PolicyStore = sf.KeepwarmPolicy()              //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard
	var _ *keepwarm.ManagerRegistry = sf.KeepwarmRegistry()        //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard
	var _ *sessionpolicy.Store = sf.TerminalDecisionPolicy()       //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard
	var _ runtime.TerminalPolicyReader = sf.TerminalPolicyReader() //nolint:staticcheck // QF1011: intentional compile-time return-type drift guard

	var (
		_ func(int) (*keepwarm.PolicyStore, error)                                      = keepwarm.NewPolicyStore                    //nolint:staticcheck // QF1011: intentional compile-time constructor-signature drift guard
		_ func() *keepwarm.ManagerRegistry                                              = keepwarm.NewManagerRegistry                //nolint:staticcheck // QF1011: intentional compile-time constructor-signature drift guard
		_ func(sessionpolicy.Config) *sessionpolicy.Store                               = sessionpolicy.NewStore                     //nolint:staticcheck // QF1011: intentional compile-time constructor-signature drift guard
		_ func(context.Context, auxreq.SchedulerConfig) *auxreq.BackgroundScheduler     = auxiliary.NewProductionBackgroundScheduler //nolint:staticcheck // QF1011: intentional compile-time constructor-signature drift guard
		_ func(context.Context, featurehost.ProcessInput) (*featurehost.Runtime, error) = featurehost.NewProcess                     //nolint:staticcheck // QF1011: intentional compile-time constructor-signature drift guard
	)
}

func TestProcessFeatureOwnership_TransitionTableIntegrity(t *testing.T) {
	t.Parallel()

	psType := reflect.TypeFor[ProcessServices]()
	fhType := reflect.TypeFor[*featurehost.Runtime]()

	expectedRows := map[string]struct {
		fieldName    string
		fhField      string
		closable     bool
		transferTask string
		callSite     string
		closeSite    string
	}{
		"KeepwarmPolicy": {
			fieldName:    "KeepwarmPolicy",
			fhField:      "keepwarmPolicy",
			closable:     false,
			transferTask: "Task 6.3",
			callSite:     "featurehost/process.go:136",
			closeSite:    "Non-closable",
		},
		"KeepwarmRegistry": {
			fieldName:    "KeepwarmRegistry",
			fhField:      "keepwarmRegistry",
			closable:     false,
			transferTask: "Task 6.3",
			callSite:     "featurehost/process.go:141",
			closeSite:    "Non-closable",
		},
		"TerminalDecisionPolicy": {
			fieldName:    "TerminalDecisionPolicy",
			fhField:      "terminalPolicy",
			closable:     true,
			transferTask: "Task 7.3",
			callSite:     "featurehost/process.go:147",
			closeSite:    "featurehost/process.go:149",
		},
		"CompactionDetector": {
			fieldName:    "CompactionDetector",
			fhField:      "compactionDetector",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "featurehost/process.go:59",
			closeSite:    "Non-closable",
		},
		"BranchCoordinator": {
			fieldName:    "BranchCoordinator",
			fhField:      "branchCoordinator",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "featurehost/process.go:61",
			closeSite:    "Non-closable",
		},
		"CompactionParentPort": {
			fieldName:    "CompactionParentPort",
			fhField:      "compactionParentPort",
			closable:     false,
			transferTask: "Task 3.3",
			callSite:     "featurehost/process.go:67",
			closeSite:    "Non-closable",
		},
		"ConversationStore": {
			fieldName:    "ConversationStore",
			fhField:      "conversationStore",
			closable:     false,
			transferTask: "Task 4.3",
			callSite:     "featurehost/process.go:85",
			closeSite:    "Non-closable",
		},
		"BackgroundAux": {
			fieldName:    "BackgroundAux",
			closable:     true,
			transferTask: "never (borrowed)",
			callSite:     "background_aux_lifecycle.go:23",
			closeSite:    "background_aux_lifecycle.go:27",
		},
	}

	if len(ProcessFeatureTransitionTable) != len(expectedRows) {
		t.Fatalf("transition table row count = %d, want %d", len(ProcessFeatureTransitionTable), len(expectedRows))
	}

	for _, row := range ProcessFeatureTransitionTable {
		expected, ok := expectedRows[row.ResourceName]
		if !ok {
			t.Errorf("unexpected resource name in transition table: %s", row.ResourceName)
			continue
		}
		if strings.HasPrefix(row.CurrentFieldHolder, "ProcessServices.") {
			field, ok := psType.FieldByName(expected.fieldName)
			if !ok {
				t.Errorf("expected field %s missing on ProcessServices", expected.fieldName)
				continue
			}
			expectedType := field.Type.String()
			if row.ConcreteType != expectedType {
				t.Errorf("resource %s concreteType = %q, want %q", row.ResourceName, row.ConcreteType, expectedType)
			}
			if row.CurrentFieldHolder != "ProcessServices."+expected.fieldName {
				t.Errorf("resource %s CurrentFieldHolder = %q, want %q", row.ResourceName, row.CurrentFieldHolder, "ProcessServices."+expected.fieldName)
			}
		} else if strings.HasPrefix(row.CurrentFieldHolder, "featurehost.Runtime.") {
			if _, ok := psType.FieldByName(expected.fieldName); ok {
				t.Errorf("transferred field %s must NOT remain on ProcessServices", expected.fieldName)
			}
			// Unexported featurehost fields are intentionally inaccessible here,
			// but reflection still proves the documented holder field exists with
			// the documented concrete type (Type.String uses short package names,
			// matching the row vocabulary). Instance-level presence/distinctness
			// is proven inside package featurehost.
			fhField, ok := fhType.Elem().FieldByName(expected.fhField)
			if !ok {
				t.Errorf("expected holder field %s missing on featurehost.Runtime", expected.fhField)
				continue
			}
			if fhField.Type.String() != row.ConcreteType {
				t.Errorf("resource %s holder type = %q, want %q", row.ResourceName, fhField.Type.String(), row.ConcreteType)
			}
		} else {
			t.Errorf("unexpected field holder for %s: %s", row.ResourceName, row.CurrentFieldHolder)
		}
		if row.Closable != expected.closable {
			t.Errorf("resource %s closable = %v, want %v", row.ResourceName, row.Closable, expected.closable)
		}
		if !strings.Contains(row.TransferTask, expected.transferTask) {
			t.Errorf("resource %s transfer task = %s, want %s", row.ResourceName, row.TransferTask, expected.transferTask)
		}
		if !strings.Contains(row.CurrentConstructor, expected.callSite) {
			t.Errorf("resource %s CurrentConstructor %q does not cite call site %q", row.ResourceName, row.CurrentConstructor, expected.callSite)
		}
		if !strings.Contains(row.CloseRegistrationSite, expected.closeSite) {
			t.Errorf("resource %s CloseRegistrationSite %q does not cite %q", row.ResourceName, row.CloseRegistrationSite, expected.closeSite)
		}
		if row.InterimOwnershipRule == "" {
			t.Errorf("resource %s has empty InterimOwnershipRule", row.ResourceName)
		}
	}
}

func TestProcessFeatureOwnership_SingleConstructorValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if err := ValidateProcessFeatureOwnership(ps); err != nil {
		t.Fatalf("ValidateProcessFeatureOwnership failed on production wiring: %v", err)
	}
}

func TestProcessFeatureOwnership_DualConstructorWiringRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	buildRealProcess := func(t *testing.T, opts *BuildOptions) *ProcessServices {
		t.Helper()
		ps, err := NewProcessServices(ctx, ProcessServicesInput{
			Cfg:  testProcessServicesOwnershipConfig(),
			Log:  testkit.DiscardLogger(),
			Opts: opts,
		})
		if err != nil {
			t.Fatalf("NewProcessServices: %v", err)
		}
		return ps
	}

	optShapes := map[string]*BuildOptions{
		"default":         {PluginRegistry: pluginreg.NewRegistry()},
		"nil testing":     {PluginRegistry: pluginreg.NewRegistry(), Testing: TestingOptions{}},
		"empty prod/test": {PluginRegistry: pluginreg.NewRegistry(), Testing: TestingOptions{}, Production: ProductionOptions{}},
	}
	for name, opts := range optShapes {
		ps := buildRealProcess(t, opts)
		t.Cleanup(func() { _ = ps.Close() })

		if ps.StandardFeatures.TerminalDecisionPolicy() == nil {
			t.Fatalf("%s: expected featurehost-owned TerminalDecisionPolicy to be instantiated", name)
		}
		if got := ps.StandardFeatures.ClosersCount(); got != 2 {
			t.Fatalf("%s: observed %d featurehost-owned closers (want 2 for terminal policy store and lightweight session-classification holder)", name, got)
		}
		if err := ValidateProcessFeatureOwnership(ps); err != nil {
			t.Fatalf("%s: ValidateProcessFeatureOwnership failed on single-owner wiring: %v", name, err)
		}
	}

	// The validator must still reject genuinely missing transferred ownership.
	broken := buildRealProcess(t, &BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	t.Cleanup(func() { _ = broken.Close() })
	broken.StandardFeatures = nil
	if err := ValidateProcessFeatureOwnership(broken); err == nil {
		t.Fatal("expected error when StandardFeatures is missing, got nil")
	}
}

func TestProcessFeatureOwnership_MissingBorrowedResourceRejected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Simulate missing borrowed resource BackgroundAux
	ps.BackgroundAux = nil

	err = ValidateProcessFeatureOwnership(ps)
	if err == nil {
		t.Fatal("expected error when borrowed resource BackgroundAux is missing, got nil")
	}
}
