package archtest

import (
	"strings"
	"testing"
)

// TestForbiddenMirrorPredicate_DiagnosticsArms verifies that switch/if arms in
// diagnostics keyed to specific plane fields are rejected, while catalog iteration passes.
func TestForbiddenMirrorPredicate_DiagnosticsArms(t *testing.T) {
	t.Parallel()

	// 1. If statement on plane field is rejected
	forbiddenIfSrc := `package diag
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func stageOccupancyFromBundle(b aipfeature.FeatureBundle) []InventoryStageOccupancy {
	out := make([]InventoryStageOccupancy, 0, 4)
	if n := len(b.ToolCatalogFilters); n > 0 {
		out = append(out, InventoryStageOccupancy{Count: n})
	}
	return out
}
`
	findingsIf := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenIfSrc, Wave5c_Residual)
	if len(findingsIf) == 0 {
		t.Fatalf("expected forbidden diagnostics arm finding for ToolCatalogFilters at Wave5c (if stmt)")
	}
	if findingsIf[0].ShapeKind != MirrorDiagArm || findingsIf[0].PlaneID != "tool_catalog_filters" {
		t.Fatalf("unexpected finding for if stmt: %+v", findingsIf[0])
	}
	// At earlier waves (Wave1-Wave5b), diagnostics field reads from FeatureBundle are allowed until Wave5c
	findingsIfW1 := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenIfSrc, Wave1_HookBus)
	if len(findingsIfW1) != 0 {
		t.Fatalf("expected 0 findings at Wave1 for diagnostics arm, got %+v", findingsIfW1)
	}

	// 2. Switch statement arm on plane field is rejected
	forbiddenSwitchSrc := `package diag
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func stageOccupancyFromBundleSwitch(b aipfeature.FeatureBundle, stage string) int {
	switch stage {
	case "tools":
		return len(b.ToolCatalogFilters)
	default:
		return 0
	}
}
`
	findingsSwitch := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", forbiddenSwitchSrc, Wave5c_Residual)
	if len(findingsSwitch) == 0 {
		t.Fatalf("expected forbidden diagnostics switch arm finding for ToolCatalogFilters at Wave5c (switch stmt)")
	}
	if findingsSwitch[0].ShapeKind != MirrorDiagArm || findingsSwitch[0].PlaneID != "tool_catalog_filters" {
		t.Fatalf("unexpected finding for switch stmt: %+v", findingsSwitch[0])
	}

	// 3. Catalog iteration passes
	allowedSrc := `package diag
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func stageOccupancyFromFrozen(frozen aipfeature.FrozenPlaneSet) []InventoryStageOccupancy {
	out := make([]InventoryStageOccupancy, 0)
	for _, p := range aipfeature.StandardPlanes {
		out = append(out, InventoryStageOccupancy{Count: 1})
	}
	return out
}
`
	allowedFindings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", allowedSrc, Wave5c_Residual)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for catalog iteration diagnostics, got %+v", allowedFindings)
	}
}

func TestForbiddenMirrorPredicate_SessionClassifierDiagnosticIDBranch(t *testing.T) {
	t.Parallel()

	src := `package diag

func stageOccupancyFromClassifierID(planeID string) bool {
	switch planeID {
	case "session_classifier":
		return true
	default:
		return false
	}
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", src, Wave5c_Residual)
	if len(findings) == 0 {
		t.Fatal("expected classifier plane-ID diagnostic branch to be rejected")
	}
	if findings[0].ShapeKind != MirrorDiagArm || findings[0].PlaneID != "session_classifier" {
		t.Fatalf("unexpected classifier diagnostic branch finding: %+v", findings[0])
	}
}

// TestForbiddenMirrorPredicate_StageConsumers verifies that stage consumer accessors
// outside AllowedStageConsumers or failing thin-delegation are rejected past their wave,
// while whitelisted thin-delegating stage consumers pass.
func TestForbiddenMirrorPredicate_StageConsumers(t *testing.T) {
	t.Parallel()

	// 1. Allowed explicit stage consumer in internal/core/extensions delegating to Get passes at Wave3
	allowedConsumerSrc := `package extensions
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	return aipfeature.Get(s.frozen, aipfeature.PlaneCompletionGates)
}
`
	allowedFindings := scanSyntheticSource(t, "internal/core/extensions/seam_views.go", allowedConsumerSrc, Wave3_RequestShaping)
	if len(allowedFindings) != 0 {
		t.Fatalf("expected 0 findings for allowed thin-delegating stage consumer, got %+v", allowedFindings)
	}

	// 2. Unregistered/non-whitelisted stage consumer is rejected at Wave3
	unregisteredConsumerSrc := `package custom
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type UnregisteredConsumer struct {
	frozen aipfeature.FrozenPlaneSet
}

func (u *UnregisteredConsumer) CompletionGates() []completion.Gate {
	return aipfeature.Get(u.frozen, aipfeature.PlaneCompletionGates)
}
`
	unregisteredFindings := scanSyntheticSource(t, "internal/custom/consumer.go", unregisteredConsumerSrc, Wave3_RequestShaping)
	if len(unregisteredFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for non-whitelisted symbol at Wave3")
	}
	if unregisteredFindings[0].ShapeKind != MirrorStageConsumer || unregisteredFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding: %+v", unregisteredFindings[0])
	}
	if !strings.Contains(unregisteredFindings[0].Detail, "not in AllowedStageConsumers allowlist") {
		t.Fatalf("expected detail to mention AllowedStageConsumers, got %q", unregisteredFindings[0].Detail)
	}

	// 3. Whitelisted stage consumer that is not a thin delegate (e.g. invalid condition / branching) is rejected at Wave3
	nonThinConsumerSrc := `package extensions
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	if s != nil {
		return nil
	}
	return aipfeature.Get(s.frozen, aipfeature.PlaneCompletionGates)
}
`
	nonThinFindings := scanSyntheticSource(t, "internal/core/extensions/seam_views.go", nonThinConsumerSrc, Wave3_RequestShaping)
	if len(nonThinFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for non-thin delegate at Wave3")
	}
	if nonThinFindings[0].ShapeKind != MirrorStageConsumer || nonThinFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding: %+v", nonThinFindings[0])
	}
	if !strings.Contains(nonThinFindings[0].Detail, "does not thinly delegate to Get") {
		t.Fatalf("expected detail to mention non-thin delegation, got %q", nonThinFindings[0].Detail)
	}

	// 3b. Whitelisted stage consumer with canonical receiver nil guard passes at Wave3
	nilSafeConsumerSrc := `package extensions
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type RequestRuntimeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	if s == nil {
		return nil
	}
	return aipfeature.Get(s.frozen, aipfeature.PlaneCompletionGates)
}
`
	nilSafeFindings := scanSyntheticSource(t, "internal/core/extensions/seam_views.go", nilSafeConsumerSrc, Wave3_RequestShaping)
	if len(nilSafeFindings) != 0 {
		t.Fatalf("expected 0 findings for nil-safe whitelisted stage consumer, got %+v", nilSafeFindings)
	}

	// 4. EvilCompletionGates calling aipfeature.Get outside whitelist is rejected at Wave3 (Finding 3 fixture a)
	evilSrc := `package custom
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type FakeSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func EvilCompletionGates(s *FakeSnapshot) []completion.Gate {
	return aipfeature.Get(s.frozen, aipfeature.PlaneCompletionGates)
}
`
	evilFindings := scanSyntheticSource(t, "internal/custom/evil.go", evilSrc, Wave3_RequestShaping)
	if len(evilFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for EvilCompletionGates at Wave3")
	}
	if evilFindings[0].ShapeKind != MirrorStageConsumer || evilFindings[0].PlaneID != "completion_gates" {
		t.Fatalf("unexpected finding for EvilCompletionGates: %+v", evilFindings[0])
	}
	if !strings.Contains(evilFindings[0].Detail, "not in AllowedStageConsumers allowlist") {
		t.Fatalf("expected detail to mention AllowedStageConsumers, got %q", evilFindings[0].Detail)
	}

	// 5. Singular named method TrafficObserver outside whitelist is rejected at Wave2
	evilTrafficSrc := `package custom
import (
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type CustomSnapshot struct {
	frozen aipfeature.FrozenPlaneSet
}

func (c *CustomSnapshot) TrafficObserver() traffic.Observer {
	return aipfeature.Get(c.frozen, aipfeature.PlaneTrafficObservers)
}
`
	evilTrafficFindings := scanSyntheticSource(t, "internal/custom/traffic.go", evilTrafficSrc, Wave2_Observers)
	if len(evilTrafficFindings) == 0 {
		t.Fatalf("expected forbidden stage consumer finding for CustomSnapshot.TrafficObserver at Wave2")
	}
	if evilTrafficFindings[0].ShapeKind != MirrorStageConsumer || evilTrafficFindings[0].PlaneID != "traffic_observers" {
		t.Fatalf("unexpected finding: %+v", evilTrafficFindings[0])
	}
}
