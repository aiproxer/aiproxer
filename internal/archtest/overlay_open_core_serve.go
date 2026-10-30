package archtest

import (
	"os"
	"path/filepath"
	"sort"
)

// OpenCoreServeGrowthBaselineSHA identifies the frozen pre-C4 tree (92cba5a22,
// the main merge commit of the task-4.2 admin-mount slice, #1519) used for
// the open-core serve per-file baselines. The pin names a commit on main, so
// every clone can read it.
const OpenCoreServeGrowthBaselineSHA = "92cba5a2203e681903db52e5dfd760dedb2ce2b1"

// OpenCoreServeGrowthOverlayMax ratchets the open-core-billing-split task-5.1
// C4 serve-lifecycle growth independently from the legacy convergence delta.
// C4 is additive by design — the retained stdhttp generation host worker that
// makes a failed drain resumable, the single-admission aipruntime Serve with
// its convergent Close, and the build-time serve view/config/logger capture —
// yet the historical floor has zero headroom and every existing allowlist is
// closed to these files, so the growth was uncredited. The owner authorized
// this bounded accounting repair ("Ignore the line limits/budgets as long as
// code stays in reasonable shape. Human override"), applied narrowly as
// exactly the measured 390 net uncredited lines with no extra reserve: the
// -795 floor, every existing overlay cap, the surface baselines and every
// other allowlist are unchanged, no file moves into or out of the historical
// scan, and the credited seams carry no pricing, rating or other money logic.
//
// Like the task-4.2 admin-seam overlay (and unlike task-4.1's per-file
// positive-only credit), this overlay credits the seam's net signed growth,
// clamped at zero, so later in-seam deletions offset growth instead of being
// ignored. Every allowlisted file carries a positive delta for this additive
// slice, so the net sum equals the positive-only sum exactly.
const OpenCoreServeGrowthOverlayMax = 390

// openCoreServeGrowthFile is one allowlisted production file with its line
// count locked at the frozen pre-C4 tree. The overlay credits the signed delta
// above the baseline; baseline lines can never enter, and deletions only
// shrink the credit.
type openCoreServeGrowthFile struct {
	path     string
	baseline int
}

// openCoreServeGrowthFiles is the single allowlist of open-core serve
// lifecycle growth surfaces: exactly the C4 files that no existing overlay
// claims (the retained stdhttp worker, the single-admission Serve/Close
// convergence, and the Runtime capture fields that make Serve possible). All
// paths live inside the Req 11.5 convergence surfaces and outside every other
// overlay; the drift test pins this table exactly, so broadening requires an
// explicit table edit that review must approve.
var openCoreServeGrowthFiles = []openCoreServeGrowthFile{
	{path: "internal/stdhttp/generation_host.go", baseline: 202},
	{path: "pkg/aipruntime/build.go", baseline: 48},
	{path: "pkg/aipruntime/host.go", baseline: 68},
	{path: "pkg/aipruntime/serve.go", baseline: 0},
}

// measureOpenCoreServeGrowthOverlay credits the seam's net signed growth above
// the locked baselines, clamped at zero. Every allowlisted file is always
// counted: a missing file contributes its full negative baseline as zero
// current lines, never a skip, so deleting a seam file reduces the credit.
// Files already claimed by another overlay are skipped (never double counted;
// the disjointness test flags any such overlap loudly). Only files with
// positive deltas are listed as credited, and only when the net credit is
// positive; a clamped-to-zero measurement credits no files.
func measureOpenCoreServeGrowthOverlay(root string, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Name: "Open-core serve growth", Max: OpenCoreServeGrowthOverlayMax}
	net := 0
	var credited []string
	for _, f := range openCoreServeGrowthFiles {
		if _, skip := exclude[f.path]; skip {
			continue
		}
		n, err := countTreeFileLines(filepath.Join(root, filepath.FromSlash(f.path)))
		if err != nil {
			if os.IsNotExist(err) {
				n = 0
			} else {
				return OverlayMeasurement{}, err
			}
		}
		delta := n - f.baseline
		net += delta
		if delta > 0 {
			credited = append(credited, f.path)
		}
	}
	if net > 0 {
		m.Lines = net
		m.Files = credited
	}
	sort.Strings(m.Files)
	m.Pass = m.Lines <= m.Max
	return m, nil
}
