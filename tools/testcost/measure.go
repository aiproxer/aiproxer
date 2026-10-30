package testcost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

const (
	TargetTestUnit         = "test-unit"
	TargetQualityChecks    = "quality-checks"
	TargetQATaggedHotspots = "qa-tagged-hotspots"
	defaultTimeout         = 12 * time.Minute
	defaultFailureTail     = 8 * 1024
)

var qaTaggedHotspotPackages = []string{
	"./internal/archtest",
	"./internal/infra/runtimebundle",
	"./tools/backendplugin/...",
}

// QATaggedHotspotPackages returns a cloned slice of the hotspot package roots.
func QATaggedHotspotPackages() []string {
	return slices.Clone(qaTaggedHotspotPackages)
}

type Runner interface {
	Run(context.Context, taskrunner.Request) taskrunner.Result
}

type RunnerFunc func(context.Context, taskrunner.Request) taskrunner.Result

func (f RunnerFunc) Run(ctx context.Context, request taskrunner.Request) taskrunner.Result {
	return f(ctx, request)
}

type taskRunner struct{}

func (taskRunner) Run(ctx context.Context, request taskrunner.Request) taskrunner.Result {
	return taskrunner.Run(ctx, request)
}

type MeasureOptions struct {
	Root             string
	Revision         string
	TempRoot         string
	LogDir           string
	Parallel         int
	ParallelEnv      string
	Timeout          time.Duration
	FailureTailBytes int
	Runner           Runner
}

// MeasurementResult carries the machine-readable measurement and operational
// paths/diagnostics. The JSON schema is Measurement; internal fields are not
// serialized.
type MeasurementResult struct {
	Measurement
	Result      taskrunner.Result `json:"-"`
	StdoutLog   string            `json:"-"`
	StderrLog   string            `json:"-"`
	FailureTail string            `json:"-"`
}

// Measurement is the return type used by the public measurement helpers.
// Keep this alias to make the shape explicit at call sites.
type RunMeasurement = MeasurementResult

func MeasureTestUnit(ctx context.Context, options MeasureOptions) (MeasurementResult, error) {
	return Measure(ctx, TargetTestUnit, options)
}

func MeasureQualityChecks(ctx context.Context, options MeasureOptions) (MeasurementResult, error) {
	return Measure(ctx, TargetQualityChecks, options)
}

func MeasureQATaggedHotspots(ctx context.Context, options MeasureOptions) (MeasurementResult, error) {
	return Measure(ctx, TargetQATaggedHotspots, options)
}

func BuildQATaggedHotspotsRequest(options MeasureOptions) (taskrunner.Request, error) {
	if runtime.GOOS != "windows" {
		return taskrunner.Request{}, ErrWindowsOnly
	}
	root, err := resolveRoot(options.Root)
	if err != nil {
		return taskrunner.Request{}, err
	}
	parallel := resolvedParallel(options)
	timeout, err := requestTimeout(options.Timeout)
	if err != nil {
		return taskrunner.Request{}, err
	}
	argv := append([]string{
		"go", "test", "-count=1", "-json",
		"-parallel=" + strconv.Itoa(parallel),
		"-timeout=10m",
		"-tags=precommit,integration",
	}, qaTaggedHotspotPackages...)
	return taskrunner.Request{
		Argv:          argv,
		Dir:           root,
		Timeout:       timeout,
		Output:        taskrunner.Stream,
		Label:         TargetQATaggedHotspots,
		RestrictAdmin: true,
	}, nil
}

func ResolveParallel(value string, cpus int) int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 1 {
		return parsed
	}
	if cpus < 1 {
		return 1
	}
	return cpus
}

func BuildTestUnitRequest(options MeasureOptions) (taskrunner.Request, error) {
	if runtime.GOOS != "windows" {
		return taskrunner.Request{}, ErrWindowsOnly
	}
	root, err := resolveRoot(options.Root)
	if err != nil {
		return taskrunner.Request{}, err
	}
	parallel := resolvedParallel(options)
	timeout, err := requestTimeout(options.Timeout)
	if err != nil {
		return taskrunner.Request{}, err
	}
	return taskrunner.Request{
		Argv:          []string{"go", "test", "-count=1", "-json", "-parallel=" + strconv.Itoa(parallel), "-timeout=10m", "./..."},
		Dir:           root,
		Timeout:       timeout,
		Output:        taskrunner.Stream,
		Label:         TargetTestUnit,
		RestrictAdmin: true,
	}, nil
}

func BuildQualityChecksRequest(options MeasureOptions) (taskrunner.Request, error) {
	if runtime.GOOS != "windows" {
		return taskrunner.Request{}, ErrWindowsOnly
	}
	root, err := resolveRoot(options.Root)
	if err != nil {
		return taskrunner.Request{}, err
	}
	tempRoot := options.TempRoot
	if tempRoot == "" {
		return taskrunner.Request{}, ErrMissingTempRoot
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		return taskrunner.Request{}, fmt.Errorf("testcost: resolve temp root: %w", err)
	}
	parallel := resolvedParallel(options)
	timeout, err := requestTimeout(options.Timeout)
	if err != nil {
		return taskrunner.Request{}, err
	}
	return taskrunner.Request{
		Argv:     []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "scripts/quality-checks.ps1"},
		Dir:      root,
		Timeout:  timeout,
		Output:   taskrunner.Stream,
		ClearEnv: false,
		// Anchor and head must execute the same guardrail work for a fair
		// comparison. This target-scoped skip only applies to the cost
		// measurement; the canonical quality-checks gate still runs lint.
		Env: []string{
			"CI=", "AIP_VERIFY_MODULE_CACHE=", "AIP_SKIP_ARCHTEST=1", "AIP_SKIP_GO_COMPILE_CHECKS=1", "AIP_SKIP_LINT=1",
			"AIP_TEST_PARALLEL=" + strconv.Itoa(parallel), "TEMP=" + tempRoot, "TMP=" + tempRoot,
		},
		Label:         TargetQualityChecks,
		RestrictAdmin: true,
	}, nil
}

func Measure(ctx context.Context, target string, options MeasureOptions) (MeasurementResult, error) {
	if ctx == nil {
		return MeasurementResult{}, errors.New("testcost: nil context")
	}
	if runtime.GOOS != "windows" {
		return MeasurementResult{}, ErrWindowsOnly
	}
	if target != TargetTestUnit && target != TargetQualityChecks && target != TargetQATaggedHotspots {
		return MeasurementResult{}, fmt.Errorf("%w %q", ErrUnsupportedTarget, target)
	}
	if strings.TrimSpace(options.TempRoot) == "" {
		return MeasurementResult{}, ErrMissingTempRoot
	}
	var request taskrunner.Request
	var err error
	switch target {
	case TargetTestUnit:
		request, err = BuildTestUnitRequest(options)
	case TargetQualityChecks:
		request, err = BuildQualityChecksRequest(options)
	case TargetQATaggedHotspots:
		request, err = BuildQATaggedHotspotsRequest(options)
	}
	if err != nil {
		return MeasurementResult{}, err
	}
	logDir := options.LogDir
	if logDir == "" {
		logDir = filepath.Join(options.TempRoot, "logs")
	}
	logDir, err = filepath.Abs(logDir)
	if err != nil {
		return MeasurementResult{}, fmt.Errorf("testcost: resolve log directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return MeasurementResult{}, fmt.Errorf("testcost: create log directory: %w", err)
	}
	if err := os.MkdirAll(options.TempRoot, 0o755); err != nil {
		return MeasurementResult{}, fmt.Errorf("testcost: create temp root: %w", err)
	}
	stdoutPath := filepath.Join(logDir, target+"-stdout.log")
	stderrPath := filepath.Join(logDir, target+"-stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		return MeasurementResult{}, fmt.Errorf("testcost: create stdout log: %w", err)
	}
	stderr, err := os.Create(stderrPath)
	if err != nil {
		_ = stdout.Close()
		return MeasurementResult{}, fmt.Errorf("testcost: create stderr log: %w", err)
	}
	request.StreamOut, request.StreamErr = stdout, stderr
	runner := options.Runner
	if runner == nil {
		runner = taskRunner{}
	}
	result := runner.Run(ctx, request)
	_ = stdout.Close()
	_ = stderr.Close()
	out := MeasurementResult{Result: result, StdoutLog: stdoutPath, StderrLog: stderrPath}
	if result.AccountingErr != nil {
		out.FailureTail = boundedTail(stderrPath, options.FailureTailBytes)
		return out, fmt.Errorf("%w: %v", ErrAccountingFailure, result.AccountingErr)
	}
	if !result.Accounting.Supported {
		out.FailureTail = boundedTail(stderrPath, options.FailureTailBytes)
		return out, ErrAccountingUnsupported
	}
	if result.Kind != taskrunner.Success {
		out.FailureTail = boundedTail(stderrPath, options.FailureTailBytes)
		if out.FailureTail == "" {
			out.FailureTail = boundedTail(stdoutPath, options.FailureTailBytes)
		}
		return out, fmt.Errorf("%w: %v: %s", ErrMeasurementFailed, result.Kind, out.FailureTail)
	}
	measurement := Measurement{SchemaVersion: SchemaVersion, Target: target, Revision: options.Revision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(), LogicalCPUs: runtime.NumCPU(), TestParallel: resolvedParallel(options), WallNanos: elapsedNanos(result.Elapsed), Process: processMetrics(result.Accounting)}
	if target == TargetTestUnit || target == TargetQATaggedHotspots {
		file, readErr := os.Open(stdoutPath)
		if readErr != nil {
			return out, fmt.Errorf("testcost: open test JSON log: %w", readErr)
		}
		parsed, parseErr := ParseTestJSON(file, target)
		_ = file.Close()
		if parseErr != nil {
			return out, parseErr
		}
		measurement.Packages = parsed.Packages
	}
	out.Measurement = measurement
	return out, nil
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("testcost: resolve root: %w", err)
	}
	return abs, nil
}

func requestTimeout(timeout time.Duration) (time.Duration, error) {
	if timeout == 0 {
		return defaultTimeout, nil
	}
	if timeout < 0 {
		return 0, errors.New("testcost: timeout must be positive")
	}
	return timeout, nil
}

func resolvedParallel(options MeasureOptions) int {
	if options.Parallel >= 1 {
		return options.Parallel
	}
	parallelEnv := options.ParallelEnv
	if parallelEnv == "" {
		parallelEnv = os.Getenv("AIP_TEST_PARALLEL")
	}
	return ResolveParallel(parallelEnv, runtime.NumCPU())
}

func processMetrics(accounting taskrunner.ProcessAccounting) ProcessMetrics {
	return ProcessMetrics{UserCPUNanos: nonNegative(accounting.UserCPUNanos), KernelCPUNanos: nonNegative(accounting.KernelCPUNanos), TotalCPUNanos: nonNegative(accounting.TotalCPUNanos), TotalProcesses: uint64(accounting.TotalProcesses), ActiveProcesses: uint64(accounting.ActiveProcesses), TerminatedProcesses: uint64(accounting.TerminatedProcesses), PageFaults: uint64(accounting.PageFaults), ReadOperations: accounting.ReadOperations, WriteOperations: accounting.WriteOperations, OtherOperations: accounting.OtherOperations, ReadBytes: accounting.ReadBytes, WriteBytes: accounting.WriteBytes, OtherBytes: accounting.OtherBytes}
}

func nonNegative(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func elapsedNanos(elapsed time.Duration) uint64 {
	if elapsed <= 0 {
		return 0
	}
	return uint64(elapsed)
}

func boundedTail(path string, limit int) string {
	if limit <= 0 {
		limit = defaultFailureTail
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(data)
}
