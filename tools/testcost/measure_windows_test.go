//go:build windows

package testcost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func TestWindowsCostProbeHasProcessAccounting(t *testing.T) {
	t.Parallel()

	result := taskrunner.Run(context.Background(), taskrunner.Request{
		Argv:    []string{"cmd.exe", "/C", "exit", "0"},
		Timeout: 30 * time.Second,
		Output:  taskrunner.Stream,
	})
	if result.Kind != taskrunner.Success {
		t.Fatalf("probe result kind = %s, error = %v", result.Kind, result.Err)
	}
	if result.AccountingErr != nil {
		t.Fatalf("probe accounting error = %v", result.AccountingErr)
	}
	if !result.Accounting.Supported {
		t.Fatal("Windows taskrunner accounting must be supported for cost measurement")
	}
}

func TestWindowsMeasureSmallModuleEndToEnd(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeProbeFile(t, filepath.Join(root, "go.mod"), "module example.com/testcostprobe\n\ngo 1.26\n")
	writeProbeFile(t, filepath.Join(root, "probe_test.go"), "package probe\n\nimport \"testing\"\n\nfunc TestProbe(t *testing.T) {}\n")
	result, err := Measure(context.Background(), TargetTestUnit, MeasureOptions{
		Root:     root,
		Revision: "integration-probe",
		TempRoot: filepath.Join(t.TempDir(), "measure-temp"),
		Parallel: 1,
	})
	if err != nil {
		t.Fatalf("Measure() error = %v, tail=%q", err, result.FailureTail)
	}
	if result.Process.TotalProcesses == 0 || result.WallNanos == 0 {
		t.Fatalf("measurement lacks process/wall accounting: %#v", result.Measurement)
	}
	if _, ok := result.Packages["example.com/testcostprobe"]; !ok {
		t.Fatalf("package elapsed missing: %#v", result.Packages)
	}
}

func writeProbeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
