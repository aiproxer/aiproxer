package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryFile(t *testing.T, name ...string) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(append([]string{root, "..", ".."}, name...)...)
}

func readRepositoryFile(t *testing.T, name ...string) string {
	t.Helper()
	contents, err := os.ReadFile(repositoryFile(t, name...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(name...), err)
	}
	return string(contents)
}

func TestWindowsTaskReliability_TaskRunnerCaptureContract(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell taskrunner contract is Windows-specific")
	}
	t.Parallel()
	root := repositoryFile(t)
	// The 30s budget tolerates cold PowerShell spawn latency on loaded CI
	// runners; the contract under test is exact, duplicate-free capture.
	command := "$items = @(Invoke-TaskRunner -Label 'contract' -Cwd '" + strings.ReplaceAll(root, "'", "''") + "' -Timeout '30s' -Output capture -Command @('powershell', '-NoProfile', '-Command', 'Write-Output module-a; Write-Output module-b; Write-Output module-c')); Write-Output ('count=' + $items.Count); $items | ForEach-Object { Write-Output $_ }"
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ". '"+strings.ReplaceAll(filepath.Join(root, "scripts", "taskrunner.ps1"), "'", "''")+"'; "+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture contract: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\r\n")
	if len(lines) != 4 || lines[0] != "count=3" || strings.Join(lines[1:], "\n") != "module-a\nmodule-b\nmodule-c" {
		t.Fatalf("capture returned duplicated or unexpected discovery output: %q", output)
	}
}

func TestWindowsTaskReliability_RobocopyExitCodeClassifier(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("robocopy exit-code protocol is Windows-specific")
	}
	t.Parallel()
	script := repositoryFile(t, "scripts", "backend-plugin-module-checks.ps1")
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-SelfTest")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robocopy classifier self-test failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "robocopy exit-code classifier self-test") {
		t.Fatalf("robocopy classifier self-test marker missing: %q", output)
	}
}

func TestWindowsTaskReliability_TaskRunnerCaptureFailureDiagnosticOnce(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell taskrunner contract is Windows-specific")
	}
	t.Parallel()
	root := repositoryFile(t)
	marker := "unique-powershell-taskrunner-failure-marker"
	// The 30s budget tolerates cold PowerShell spawn latency on loaded CI
	// runners; the contract under test is exactly-once failure diagnostics.
	command := "$ErrorActionPreference = 'Continue'; try { Invoke-TaskRunner -Label 'contract-failure' -Cwd '" + strings.ReplaceAll(root, "'", "''") + "' -Timeout '30s' -Output capture -Command @('powershell', '-NoProfile', '-Command', 'Write-Output " + marker + "; exit 23') } catch { Write-Output ('caught=' + $_.Exception.Message) }"
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ". '"+strings.ReplaceAll(filepath.Join(root, "scripts", "taskrunner.ps1"), "'", "''")+"'; "+command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failure contract: %v\n%s", err, output)
	}
	if got := strings.Count(string(output), marker); got != 1 {
		t.Fatalf("failure marker count = %d, want 1: %q", got, output)
	}
}
