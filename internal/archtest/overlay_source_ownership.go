package archtest

import (
	"os"
	"path/filepath"
	"sort"
)

// SourceOwnershipGrowthBaselineSHA identifies the immutable pre-feature tree
// used for the config-source inode ownership integration's per-file baselines.
const SourceOwnershipGrowthBaselineSHA = "09f93c1015eca50f51cba7f077f5cb0fb1a4189e"

// SourceOwnershipOverlayMax gives the measured 475-line integration 50% headroom,
// rounded up to 750 lines. Only actual positive growth is credited, never this
// cap or pre-existing code. The historical 800-line reduction remains unchanged.
const SourceOwnershipOverlayMax = 750

type sourceOwnershipGrowthFile struct {
	path     string
	baseline int
}

// Only previously unclaimed files in the historical scanned surfaces belong
// here. host_build.go, inspect.go, validate_distribution.go and
// validate_structural.go already belong to connector/path overlays. The lease
// primitives outside these surfaces receive no convergence credit.
var sourceOwnershipGrowthFiles = []sourceOwnershipGrowthFile{
	{path: "internal/infra/runtimebundle/bootstrap_effective.go", baseline: 95},
	{path: "internal/infra/runtimebundle/reload_host.go", baseline: 284},
	{path: "internal/infra/runtimebundle/resource_ledger.go", baseline: 424},
	{path: "internal/infra/runtimehost/attempt_gate.go", baseline: 242},
	{path: "internal/infra/runtimehost/attempt_runner.go", baseline: 380},
	{path: "internal/infra/runtimehost/coordinator.go", baseline: 292},
	{path: "internal/infra/runtimehost/manager.go", baseline: 370},
	{path: "internal/infra/runtimehost/reload_ports.go", baseline: 67},
	{path: "internal/infra/runtimehost/reload_state.go", baseline: 214},
}

func measureSourceOwnershipGrowthOverlay(root string, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Name: "Config-source ownership", Max: SourceOwnershipOverlayMax}
	for _, f := range sourceOwnershipGrowthFiles {
		if _, skip := exclude[f.path]; skip {
			continue
		}
		n, err := countTreeFileLines(filepath.Join(root, filepath.FromSlash(f.path)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return OverlayMeasurement{}, err
		}
		if growth := n - f.baseline; growth > 0 {
			m.Files = append(m.Files, f.path)
			m.Lines += growth
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}
