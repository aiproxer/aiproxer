package testcost

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestMeasurementJSONSchemaV1(t *testing.T) {
	t.Parallel()

	measurement := Measurement{
		SchemaVersion: SchemaVersion, Target: TargetTestUnit, Revision: "abc123", GOOS: "windows", GOARCH: "amd64", GoVersion: "go1.26.6", LogicalCPUs: 16, TestParallel: 8, WallNanos: 900,
		Process:  ProcessMetrics{UserCPUNanos: 1, KernelCPUNanos: 2, TotalCPUNanos: 3, TotalProcesses: 4, ActiveProcesses: 5, TerminatedProcesses: 6, PageFaults: 7, ReadOperations: 8, WriteOperations: 9, OtherOperations: 10, ReadBytes: 11, WriteBytes: 12, OtherBytes: 13},
		Packages: map[string]PackageMetrics{"example/pkg": {ElapsedNanos: 125}},
	}
	data, err := json.Marshal(measurement)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schema_version", "target", "revision", "goos", "goarch", "go_version", "logical_cpus", "test_parallel", "wall_nanos", "process", "packages"} {
		if _, ok := got[field]; !ok {
			t.Errorf("measurement missing top-level field %q: %s", field, data)
		}
	}
	process, ok := got["process"].(map[string]any)
	if !ok {
		t.Fatalf("process JSON = %#v", got["process"])
	}
	for _, field := range []string{"user_cpu_nanos", "kernel_cpu_nanos", "total_cpu_nanos", "total_processes", "active_processes", "terminated_processes", "page_faults", "read_operations", "write_operations", "other_operations", "read_bytes", "write_bytes", "other_bytes"} {
		if _, ok := process[field]; !ok {
			t.Errorf("process missing field %q: %s", field, data)
		}
	}
}

func TestQualityMeasurementOmitsPackages(t *testing.T) {
	t.Parallel()

	measurement := Measurement{SchemaVersion: SchemaVersion, Target: TargetQualityChecks, Packages: nil}
	data, err := json.Marshal(measurement)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["packages"]; ok {
		t.Fatalf("quality-checks measurement must omit packages: %s", data)
	}
}

func TestPolicyValidationRequiresExactSchemaAnchorAndTarget(t *testing.T) {
	t.Parallel()

	valid := Policy{SchemaVersion: SchemaVersion, AnchorRef: "origin/main", Targets: map[string]TargetPolicy{TargetTestUnit: {CPU: AbsoluteBudget{Ratio: 1.1, DeltaSeconds: 1}, Processes: ProcessBudget{Ratio: 1, Delta: 1}, IOOperations: CountBudget{Ratio: 1.1, Delta: 1}, Wall: AbsoluteBudget{Ratio: 1.1, DeltaSeconds: 1}, Package: PackagePolicy{ExistingRatio: 1, ExistingDeltaSeconds: 1, ExistingFloorSeconds: 1, NewWarnSeconds: 4, NewFailSeconds: 8}}}}
	if err := ValidatePolicy(valid); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		p    Policy
		want error
	}{
		{name: "unknown schema", p: Policy{SchemaVersion: 2, AnchorRef: "origin/main", Targets: valid.Targets}, want: ErrUnknownSchema},
		{name: "missing anchor", p: Policy{SchemaVersion: SchemaVersion, Targets: valid.Targets}, want: ErrInvalidPolicy},
		{name: "missing targets", p: Policy{SchemaVersion: SchemaVersion, AnchorRef: "origin/main"}, want: ErrInvalidPolicy},
		{name: "invalid ratio", p: Policy{SchemaVersion: SchemaVersion, AnchorRef: "origin/main", Targets: map[string]TargetPolicy{TargetTestUnit: {CPU: AbsoluteBudget{Ratio: math.NaN()}}}}, want: ErrInvalidPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePolicy(tc.p); !errors.Is(err, tc.want) {
				t.Fatalf("ValidatePolicy() error = %v, want errors.Is(..., %v)", err, tc.want)
			}
		})
	}
}

func TestPolicyJSONUsesDirectTargetShape(t *testing.T) {
	t.Parallel()

	data := []byte(`{"schema_version":1,"anchor_ref":"origin/main","targets":{"test-unit":{"cpu":{"ratio":1.1,"delta_seconds":1},"processes":{"ratio":1.2,"delta":2},"io_operations":{"ratio":1.4,"delta":10000},"wall":{"ratio":1.1,"delta_seconds":1},"packages":{"existing_ratio":1.1,"existing_delta_seconds":1,"existing_floor_seconds":2,"new_warn_seconds":4,"new_fail_seconds":8},"package_overrides":{"internal/archtest":{"existing_ratio":2}}}}}`)
	policy, err := DecodePolicy(data)
	if err != nil {
		t.Fatalf("DecodePolicy() error = %v", err)
	}
	target := policy.Targets[TargetTestUnit]
	if target.CPU.Ratio != 1.1 || target.Processes.Delta != 2 || target.IOOperations.Delta != 10000 || target.Package.ExistingFloorSeconds != 2 || target.PackageOverrides["internal/archtest"].ExistingRatio != 2 {
		t.Fatalf("direct policy target = %#v", target)
	}
	roundTrip, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(roundTrip, []byte(`"overall"`)) || bytes.Contains(roundTrip, []byte(`"package":`)) {
		t.Fatalf("policy round trip retained wrapper shape: %s", roundTrip)
	}
}

func TestDecodeRejectsUnknownSchemaAndMalformedTrailingJSON(t *testing.T) {
	t.Parallel()

	valid := `{"schema_version":1,"target":"test-unit","revision":"r","goos":"windows","goarch":"amd64","go_version":"go1.26.6","logical_cpus":1,"test_parallel":1,"wall_nanos":1,"process":{},"packages":{}}`
	if _, err := DecodeMeasurement([]byte(valid + " {}")); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("trailing measurement JSON error = %v", err)
	}
	if _, err := DecodeMeasurement([]byte(`{"schema_version":99,"target":"test-unit"}`)); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("unknown measurement schema error = %v", err)
	}
	if _, err := DecodePolicy([]byte(`{"schema_version":1,"anchor_ref":"r","targets":{"test-unit":{}},}`)); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("malformed policy error = %v", err)
	}
}
