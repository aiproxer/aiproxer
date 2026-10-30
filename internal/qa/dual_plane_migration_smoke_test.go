package qa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
)

var (
	aipstdBinOnce sync.Once
	aipstdBinPath string
	aipstdBinErr  error
)

func getAipstdBinary(tb testing.TB, root string) string {
	tb.Helper()
	aipstdBinOnce.Do(func() {
		if prebuilt := os.Getenv("AIP_QA_AIPSTD_BINARY"); prebuilt != "" {
			if _, err := os.Stat(prebuilt); err != nil {
				aipstdBinErr = fmt.Errorf("stat prebuilt aipstd: %w", err)
				return
			}
			aipstdBinPath = prebuilt
			return
		}
		dir, err := os.MkdirTemp("", "aipstd-qa-bin-")
		if err != nil {
			aipstdBinErr = err
			return
		}
		name := "aipstd"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		dst := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", dst, "./cmd/aipstd")
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			aipstdBinErr = fmt.Errorf("build aipstd: %w\n%s", err, out)
			return
		}
		aipstdBinPath = dst
	})
	if aipstdBinErr != nil {
		tb.Fatal(aipstdBinErr)
	}
	return aipstdBinPath
}

// TestDualPlaneMigrationCheckConfigDogfoodStub runs the distributed aipstd
// binary against the dogfood-local-stub backendplugin configuration. The
// operator-facing rollout narrative in docs/dual-plane-migration-rollout.md is
// human-reviewed; the machine-checkable guarantee is that a real aipstd build
// accepts the distributed connector configuration.
func TestDualPlaneMigrationCheckConfigDogfoodStub(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	cfg := bpkit.WriteDogfoodLocalStubConfig(t)
	bin := getAipstdBinary(t, root)
	cmd := exec.Command(bin, "check-config", "--config", cfg)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check-config dogfood-local-stub: %v\n%s", err, out)
	}
}
