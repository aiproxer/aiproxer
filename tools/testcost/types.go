// Package testcost contains the versioned test-cost measurement and ratchet
// policy used by the repository's Windows quality gate.
package testcost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const SchemaVersion = 1

var (
	ErrMissingTarget         = errors.New("testcost: target is required")
	ErrUnknownSchema         = errors.New("testcost: unknown schema version")
	ErrInvalidPolicy         = errors.New("testcost: invalid policy")
	ErrMalformedJSON         = errors.New("testcost: malformed JSON")
	ErrAccountingUnsupported = errors.New("testcost: process accounting is unsupported")
	ErrAccountingFailure     = errors.New("testcost: process accounting failed")
	ErrMeasurementFailed     = errors.New("testcost: measurement command failed")
	ErrWindowsOnly           = errors.New("testcost: measurement is Windows-only")
	ErrUnsupportedTarget     = errors.New("testcost: unsupported target")
	ErrMissingTempRoot       = errors.New("testcost: temp root is required")
)

// Measurement is the stable v1 JSON schema. Package elapsed values are
// diagnostics; WallNanos is the process wall time and is never their sum.
type Measurement struct {
	SchemaVersion int                       `json:"schema_version"`
	Target        string                    `json:"target"`
	Revision      string                    `json:"revision"`
	GOOS          string                    `json:"goos"`
	GOARCH        string                    `json:"goarch"`
	GoVersion     string                    `json:"go_version"`
	LogicalCPUs   int                       `json:"logical_cpus"`
	TestParallel  int                       `json:"test_parallel"`
	WallNanos     uint64                    `json:"wall_nanos"`
	Process       ProcessMetrics            `json:"process"`
	Packages      map[string]PackageMetrics `json:"packages,omitempty"`
}

type ProcessMetrics struct {
	UserCPUNanos        uint64 `json:"user_cpu_nanos"`
	KernelCPUNanos      uint64 `json:"kernel_cpu_nanos"`
	TotalCPUNanos       uint64 `json:"total_cpu_nanos"`
	TotalProcesses      uint64 `json:"total_processes"`
	ActiveProcesses     uint64 `json:"active_processes"`
	TerminatedProcesses uint64 `json:"terminated_processes"`
	PageFaults          uint64 `json:"page_faults"`
	ReadOperations      uint64 `json:"read_operations"`
	WriteOperations     uint64 `json:"write_operations"`
	OtherOperations     uint64 `json:"other_operations"`
	ReadBytes           uint64 `json:"read_bytes"`
	WriteBytes          uint64 `json:"write_bytes"`
	OtherBytes          uint64 `json:"other_bytes"`
}

type ProcessAccounting = ProcessMetrics

type PackageMetrics struct {
	ElapsedNanos uint64 `json:"elapsed_nanos"`
}

type PackageMeasurement = PackageMetrics

type CostMeasurement = Measurement

// Snapshot is retained as an API-compatible name for callers of the initial
// package draft.
type Snapshot = Measurement

// AbsoluteBudget uses a ratio multiplier and an absolute delta. The effective
// limit is max(anchor*ratio, anchor+delta).
type AbsoluteBudget struct {
	Ratio        float64 `json:"ratio"`
	DeltaSeconds float64 `json:"delta_seconds"`
}

type ProcessBudget struct {
	Ratio float64 `json:"ratio"`
	Delta uint64  `json:"delta"`
}

type CountBudget struct {
	Ratio float64 `json:"ratio"`
	Delta uint64  `json:"delta"`
}

// OverallPolicy is a convenience value for callers that build policies in Go;
// TargetPolicy stores the same fields directly in the v1 JSON shape.
type OverallPolicy struct {
	CPU          AbsoluteBudget
	Processes    ProcessBudget
	IOOperations CountBudget
	Wall         AbsoluteBudget
}

type PackagePolicy struct {
	ExistingRatio        float64 `json:"existing_ratio,omitempty"`
	ExistingDeltaSeconds float64 `json:"existing_delta_seconds,omitempty"`
	ExistingFloorSeconds float64 `json:"existing_floor_seconds,omitempty"`
	NewWarnSeconds       float64 `json:"new_warn_seconds,omitempty"`
	NewFailSeconds       float64 `json:"new_fail_seconds,omitempty"`
	present              map[string]bool
}

func (p *PackagePolicy) UnmarshalJSON(data []byte) error {
	type plain PackagePolicy
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*p = PackagePolicy(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	p.present = make(map[string]bool, len(fields))
	for name := range fields {
		p.present[name] = true
	}
	return nil
}

type TargetPolicy struct {
	CPU              AbsoluteBudget           `json:"cpu"`
	Processes        ProcessBudget            `json:"processes"`
	IOOperations     CountBudget              `json:"io_operations"`
	Wall             AbsoluteBudget           `json:"wall"`
	Package          PackagePolicy            `json:"packages"`
	Packages         PackagePolicy            `json:"-"`
	PackageOverrides map[string]PackagePolicy `json:"package_overrides,omitempty"`
}

// Policy intentionally contains only the three schema-level fields. Override
// authorization is supplied by CompareOptions or the CLI flag, not persisted
// in policy JSON.
type Policy struct {
	SchemaVersion int                     `json:"schema_version"`
	AnchorRef     string                  `json:"anchor_ref"`
	Targets       map[string]TargetPolicy `json:"targets"`
	AllowOverride bool                    `json:"-"`
}

type Violation struct {
	Package  string  `json:"package,omitempty"`
	Metric   string  `json:"metric"`
	Baseline uint64  `json:"baseline"`
	Current  uint64  `json:"current"`
	Delta    uint64  `json:"delta"`
	Ratio    float64 `json:"ratio"`
	Allowed  uint64  `json:"allowed"`
}

type Warning struct {
	Package      string `json:"package"`
	ElapsedNanos uint64 `json:"elapsed_nanos"`
	WarnNanos    uint64 `json:"warn_nanos"`
	FailNanos    uint64 `json:"fail_nanos"`
}

type PackageDelta struct {
	Package              string `json:"package"`
	AnchorElapsedNanos   uint64 `json:"anchor_elapsed_nanos"`
	HeadElapsedNanos     uint64 `json:"head_elapsed_nanos"`
	DeltaNanos           uint64 `json:"delta_nanos"`
	BaselineElapsedNanos uint64 `json:"-"`
	CurrentElapsedNanos  uint64 `json:"-"`
}

func (p PackageDelta) MarshalJSON() ([]byte, error) {
	anchor, head := p.AnchorElapsedNanos, p.HeadElapsedNanos
	if anchor == 0 {
		anchor = p.BaselineElapsedNanos
	}
	if head == 0 {
		head = p.CurrentElapsedNanos
	}
	type wire struct {
		Package            string `json:"package"`
		AnchorElapsedNanos uint64 `json:"anchor_elapsed_nanos"`
		HeadElapsedNanos   uint64 `json:"head_elapsed_nanos"`
		DeltaNanos         uint64 `json:"delta_nanos"`
	}
	return json.Marshal(wire{Package: p.Package, AnchorElapsedNanos: anchor, HeadElapsedNanos: head, DeltaNanos: p.DeltaNanos})
}

func (p *PackageDelta) UnmarshalJSON(data []byte) error {
	type wire struct {
		Package            string `json:"package"`
		AnchorElapsedNanos uint64 `json:"anchor_elapsed_nanos"`
		HeadElapsedNanos   uint64 `json:"head_elapsed_nanos"`
		DeltaNanos         uint64 `json:"delta_nanos"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = PackageDelta{Package: value.Package, AnchorElapsedNanos: value.AnchorElapsedNanos, HeadElapsedNanos: value.HeadElapsedNanos, BaselineElapsedNanos: value.AnchorElapsedNanos, CurrentElapsedNanos: value.HeadElapsedNanos, DeltaNanos: value.DeltaNanos}
	return nil
}

type OverallComparison struct {
	Metric string `json:"metric"`
	Anchor uint64 `json:"anchor"`
	Head   uint64 `json:"head"`
	Delta  uint64 `json:"delta"`
	Limit  uint64 `json:"limit"`
}

type Report struct {
	SchemaVersion int                 `json:"schema_version"`
	Target        string              `json:"target"`
	Passed        bool                `json:"passed"`
	Overridden    bool                `json:"overridden"`
	Violations    []Violation         `json:"violations"`
	Warnings      []Warning           `json:"warnings"`
	Overall       []OverallComparison `json:"overall,omitempty"`
	TopDeltas     []PackageDelta      `json:"top_deltas,omitempty"`
}

func (r Report) ExitCode() int {
	if r.Passed {
		return 0
	}
	return 1
}

type CompareOptions struct {
	AllowOverride bool
}

func ValidateMeasurement(m Measurement) error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnknownSchema, m.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(m.Target) == "" {
		return ErrMissingTarget
	}
	for name := range m.Packages {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: empty package name", ErrMalformedJSON)
		}
	}
	return nil
}

func ValidatePolicy(p Policy) error {
	if p.SchemaVersion != SchemaVersion {
		if p.SchemaVersion == 0 {
			return fmt.Errorf("%w: schema_version is required", ErrInvalidPolicy)
		}
		return fmt.Errorf("%w: got %d, want %d", ErrUnknownSchema, p.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(p.AnchorRef) == "" {
		return fmt.Errorf("%w: anchor_ref is required", ErrInvalidPolicy)
	}
	if len(p.Targets) == 0 {
		return fmt.Errorf("%w: targets are required", ErrInvalidPolicy)
	}
	for target, policy := range p.Targets {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("%w: target name is blank", ErrInvalidPolicy)
		}
		if err := validateOverall(OverallPolicy{CPU: policy.CPU, Processes: policy.Processes, IOOperations: policy.IOOperations, Wall: policy.Wall}); err != nil {
			return fmt.Errorf("%w for target %q", err, target)
		}
		packagePolicy := policy.Package
		if !packagePolicy.presentValue("existing_ratio") && !packagePolicy.presentValue("existing_delta_seconds") && !packagePolicy.presentValue("existing_floor_seconds") && !packagePolicy.presentValue("new_warn_seconds") && !packagePolicy.presentValue("new_fail_seconds") {
			packagePolicy = policy.Packages
		}
		if err := validatePackagePolicy(packagePolicy); err != nil {
			return fmt.Errorf("%w for target %q", err, target)
		}
		for name, override := range policy.PackageOverrides {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("%w: blank package override", ErrInvalidPolicy)
			}
			if err := validatePackagePolicy(override); err != nil {
				return fmt.Errorf("%w for package %q", err, name)
			}
		}
	}
	return nil
}

func validateOverall(p OverallPolicy) error {
	for name, budget := range map[string]AbsoluteBudget{"cpu": p.CPU, "wall": p.Wall} {
		if math.IsNaN(budget.Ratio) || math.IsInf(budget.Ratio, 0) || budget.Ratio < 0 {
			return fmt.Errorf("%w: %s ratio must be finite and non-negative", ErrInvalidPolicy, name)
		}
		if budget.Ratio != 0 && budget.Ratio < 1 {
			return fmt.Errorf("%w: %s ratio must be zero or at least one", ErrInvalidPolicy, name)
		}
		if math.IsNaN(budget.DeltaSeconds) || math.IsInf(budget.DeltaSeconds, 0) || budget.DeltaSeconds < 0 {
			return fmt.Errorf("%w: %s delta_seconds must be finite and non-negative", ErrInvalidPolicy, name)
		}
	}
	if math.IsNaN(p.Processes.Ratio) || math.IsInf(p.Processes.Ratio, 0) || p.Processes.Ratio < 0 || p.Processes.Ratio != 0 && p.Processes.Ratio < 1 {
		return fmt.Errorf("%w: processes ratio must be zero or at least one", ErrInvalidPolicy)
	}
	if math.IsNaN(p.IOOperations.Ratio) || math.IsInf(p.IOOperations.Ratio, 0) || p.IOOperations.Ratio < 0 || p.IOOperations.Ratio != 0 && p.IOOperations.Ratio < 1 {
		return fmt.Errorf("%w: io_operations ratio must be zero or at least one", ErrInvalidPolicy)
	}
	return nil
}

func validatePackagePolicy(p PackagePolicy) error {
	for name, value := range map[string]float64{"existing_ratio": p.ExistingRatio, "existing_delta_seconds": p.ExistingDeltaSeconds, "existing_floor_seconds": p.ExistingFloorSeconds, "new_warn_seconds": p.NewWarnSeconds, "new_fail_seconds": p.NewFailSeconds} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("%w: %s must be finite and non-negative", ErrInvalidPolicy, name)
		}
	}
	if p.ExistingRatio != 0 && p.ExistingRatio < 1 {
		return fmt.Errorf("%w: existing_ratio must be zero or at least one", ErrInvalidPolicy)
	}
	if p.NewFailSeconds != 0 && p.NewWarnSeconds > p.NewFailSeconds {
		return fmt.Errorf("%w: new_warn_seconds exceeds new_fail_seconds", ErrInvalidPolicy)
	}
	return nil
}

func DecodeMeasurement(data []byte) (Measurement, error) {
	var measurement Measurement
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&measurement); err != nil {
		return Measurement{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if err := requireEOF(decoder, ErrMalformedJSON); err != nil {
		return Measurement{}, err
	}
	if err := ValidateMeasurement(measurement); err != nil {
		return Measurement{}, err
	}
	return measurement, nil
}

func EncodeMeasurement(measurement Measurement) ([]byte, error) {
	if err := ValidateMeasurement(measurement); err != nil {
		return nil, err
	}
	return json.MarshalIndent(measurement, "", "  ")
}

func DecodePolicy(data []byte) (Policy, error) {
	var policy Policy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("%w: %v", ErrMalformedJSON, err)
	}
	if err := requireEOF(decoder, ErrMalformedJSON); err != nil {
		return Policy{}, err
	}
	if err := ValidatePolicy(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func EncodePolicy(policy Policy) ([]byte, error) {
	if err := ValidatePolicy(policy); err != nil {
		return nil, err
	}
	return json.MarshalIndent(policy, "", "  ")
}

func EncodeReport(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

func requireEOF(decoder *json.Decoder, class error) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", class)
		}
		return fmt.Errorf("%w: trailing JSON: %v", class, err)
	}
	return nil
}
