package testcost

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
)

func TestReportJSONIncludesFalseOverride(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Report{SchemaVersion: 1, Target: TargetTestUnit})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"overridden":false`) {
		t.Fatalf("report must make unauthorized status explicit: %s", encoded)
	}
}

func validPolicy(overall OverallPolicy, packagePolicy PackagePolicy, overrides map[string]PackagePolicy) Policy {
	return Policy{SchemaVersion: SchemaVersion, AnchorRef: "origin/main", Targets: map[string]TargetPolicy{TargetTestUnit: {CPU: overall.CPU, Processes: overall.Processes, IOOperations: overall.IOOperations, Wall: overall.Wall, Package: packagePolicy, PackageOverrides: overrides}}}
}

func validMeasurement(target string) Measurement {
	return Measurement{SchemaVersion: SchemaVersion, Target: target, Revision: "r", GOOS: "windows", GOARCH: "amd64", GoVersion: "go1.26.6", LogicalCPUs: 4, TestParallel: 4, Process: ProcessMetrics{}, Packages: map[string]PackageMetrics{}}
}

func TestCompareOverallUsesMultiplierOrAnchorDeltaAndProcessDelta(t *testing.T) {
	t.Parallel()

	baseline := validMeasurement(TargetTestUnit)
	baseline.WallNanos = 100
	baseline.Process = ProcessMetrics{TotalCPUNanos: 100, TotalProcesses: 10, ReadOperations: 100, WriteOperations: 0, OtherOperations: 0}
	current := baseline
	current.WallNanos = 131
	current.Process = ProcessMetrics{TotalCPUNanos: 131, TotalProcesses: 13, ReadOperations: 131}
	policy := validPolicy(OverallPolicy{
		CPU:          AbsoluteBudget{Ratio: 1.10, DeltaSeconds: 0.00000003},
		Processes:    ProcessBudget{Ratio: 1.2, Delta: 2},
		IOOperations: CountBudget{Ratio: 1.10, Delta: 30},
		Wall:         AbsoluteBudget{Ratio: 1.10, DeltaSeconds: 0.00000003},
	}, PackagePolicy{}, nil)
	report, err := Compare(baseline, current, policy)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if report.Passed || len(report.Violations) != 4 {
		t.Fatalf("expected CPU/process/io/wall violations, got %#v", report)
	}
	for _, violation := range report.Violations {
		if violation.Package != "" {
			t.Errorf("overall violation package = %q, want empty", violation.Package)
		}
	}
}

func TestComparePackageLifecycleWarningsFailuresFloorAndArchtestOverride(t *testing.T) {
	t.Parallel()

	baseline := validMeasurement(TargetTestUnit)
	baseline.Packages = map[string]PackageMetrics{
		"kept":              {ElapsedNanos: 10_000_000_000},
		"removed":           {ElapsedNanos: 10_000_000_000},
		"internal/archtest": {ElapsedNanos: 10_000_000_000},
		"override-merge":    {ElapsedNanos: 10_000_000_000},
		"small-kept":        {ElapsedNanos: 1_000_000_000},
	}
	current := baseline
	current.Packages = map[string]PackageMetrics{
		"kept":              {ElapsedNanos: 12_000_000_000},
		"new-warning":       {ElapsedNanos: 5_000_000_000},
		"new-failure":       {ElapsedNanos: 9_000_000_000},
		"below-floor":       {ElapsedNanos: 1_000_000_000},
		"internal/archtest": {ElapsedNanos: 15_000_000_000},
		"override-merge":    {ElapsedNanos: 15_000_000_000},
		"small-kept":        {ElapsedNanos: 3_000_000_000},
	}
	policy := validPolicy(OverallPolicy{}, PackagePolicy{ExistingRatio: 1, ExistingDeltaSeconds: 1, ExistingFloorSeconds: 2, NewWarnSeconds: 4, NewFailSeconds: 8}, map[string]PackagePolicy{
		"internal/archtest": {ExistingRatio: 2},
		"override-merge":    {ExistingDeltaSeconds: 3},
	})
	report, err := Compare(baseline, current, policy)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if report.Passed {
		t.Fatal("new-failure and kept package should fail")
	}
	if len(report.Warnings) != 2 {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
	if report.Warnings[0].Package != "new-failure" || report.Warnings[1].Package != "new-warning" {
		t.Fatalf("warnings are not deterministic: %#v", report.Warnings)
	}
	for _, violation := range report.Violations {
		if violation.Package == "removed" || violation.Package == "below-floor" || violation.Package == "internal/archtest" {
			t.Fatalf("unexpected lifecycle/floor/override violation: %#v", violation)
		}
	}
	foundSmall := false
	for _, violation := range report.Violations {
		if violation.Package == "small-kept" {
			foundSmall = true
		}
	}
	if !foundSmall {
		t.Fatal("existing package below floor must still be compared")
	}
	if len(report.TopDeltas) != 4 {
		t.Fatalf("top package deltas = %#v, want kept, archtest, override-merge, and small-kept", report.TopDeltas)
	}
}

func TestCompareTopFifteenPackageElapsedIncreases(t *testing.T) {
	t.Parallel()

	baseline := validMeasurement(TargetTestUnit)
	current := validMeasurement(TargetTestUnit)
	for i := range 20 {
		name := string(rune('a' + i))
		baseline.Packages[name] = PackageMetrics{ElapsedNanos: 1}
		current.Packages[name] = PackageMetrics{ElapsedNanos: uint64(i + 2)}
	}
	report, err := Compare(baseline, current, validPolicy(OverallPolicy{}, PackagePolicy{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.TopDeltas) != 15 || report.TopDeltas[0].DeltaNanos <= report.TopDeltas[14].DeltaNanos {
		t.Fatalf("top deltas = %#v", report.TopDeltas)
	}
}

func TestCompareAuthorizedOverrideRetainsViolationsAndPasses(t *testing.T) {
	t.Parallel()

	baseline := validMeasurement(TargetTestUnit)
	baseline.WallNanos = 1
	current := baseline
	current.WallNanos = 100
	policy := validPolicy(OverallPolicy{Wall: AbsoluteBudget{Ratio: 1, DeltaSeconds: 0.000000001}}, PackagePolicy{}, nil)
	report, err := CompareWithOptions(baseline, current, policy, CompareOptions{AllowOverride: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || !report.Overridden || len(report.Violations) != 1 {
		t.Fatalf("override report = %#v", report)
	}
}

func TestCompareFailsClosedForMissingTargetAndUnknownSchema(t *testing.T) {
	t.Parallel()

	base := validMeasurement(TargetTestUnit)
	current := base
	policy := validPolicy(OverallPolicy{}, PackagePolicy{}, nil)
	for _, tc := range []struct {
		name    string
		base    Measurement
		current Measurement
		want    error
	}{
		{name: "missing baseline target", base: Measurement{SchemaVersion: SchemaVersion}, current: current, want: ErrMissingTarget},
		{name: "unknown current schema", base: base, current: Measurement{SchemaVersion: 99, Target: TargetTestUnit}, want: ErrUnknownSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compare(tc.base, tc.current, policy)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Compare() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCompareQATaggedHotspotsSyntheticDriftSimulation(t *testing.T) {
	t.Parallel()

	budgetJSON := []byte(`{
		"schema_version": 1,
		"anchor_ref": "1b58ca4f5173734fc3b7b0c63059c4f10a09d335",
		"targets": {
			"qa-tagged-hotspots": {
				"cpu": { "ratio": 1.25, "delta_seconds": 10 },
				"processes": { "ratio": 1.15, "delta": 15 },
				"io_operations": { "ratio": 1.30, "delta": 50000 },
				"wall": { "ratio": 1.50, "delta_seconds": 15 },
				"packages": {
					"existing_ratio": 1.50,
					"existing_delta_seconds": 5,
					"existing_floor_seconds": 5,
					"new_warn_seconds": 4,
					"new_fail_seconds": 8
				},
				"package_overrides": {
					"github.com/aiproxer/aiproxer/internal/archtest": {
						"existing_ratio": 1.40,
						"existing_delta_seconds": 8
					},
					"github.com/aiproxer/aiproxer/internal/infra/runtimebundle": {
						"existing_ratio": 1.40,
						"existing_delta_seconds": 8
					},
					"github.com/aiproxer/aiproxer/tools/backendplugin": {
						"existing_ratio": 1.40,
						"existing_delta_seconds": 8
					},
					"github.com/aiproxer/aiproxer/tools/backendplugin/release_gates": {
						"existing_ratio": 1.40,
						"existing_delta_seconds": 8
					}
				}
			}
		}
	}`)

	policy, err := DecodePolicy(budgetJSON)
	if err != nil {
		t.Fatalf("DecodePolicy() failed: %v", err)
	}

	baseline := validMeasurement(TargetQATaggedHotspots)
	baseline.WallNanos = 45_381_029_900
	baseline.Process = ProcessMetrics{
		TotalCPUNanos:   314_031_250_000,
		TotalProcesses:  701,
		ReadOperations:  1_717_101,
		WriteOperations: 122_443,
		OtherOperations: 4_420_189,
	}
	baseline.Packages = map[string]PackageMetrics{
		"github.com/aiproxer/aiproxer/internal/archtest":                 {ElapsedNanos: 36_721_000_000},
		"github.com/aiproxer/aiproxer/internal/infra/runtimebundle":      {ElapsedNanos: 27_644_000_000},
		"github.com/aiproxer/aiproxer/tools/backendplugin":               {ElapsedNanos: 40_603_000_000},
		"github.com/aiproxer/aiproxer/tools/backendplugin/release_gates": {ElapsedNanos: 22_647_000_000},
	}

	t.Run("Observed same-code backendplugin noise passes", func(t *testing.T) {
		t.Parallel()

		currentObserved := validMeasurement(TargetQATaggedHotspots)
		currentObserved.WallNanos = 58_638_498_800
		currentObserved.Process = ProcessMetrics{
			TotalCPUNanos:   310_281_250_000,
			TotalProcesses:  698,
			ReadOperations:  1_717_186,
			WriteOperations: 122_212,
			OtherOperations: 4_418_934,
		}
		currentObserved.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/internal/archtest":                 {ElapsedNanos: 29_642_000_000},
			"github.com/aiproxer/aiproxer/internal/infra/runtimebundle":      {ElapsedNanos: 23_921_000_000},
			"github.com/aiproxer/aiproxer/tools/backendplugin":               {ElapsedNanos: 53_343_000_000}, // +31.4% noise (within 1.40 ratio)
			"github.com/aiproxer/aiproxer/tools/backendplugin/release_gates": {ElapsedNanos: 19_519_000_000},
		}

		report, err := Compare(baseline, currentObserved, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if !report.Passed || len(report.Violations) != 0 {
			t.Fatalf("observed noise expected to pass, got report: %#v", report)
		}
	})

	t.Run("Synthetic 26 percent CPU drift fails", func(t *testing.T) {
		t.Parallel()

		currentCPU26 := baseline
		currentCPU26.Process.TotalCPUNanos = uint64(float64(baseline.Process.TotalCPUNanos) * 1.26)

		report, err := Compare(baseline, currentCPU26, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "cpu" {
			t.Fatalf("+26%% CPU drift expected to fail on CPU metric, got report: %#v", report)
		}
	})

	t.Run("Synthetic 16 percent process drift fails", func(t *testing.T) {
		t.Parallel()

		currentProc16 := baseline
		currentProc16.Process.TotalProcesses = uint64(float64(baseline.Process.TotalProcesses) * 1.16)

		report, err := Compare(baseline, currentProc16, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "processes" {
			t.Fatalf("+16%% process drift expected to fail on processes metric, got report: %#v", report)
		}
	})

	t.Run("Material package drift at 50 percent fails for each hotspot individually", func(t *testing.T) {
		t.Parallel()

		for pkg, baseMetrics := range baseline.Packages {
			currentPkgDrift := baseline
			currentPkgDrift.Packages = maps.Clone(baseline.Packages)
			currentPkgDrift.Packages[pkg] = PackageMetrics{ElapsedNanos: uint64(float64(baseMetrics.ElapsedNanos) * 1.50)}

			report, err := Compare(baseline, currentPkgDrift, policy)
			if err != nil {
				t.Fatalf("Compare() error for %s = %v", pkg, err)
			}
			if report.Passed || len(report.Violations) != 1 || report.Violations[0].Package != pkg || report.Violations[0].Metric != "elapsed_nanos" {
				t.Fatalf("+50%% drift on %s expected to fail single package elapsed violation, got: %#v", pkg, report)
			}
		}
	})

	t.Run("Synthetic 50 percent package drift fails all overrides together", func(t *testing.T) {
		t.Parallel()

		currentPkg50 := baseline
		currentPkg50.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/internal/archtest":                 {ElapsedNanos: uint64(float64(baseline.Packages["github.com/aiproxer/aiproxer/internal/archtest"].ElapsedNanos) * 1.50)},
			"github.com/aiproxer/aiproxer/internal/infra/runtimebundle":      {ElapsedNanos: uint64(float64(baseline.Packages["github.com/aiproxer/aiproxer/internal/infra/runtimebundle"].ElapsedNanos) * 1.50)},
			"github.com/aiproxer/aiproxer/tools/backendplugin":               {ElapsedNanos: uint64(float64(baseline.Packages["github.com/aiproxer/aiproxer/tools/backendplugin"].ElapsedNanos) * 1.50)},
			"github.com/aiproxer/aiproxer/tools/backendplugin/release_gates": {ElapsedNanos: uint64(float64(baseline.Packages["github.com/aiproxer/aiproxer/tools/backendplugin/release_gates"].ElapsedNanos) * 1.50)},
		}

		report, err := Compare(baseline, currentPkg50, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 4 {
			t.Fatalf("expected 4 package override violations, got %#v", report)
		}
	})

	t.Run("New package over 8s fail limit fails with violation and warning", func(t *testing.T) {
		t.Parallel()

		currentNewPkg := baseline
		currentNewPkg.Packages = maps.Clone(baseline.Packages)
		currentNewPkg.Packages["github.com/aiproxer/aiproxer/tools/newhotspot"] = PackageMetrics{ElapsedNanos: 9_000_000_000}

		report, err := Compare(baseline, currentNewPkg, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "new_package_elapsed_nanos" || report.Violations[0].Package != "github.com/aiproxer/aiproxer/tools/newhotspot" {
			t.Fatalf("new package >8s expected new_package_elapsed_nanos violation, got: %#v", report)
		}
		if len(report.Warnings) != 1 || report.Warnings[0].Package != "github.com/aiproxer/aiproxer/tools/newhotspot" {
			t.Fatalf("new package >8s expected warning, got: %#v", report.Warnings)
		}
	})
}

func TestCompareTestUnitSyntheticDriftSimulation(t *testing.T) {
	t.Parallel()

	budgetJSON := []byte(`{
		"schema_version": 1,
		"anchor_ref": "6dbb831885341516117034923f0c3203373aded0",
		"targets": {
			"test-unit": {
				"cpu": { "ratio": 1.30, "delta_seconds": 10 },
				"processes": { "ratio": 1.15, "delta": 8 },
				"io_operations": { "ratio": 1.40, "delta": 10000 },
				"wall": { "ratio": 1.60, "delta_seconds": 15 },
				"packages": {
					"existing_ratio": 1.75,
					"existing_delta_seconds": 3,
					"existing_floor_seconds": 15,
					"new_warn_seconds": 4,
					"new_fail_seconds": 8
				},
				"package_overrides": {
					"github.com/aiproxer/aiproxer/internal/archtest": {
						"existing_ratio": 1.50,
						"existing_delta_seconds": 5
					},
					"github.com/aiproxer/aiproxer/internal/testkit/dbparity": {
						"existing_ratio": 2.25,
						"existing_delta_seconds": 13
					}
				}
			}
		}
	}`)

	policy, err := DecodePolicy(budgetJSON)
	if err != nil {
		t.Fatalf("DecodePolicy() failed: %v", err)
	}

	baseline := validMeasurement(TargetTestUnit)
	baseline.WallNanos = 103_643_592_500
	baseline.Process = ProcessMetrics{
		TotalCPUNanos:   740_203_125_000,
		TotalProcesses:  2133,
		ReadOperations:  1_500_000,
		WriteOperations: 100_000,
		OtherOperations: 6_633_582,
	}
	baseline.Packages = map[string]PackageMetrics{
		"github.com/aiproxer/aiproxer/tools/changesize":              {ElapsedNanos: 989_000_000},
		"github.com/aiproxer/aiproxer/pkg/aipruntime":                {ElapsedNanos: 7_607_000_000},
		"github.com/aiproxer/aiproxer/internal/testkit/scale":        {ElapsedNanos: 3_393_000_000},
		"github.com/aiproxer/aiproxer/internal/testkit/dbparity":     {ElapsedNanos: 10_961_000_000},
		"github.com/aiproxer/aiproxer/internal/testkit/dbparity/cmd": {ElapsedNanos: 21_200_000_000},
	}

	t.Run("Observed same-code noise (dbparity 23.3s, scale 13.2s, runtime 11.2s, changesize 5.3s) passes", func(t *testing.T) {
		t.Parallel()

		currentObserved := baseline
		currentObserved.WallNanos = 98_399_093_700
		currentObserved.Process = ProcessMetrics{
			TotalCPUNanos:   588_093_750_000,
			TotalProcesses:  1504,
			ReadOperations:  1_450_000,
			WriteOperations: 95_000,
			OtherOperations: 6_249_174,
		}
		currentObserved.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/tools/changesize":              {ElapsedNanos: 5_338_000_000},
			"github.com/aiproxer/aiproxer/pkg/aipruntime":                {ElapsedNanos: 11_187_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/scale":        {ElapsedNanos: 13_233_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/dbparity":     {ElapsedNanos: 23_302_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/dbparity/cmd": {ElapsedNanos: 27_323_000_000},
		}

		report, err := Compare(baseline, currentObserved, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if !report.Passed || len(report.Violations) != 0 {
			t.Fatalf("observed test-unit noise expected to pass, got report: %#v", report)
		}
	})

	t.Run("Existing low-anchor package over 15s floor fails", func(t *testing.T) {
		t.Parallel()

		currentOverFloor := baseline
		currentOverFloor.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/internal/testkit/scale":    {ElapsedNanos: 15_500_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/dbparity": {ElapsedNanos: 10_961_000_000},
		}

		report, err := Compare(baseline, currentOverFloor, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Package != "github.com/aiproxer/aiproxer/internal/testkit/scale" || report.Violations[0].Metric != "elapsed_nanos" || report.Violations[0].Allowed != 15_000_000_000 {
			t.Fatalf("package >15s expected elapsed_nanos violation with allowed=15s, got: %#v", report)
		}
	})

	t.Run("Material dbparity package drift at 30s fails", func(t *testing.T) {
		t.Parallel()

		currentDBParityDrift := baseline
		currentDBParityDrift.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/internal/testkit/scale":    {ElapsedNanos: 3_393_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/dbparity": {ElapsedNanos: 30_000_000_000},
		}

		report, err := Compare(baseline, currentDBParityDrift, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Package != "github.com/aiproxer/aiproxer/internal/testkit/dbparity" || report.Violations[0].Metric != "elapsed_nanos" {
			t.Fatalf("dbparity at 30s expected elapsed_nanos violation, got: %#v", report)
		}
	})

	t.Run("Synthetic 31 percent CPU drift fails", func(t *testing.T) {
		t.Parallel()

		currentCPU31 := baseline
		currentCPU31.Process.TotalCPUNanos = uint64(float64(baseline.Process.TotalCPUNanos) * 1.31)

		report, err := Compare(baseline, currentCPU31, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "cpu" {
			t.Fatalf("+31%% CPU drift expected to fail on CPU metric, got report: %#v", report)
		}
	})

	t.Run("Synthetic 16 percent process drift fails", func(t *testing.T) {
		t.Parallel()

		currentProc16 := baseline
		currentProc16.Process.TotalProcesses = uint64(float64(baseline.Process.TotalProcesses) * 1.16)

		report, err := Compare(baseline, currentProc16, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "processes" {
			t.Fatalf("+16%% process drift expected to fail on processes metric, got report: %#v", report)
		}
	})

	t.Run("New package over 8s fail limit fails with violation and warning", func(t *testing.T) {
		t.Parallel()

		currentNewPkg := baseline
		currentNewPkg.Packages = map[string]PackageMetrics{
			"github.com/aiproxer/aiproxer/tools/changesize":          {ElapsedNanos: 989_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/scale":    {ElapsedNanos: 3_393_000_000},
			"github.com/aiproxer/aiproxer/internal/testkit/dbparity": {ElapsedNanos: 10_961_000_000},
			"github.com/aiproxer/aiproxer/tools/newpackage":          {ElapsedNanos: 9_000_000_000},
		}

		report, err := Compare(baseline, currentNewPkg, policy)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if report.Passed || len(report.Violations) != 1 || report.Violations[0].Metric != "new_package_elapsed_nanos" || report.Violations[0].Package != "github.com/aiproxer/aiproxer/tools/newpackage" {
			t.Fatalf("new package >8s expected new_package_elapsed_nanos violation, got: %#v", report)
		}
		if len(report.Warnings) != 1 || report.Warnings[0].Package != "github.com/aiproxer/aiproxer/tools/newpackage" {
			t.Fatalf("new package >8s expected warning, got: %#v", report.Warnings)
		}
	})
}
