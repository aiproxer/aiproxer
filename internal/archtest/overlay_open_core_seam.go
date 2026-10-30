package archtest

import (
	"os"
	"path/filepath"
	"sort"
)

// OpenCoreSeamGrowthBaselineSHA identifies the frozen pre-task-4.1 tree
// (413c4a9a7, the main merge commit of the predecessor slice, #1517) used for
// the open-core attachment-seam per-file baselines. The policy slice's own
// HEAD equals this tree, so it credits zero here and only the integrated
// task-4.1 growth afterwards. The pin names a commit on main, so every clone
// can read it; growth main made to an allowlisted file before the slice stays
// in the baseline instead of the credit.
const OpenCoreSeamGrowthBaselineSHA = "413c4a9a7516f07dccece56c3110510cace140b9"

// OpenCoreSeamGrowthOverlayMax ratchets the open-core-billing-split task-4.1
// attachment-seam growth independently from the legacy convergence delta. Task
// 4.1 is additive by design — public capability/port aliases, the gated
// validation input, the startup-registration callback and the stock-command
// capability declarations — yet the historical floor has zero headroom and
// every existing allowlist is closed to these six files, so the growth was
// uncredited. The owner authorized this bounded accounting repair ("Ignore
// the line limits/budgets as long as code stays in reasonable shape. Human
// override"), applied narrowly as exactly the measured 130 uncredited lines
// with no extra reserve: the -795 floor, every existing overlay cap, the
// surface baselines and every other allowlist are unchanged, no file moves
// into or out of the historical scan, and the credited seams carry no
// pricing, rating or other money logic.
const OpenCoreSeamGrowthOverlayMax = 130

// openCoreSeamGrowthFile is one allowlisted production file with its line
// count locked at the frozen pre-task-4.1 tree. Only lines above the baseline
// enter the allowance; baseline lines can never enter, and deletions only
// shrink the credit.
type openCoreSeamGrowthFile struct {
	path     string
	baseline int
}

// openCoreSeamGrowthFiles is the single allowlist of open-core attachment-seam
// growth surfaces: exactly the task-4.1 composition files that no existing
// overlay claims. All paths live inside the Req 11.5 convergence surfaces and
// outside every other overlay; the drift test pins this table exactly, so
// broadening requires an explicit table edit that review must approve.
var openCoreSeamGrowthFiles = []openCoreSeamGrowthFile{
	{path: "internal/infra/runtimebundle/build_observability.go", baseline: 107},
	{path: "cmd/aipstd/wiring.go", baseline: 9},
	{path: "cmd/aipstd/command.go", baseline: 420},
	{path: "pkg/aipruntime/normalize.go", baseline: 105},
	{path: "pkg/aipruntime/options.go", baseline: 59},
	{path: "pkg/aipruntime/capabilities.go", baseline: 0},
}

// measureOpenCoreSeamGrowthOverlay credits only per-file growth above the
// locked baselines. Files already claimed by another overlay are skipped
// (never double counted; the disjointness test flags any such overlap loudly).
// Missing files credit zero, so deletions always pass.
func measureOpenCoreSeamGrowthOverlay(root string, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Name: "Open-core seam growth", Max: OpenCoreSeamGrowthOverlayMax}
	for _, f := range openCoreSeamGrowthFiles {
		if _, skip := exclude[f.path]; skip {
			continue
		}
		n, err := countTreeFileLines(filepath.Join(root, filepath.FromSlash(f.path)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return OverlayMeasurement{}, err
		}
		if credit := n - f.baseline; credit > 0 {
			m.Lines += credit
			m.Files = append(m.Files, f.path)
		}
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}
