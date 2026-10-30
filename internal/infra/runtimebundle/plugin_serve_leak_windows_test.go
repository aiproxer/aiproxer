//go:build windows

package runtimebundle_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func cleanTempEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "TEMP") || strings.EqualFold(k, "TMP") || strings.EqualFold(k, "TMPDIR") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func listTempDirsInRoot(root, prefix string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func assertNoNewDirsInRoot(t *testing.T, root string, before []string, prefix string) {
	t.Helper()
	beforeSet := make(map[string]struct{}, len(before))
	for _, b := range before {
		beforeSet[b] = struct{}{}
	}
	var leaked []string
	deadline := time.Now().Add(2 * time.Second)
	for {
		after := listTempDirsInRoot(root, prefix)
		leaked = nil
		for _, d := range after {
			if _, ok := beforeSet[d]; !ok {
				leaked = append(leaked, d)
			}
		}
		if len(leaked) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, d := range leaked {
		t.Errorf("leaked temp dir %q (prefix %q)", d, prefix)
	}
}

func createIsolatedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aip-leak-sub-*")
	if err != nil {
		t.Fatalf("create isolated temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestProduction_ServeAndValidateNoStagingLeak proves both the BuildHost
// (serve) and ValidateDistribution paths release every aip-plugin-serve-*
// staging root they create: verified artifact handles are closed before the
// staging directory is removed, so no new matching dirs remain under
// os.TempDir after normal operation on Windows.
//
// The test executes in an isolated subprocess with a test-specific TEMP root so
// concurrent tests creating aip-plugin-serve-* staging roots under the ambient
// os.TempDir do not trigger false-positive leak assertions.
func TestProduction_ServeAndValidateNoStagingLeak(t *testing.T) {
	t.Parallel()
	subTemp := createIsolatedTempDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProduction_ServeAndValidateNoStagingLeak$", "-test.v")
	cmd.Env = append(
		cleanTempEnv(os.Environ()),
		"GO_WANT_SERVE_LEAK_HELPER=1",
		"TEMP="+subTemp,
		"TMP="+subTemp,
		"TMPDIR="+subTemp,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v\n%s", err, string(out))
	}
}

// TestProduction_ServeAndValidateNoStagingLeak_ConcurrentAmbientOverlap proves that
// active/unrelated aip-plugin-serve-* directories in the ambient os.TempDir
// (created by concurrent tests) do not cause false-positive leak failures.
func TestProduction_ServeAndValidateNoStagingLeak_ConcurrentAmbientOverlap(t *testing.T) {
	t.Parallel()
	ambientDir, err := os.MkdirTemp("", "aip-plugin-serve-ambient-overlap-*")
	if err != nil {
		t.Fatalf("create ambient temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(ambientDir) })

	subTemp := createIsolatedTempDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProduction_ServeAndValidateNoStagingLeak$", "-test.v")
	cmd.Env = append(
		cleanTempEnv(os.Environ()),
		"GO_WANT_SERVE_LEAK_HELPER=1",
		"TEMP="+subTemp,
		"TMP="+subTemp,
		"TMPDIR="+subTemp,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed with concurrent ambient overlap: %v\n%s", err, string(out))
	}
}

//nolint:paralleltest // Helper process entrypoint invoked via exec.Command in TestProduction_ServeAndValidateNoStagingLeak
func TestHelperProduction_ServeAndValidateNoStagingLeak(t *testing.T) {
	if os.Getenv("GO_WANT_SERVE_LEAK_HELPER") != "1" {
		return
	}
	const prefix = "aip-plugin-serve-"
	tempRoot := os.TempDir()
	before := listTempDirsInRoot(tempRoot, prefix)

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	if err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	}); err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
	assertNoNewDirsInRoot(t, tempRoot, before, prefix)

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	during := listTempDirsInRoot(tempRoot, prefix)
	if len(during) != 1 {
		t.Fatalf("expected 1 active staging directory while host is open, got %v", during)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("host close: %v", err)
	}

	assertNoNewDirsInRoot(t, tempRoot, before, prefix)
}
