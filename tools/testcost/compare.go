package testcost

import (
	"fmt"
	"math"
	"sort"
)

const maxTopDeltas = 15

func Compare(baseline, current Measurement, policy Policy) (Report, error) {
	return CompareWithOptions(baseline, current, policy, CompareOptions{AllowOverride: policy.AllowOverride})
}

func CompareWithOptions(baseline, current Measurement, policy Policy, options CompareOptions) (Report, error) {
	if err := ValidateMeasurement(baseline); err != nil {
		return Report{}, err
	}
	if err := ValidateMeasurement(current); err != nil {
		return Report{}, err
	}
	if baseline.Target != current.Target {
		return Report{}, fmt.Errorf("%w: baseline=%q current=%q", ErrMissingTarget, baseline.Target, current.Target)
	}
	if err := ValidatePolicy(policy); err != nil {
		return Report{}, err
	}
	targetPolicy, ok := policy.Targets[current.Target]
	if !ok {
		return Report{}, fmt.Errorf("%w: policy has no target %q", ErrMissingTarget, current.Target)
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		Target:        current.Target,
		Passed:        true,
		Violations:    []Violation{},
		Warnings:      []Warning{},
	}
	compareOverall(baseline, current, OverallPolicy{CPU: targetPolicy.CPU, Processes: targetPolicy.Processes, IOOperations: targetPolicy.IOOperations, Wall: targetPolicy.Wall}, &report)
	packagePolicy := targetPolicy.Package
	if !packagePolicy.presentValue("existing_ratio") && !packagePolicy.presentValue("existing_delta_seconds") && !packagePolicy.presentValue("existing_floor_seconds") && !packagePolicy.presentValue("new_warn_seconds") && !packagePolicy.presentValue("new_fail_seconds") {
		packagePolicy = targetPolicy.Packages
	}
	comparePackages(baseline.Packages, current.Packages, packagePolicy, targetPolicy.PackageOverrides, &report)
	sort.Slice(report.Violations, func(i, j int) bool {
		if report.Violations[i].Package != report.Violations[j].Package {
			return report.Violations[i].Package < report.Violations[j].Package
		}
		return report.Violations[i].Metric < report.Violations[j].Metric
	})
	sort.Slice(report.Warnings, func(i, j int) bool { return report.Warnings[i].Package < report.Warnings[j].Package })
	sort.Slice(report.TopDeltas, func(i, j int) bool {
		if report.TopDeltas[i].DeltaNanos != report.TopDeltas[j].DeltaNanos {
			return report.TopDeltas[i].DeltaNanos > report.TopDeltas[j].DeltaNanos
		}
		return report.TopDeltas[i].Package < report.TopDeltas[j].Package
	})
	if len(report.TopDeltas) > maxTopDeltas {
		report.TopDeltas = report.TopDeltas[:maxTopDeltas]
	}
	if len(report.Violations) != 0 {
		report.Passed = options.AllowOverride
		report.Overridden = options.AllowOverride
	}
	return report, nil
}

func compareOverall(baseline, current Measurement, policy OverallPolicy, report *Report) {
	compareBudget("cpu", baseline.Process.TotalCPUNanos, current.Process.TotalCPUNanos, policy.CPU, report)
	compareProcessBudget(baseline.Process.TotalProcesses, current.Process.TotalProcesses, policy.Processes, report)
	baseIO := saturatingSum(baseline.Process.ReadOperations, baseline.Process.WriteOperations, baseline.Process.OtherOperations)
	currentIO := saturatingSum(current.Process.ReadOperations, current.Process.WriteOperations, current.Process.OtherOperations)
	compareCountBudget("io_operations", baseIO, currentIO, policy.IOOperations.Ratio, policy.IOOperations.Delta, report)
	compareBudget("wall", baseline.WallNanos, current.WallNanos, policy.Wall, report)
}

func compareBudget(metric string, baseline, current uint64, budget AbsoluteBudget, report *Report) {
	allowed := absoluteLimit(baseline, budget)
	delta := uint64(0)
	if current > baseline {
		delta = current - baseline
	}
	report.Overall = append(report.Overall, OverallComparison{Metric: metric, Anchor: baseline, Head: current, Delta: delta, Limit: allowed})
	if current <= baseline {
		return
	}
	if current > allowed {
		report.Violations = append(report.Violations, Violation{Metric: metric, Baseline: baseline, Current: current, Delta: delta, Ratio: multiplier(baseline, current), Allowed: allowed})
	}
}

func compareProcessBudget(baseline, current uint64, budget ProcessBudget, report *Report) {
	compareCountBudget("processes", baseline, current, budget.Ratio, budget.Delta, report)
}

func compareCountBudget(metric string, baseline, current uint64, ratio float64, absoluteDelta uint64, report *Report) {
	allowed := maxUint(saturatingAdd(baseline, absoluteDelta), ratioLimit(baseline, ratio))
	delta := uint64(0)
	if current > baseline {
		delta = current - baseline
	}
	report.Overall = append(report.Overall, OverallComparison{Metric: metric, Anchor: baseline, Head: current, Delta: delta, Limit: allowed})
	if current <= baseline {
		return
	}
	if current > allowed {
		report.Violations = append(report.Violations, Violation{Metric: metric, Baseline: baseline, Current: current, Delta: delta, Ratio: multiplier(baseline, current), Allowed: allowed})
	}
}

func comparePackages(baseline, current map[string]PackageMetrics, policy PackagePolicy, overrides map[string]PackagePolicy, report *Report) {
	for name, base := range baseline {
		now, exists := current[name]
		if !exists {
			continue
		}
		if now.ElapsedNanos > base.ElapsedNanos {
			report.TopDeltas = append(report.TopDeltas, PackageDelta{Package: name, AnchorElapsedNanos: base.ElapsedNanos, HeadElapsedNanos: now.ElapsedNanos, BaselineElapsedNanos: base.ElapsedNanos, CurrentElapsedNanos: now.ElapsedNanos, DeltaNanos: now.ElapsedNanos - base.ElapsedNanos})
		}
		packagePolicy := mergePackagePolicy(policy, overrides[name])
		floor := secondsToNanos(packagePolicy.ExistingFloorSeconds)
		ratio := packagePolicy.ExistingRatio
		if ratio == 0 {
			ratio = 1
		}
		allowed := maxUint(secondsToNanosFloat(float64(base.ElapsedNanos)/1e9*ratio), saturatingAdd(base.ElapsedNanos, secondsToNanos(packagePolicy.ExistingDeltaSeconds)))
		allowed = maxUint(allowed, floor)
		if now.ElapsedNanos > allowed {
			report.Violations = append(report.Violations, Violation{Package: name, Metric: "elapsed_nanos", Baseline: base.ElapsedNanos, Current: now.ElapsedNanos, Delta: now.ElapsedNanos - base.ElapsedNanos, Ratio: multiplier(base.ElapsedNanos, now.ElapsedNanos), Allowed: allowed})
		}
	}
	for name, now := range current {
		if _, exists := baseline[name]; exists {
			continue
		}
		packagePolicy := mergePackagePolicy(policy, overrides[name])
		warn := secondsToNanos(packagePolicy.NewWarnSeconds)
		fail := secondsToNanos(packagePolicy.NewFailSeconds)
		if warn == 0 {
			warn = 4 * 1_000_000_000
		}
		if fail == 0 {
			fail = 8 * 1_000_000_000
		}
		if now.ElapsedNanos > warn {
			report.Warnings = append(report.Warnings, Warning{Package: name, ElapsedNanos: now.ElapsedNanos, WarnNanos: warn, FailNanos: fail})
		}
		if now.ElapsedNanos > fail {
			report.Violations = append(report.Violations, Violation{Package: name, Metric: "new_package_elapsed_nanos", Current: now.ElapsedNanos, Delta: now.ElapsedNanos, Allowed: fail})
		}
	}
}

func mergePackagePolicy(base, override PackagePolicy) PackagePolicy {
	merged := base
	if override.presentValue("existing_ratio") {
		merged.ExistingRatio = override.ExistingRatio
	}
	if override.presentValue("existing_delta_seconds") {
		merged.ExistingDeltaSeconds = override.ExistingDeltaSeconds
	}
	if override.presentValue("existing_floor_seconds") {
		merged.ExistingFloorSeconds = override.ExistingFloorSeconds
	}
	if override.presentValue("new_warn_seconds") {
		merged.NewWarnSeconds = override.NewWarnSeconds
	}
	if override.presentValue("new_fail_seconds") {
		merged.NewFailSeconds = override.NewFailSeconds
	}
	return merged
}

func (p PackagePolicy) presentValue(name string) bool {
	if p.present != nil && p.present[name] {
		return true
	}
	switch name {
	case "existing_ratio":
		return p.ExistingRatio != 0
	case "existing_delta_seconds":
		return p.ExistingDeltaSeconds != 0
	case "existing_floor_seconds":
		return p.ExistingFloorSeconds != 0
	case "new_warn_seconds":
		return p.NewWarnSeconds != 0
	case "new_fail_seconds":
		return p.NewFailSeconds != 0
	default:
		return false
	}
}

func absoluteLimit(anchor uint64, budget AbsoluteBudget) uint64 {
	ratioLimit := uint64(0)
	if budget.Ratio > 0 {
		ratioValue := float64(anchor) * budget.Ratio
		if ratioValue >= float64(^uint64(0)) {
			ratioLimit = ^uint64(0)
		} else {
			ratioLimit = uint64(math.Floor(ratioValue))
		}
	}
	return maxUint(ratioLimit, saturatingAdd(anchor, secondsToNanos(budget.DeltaSeconds)))
}

func ratioLimit(anchor uint64, ratio float64) uint64 {
	if ratio <= 0 {
		return 0
	}
	value := float64(anchor) * ratio
	if value >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(math.Floor(value))
}

func secondsToNanos(seconds float64) uint64 {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return secondsToNanosFloat(seconds)
}

func secondsToNanosFloat(seconds float64) uint64 {
	value := seconds * 1e9
	if value >= float64(^uint64(0)) {
		return ^uint64(0)
	}
	return uint64(math.Round(value))
}

func multiplier(base, current uint64) float64 {
	if base == 0 {
		return 0
	}
	return float64(current) / float64(base)
}

func maxUint(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func saturatingAdd(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

func saturatingSum(values ...uint64) uint64 {
	total := uint64(0)
	for _, value := range values {
		total = saturatingAdd(total, value)
	}
	return total
}
