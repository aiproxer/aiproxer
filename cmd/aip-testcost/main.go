package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aiproxer/aiproxer/tools/testcost"
)

const (
	exitInvalidCLI  = 2
	exitOperational = 3
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "invalid request: subcommand must be measure or compare")
		return exitInvalidCLI
	}
	switch args[0] {
	case "measure":
		return runMeasure(ctx, args[1:], stdout, stderr)
	case "compare":
		return runCompare(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "invalid request: subcommand must be measure or compare")
		return exitInvalidCLI
	}
}

func runMeasure(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("aip-testcost measure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("target", "", "measurement target: test-unit, quality-checks, or qa-tagged-hotspots")
	root := flags.String("root", "", "repository root")
	revision := flags.String("revision", "", "revision recorded in a measurement")
	out := flags.String("out", "", "JSON output path, or - for stdout")
	tempRoot := flags.String("temp-root", "", "temporary root for logs and process scratch")
	parallel := flags.Int("parallel", 0, "test parallelism override")
	if err := flags.Parse(args); err != nil {
		return exitInvalidCLI
	}
	if *target != testcost.TargetTestUnit && *target != testcost.TargetQualityChecks && *target != testcost.TargetQATaggedHotspots {
		_, _ = fmt.Fprintln(stderr, "invalid request: --target must be test-unit, quality-checks, or qa-tagged-hotspots")
		return exitInvalidCLI
	}
	if *root == "" || *revision == "" || *out == "" || *tempRoot == "" {
		_, _ = fmt.Fprintln(stderr, "invalid request: --root, --revision, --out, and --temp-root are required")
		return exitInvalidCLI
	}
	measurement, err := testcost.Measure(ctx, *target, testcost.MeasureOptions{Root: *root, Revision: *revision, TempRoot: *tempRoot, Parallel: *parallel})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "measurement failed: %v\n", err)
		if measurement.FailureTail != "" {
			_, _ = fmt.Fprintf(stderr, "failure tail:\n%s\n", measurement.FailureTail)
		}
		return exitOperational
	}
	if err := writeJSON(*out, measurement.Measurement, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "write output: %v\n", err)
		return exitOperational
	}
	writeMeasurementSummary(stderr, measurement.Measurement)
	return 0
}

func runCompare(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("aip-testcost compare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	baselinePath := flags.String("baseline", "", "baseline measurement JSON")
	currentPath := flags.String("current", "", "current measurement JSON")
	policyPath := flags.String("policy", "", "ratchet policy JSON")
	out := flags.String("out", "-", "JSON report output path, or - for stdout")
	target := flags.String("target", "", "expected target")
	allowOverride := flags.Bool("allow-override", false, "authorize reported violations")
	if err := flags.Parse(args); err != nil {
		return exitInvalidCLI
	}
	if *baselinePath == "" || *currentPath == "" || *policyPath == "" {
		_, _ = fmt.Fprintln(stderr, "invalid request: baseline, current, and policy are required")
		return exitInvalidCLI
	}
	if *out == "" {
		_, _ = fmt.Fprintln(stderr, "invalid request: --out cannot be empty")
		return exitInvalidCLI
	}
	baseline, err := loadMeasurement(*baselinePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid baseline: %v\n", err)
		return exitInvalidCLI
	}
	current, err := loadMeasurement(*currentPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid current: %v\n", err)
		return exitInvalidCLI
	}
	policy, err := loadPolicy(*policyPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid policy: %v\n", err)
		return exitInvalidCLI
	}
	if *target != "" && *target != current.Target {
		_, _ = fmt.Fprintf(stderr, "invalid request: target %q does not match current measurement %q\n", *target, current.Target)
		return exitInvalidCLI
	}
	report, err := testcost.CompareWithOptions(baseline, current, policy, testcost.CompareOptions{AllowOverride: *allowOverride})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "comparison failed: %v\n", err)
		return exitInvalidCLI
	}
	if err := writeJSON(*out, report, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "write output: %v\n", err)
		return exitOperational
	}
	writeReportSummary(stderr, report, baseline, current)
	return report.ExitCode()
}

func loadMeasurement(path string) (testcost.Measurement, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return testcost.Measurement{}, err
	}
	return testcost.DecodeMeasurement(data)
}

func loadPolicy(path string) (testcost.Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return testcost.Policy{}, err
	}
	return testcost.DecodePolicy(data)
}

func writeJSON(path string, value any, stdout io.Writer) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "" || path == "-" {
		_, err = stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func writeReportSummary(w io.Writer, report testcost.Report, baseline, current testcost.Measurement) {
	_, _ = fmt.Fprintf(w, "target=%s passed=%t overridden=%t violations=%d warnings=%d\n", report.Target, report.Passed, report.Overridden, len(report.Violations), len(report.Warnings))
	for _, metric := range report.Overall {
		writeMetricSummary(w, metric)
	}
	_, _ = fmt.Fprintf(w, "diagnostics active_anchor=%d active_head=%d page_faults_anchor=%d page_faults_head=%d read_bytes_anchor=%d read_bytes_head=%d write_bytes_anchor=%d write_bytes_head=%d other_bytes_anchor=%d other_bytes_head=%d\n", baseline.Process.ActiveProcesses, current.Process.ActiveProcesses, baseline.Process.PageFaults, current.Process.PageFaults, baseline.Process.ReadBytes, current.Process.ReadBytes, baseline.Process.WriteBytes, current.Process.WriteBytes, baseline.Process.OtherBytes, current.Process.OtherBytes)
	if len(report.Violations) > 0 {
		_, _ = fmt.Fprintln(w, "violations:")
		for _, violation := range report.Violations {
			_, _ = fmt.Fprintf(w, "  %s %s anchor=%d head=%d delta=%d limit=%d\n", violation.Package, violation.Metric, violation.Baseline, violation.Current, violation.Delta, violation.Allowed)
		}
	}
	if len(report.Warnings) > 0 {
		_, _ = fmt.Fprintln(w, "warnings:")
		for _, warning := range report.Warnings {
			_, _ = fmt.Fprintf(w, "  %s elapsed=%d warn=%d fail=%d\n", warning.Package, warning.ElapsedNanos, warning.WarnNanos, warning.FailNanos)
		}
	}
	_, _ = fmt.Fprintln(w, "package | anchor | head | delta")
	for _, delta := range report.TopDeltas {
		_, _ = fmt.Fprintf(w, "%s | %d | %d | %d\n", delta.Package, delta.AnchorElapsedNanos, delta.HeadElapsedNanos, delta.DeltaNanos)
	}
}

func writeMetricSummary(w io.Writer, metric testcost.OverallComparison) {
	name := metric.Metric
	switch metric.Metric {
	case "cpu":
		name = "CPU"
	case "io_operations":
		name = "I/O ops"
	}
	_, _ = fmt.Fprintf(w, "%s anchor=%d head=%d delta=%d limit=%d\n", name, metric.Anchor, metric.Head, metric.Delta, metric.Limit)
}

func writeMeasurementSummary(w io.Writer, measurement testcost.Measurement) {
	_, _ = fmt.Fprintf(w, "target=%s revision=%s wall_nanos=%d cpu_nanos=%d packages=%d\n", measurement.Target, strings.TrimSpace(measurement.Revision), measurement.WallNanos, measurement.Process.TotalCPUNanos, len(measurement.Packages))
}
