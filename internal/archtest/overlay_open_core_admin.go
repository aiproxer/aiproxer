package archtest

import (
	"os"
	"path/filepath"
	"sort"
)

// OpenCoreAdminGrowthBaselineSHA identifies the frozen pre-task-4.2 tree
// (cbd1762dd, the main merge commit of the task-4.1 attachment-options slice,
// #1518) used for the open-core admin-seam per-file baselines. The pin names a
// commit on main, so every clone can read it. The policy slice's own HEAD
// equals this tree, so it credits zero here and only the integrated task-4.2
// growth afterwards.
const OpenCoreAdminGrowthBaselineSHA = "cbd1762ddabccfef071d60924cb98c0539228e15"

// OpenCoreAdminGrowthOverlayMax ratchets the open-core-billing-split task-4.2
// admin-seam growth independently from the legacy convergence delta. Task 4.2
// is additive by design — the runtimebundle admin-mount assembly and authority
// queries, the stdhttp admin control-plane adapters and accounting authority,
// the HTTP input contract, and the request-plane rewiring — yet the historical
// floor has zero headroom and every existing allowlist is closed to these nine
// files, so the growth was uncredited. The owner authorized this bounded
// accounting repair ("Ignore the line limits/budgets as long as code stays in
// reasonable shape. Human override"), applied narrowly as exactly the measured
// 233 net uncredited lines with no extra reserve: the -795 floor, every existing
// overlay cap, the surface baselines and every other allowlist are unchanged, no
// file moves into or out of the historical scan, and the credited seams carry no
// pricing, rating or other money logic.
//
// Unlike the task-4.1 seam overlay, which credits only positive per-file growth,
// this overlay credits the seam's net signed growth, clamped at zero. Task 4.2
// grows the admin seam and shrinks two of its own files as part of the same
// change (compile_generation.go and adapt.go), so the positive-only sum (248)
// would over-credit the seam by the two in-seam reductions (15); the net sum is
// exactly the measured 233. These net semantics are scoped to this mixed
// growth/shrink seam and do not generalize the task-4.1 feature's positive-only
// behavior.
const OpenCoreAdminGrowthOverlayMax = 233

// openCoreAdminGrowthFile is one allowlisted production file with its line
// count locked at the frozen pre-task-4.2 tree. The overlay credits the signed
// delta above the baseline; baseline lines can never enter, and deletions only
// shrink the credit.
type openCoreAdminGrowthFile struct {
	path     string
	baseline int
}

// openCoreAdminGrowthFiles is the single allowlist of open-core admin-seam
// growth surfaces: exactly the task-4.2 composition files that no existing
// overlay claims. All paths live inside the Req 11.5 convergence surfaces and
// outside every other overlay; the drift test pins this table exactly, so
// broadening requires an explicit table edit that review must approve.
var openCoreAdminGrowthFiles = []openCoreAdminGrowthFile{
	{path: "internal/infra/runtimebundle/admin_mounts.go", baseline: 0},
	{path: "internal/infra/runtimebundle/authority_queries.go", baseline: 0},
	{path: "internal/infra/runtimebundle/compile_generation.go", baseline: 439},
	{path: "internal/stdhttp/admin/controlplane/accounting_authority.go", baseline: 187},
	{path: "internal/stdhttp/admin/controlplane/adapt.go", baseline: 45},
	{path: "internal/stdhttp/contract/http_input.go", baseline: 250},
	{path: "internal/stdhttp/handler.go", baseline: 109},
	{path: "internal/stdhttp/mount_admin.go", baseline: 283},
	{path: "internal/stdhttp/request_plane.go", baseline: 80},
}

// measureOpenCoreAdminGrowthOverlay credits the seam's net signed growth above
// the locked baselines, clamped at zero. Every allowlisted file is always
// counted: a missing file contributes its full negative baseline as zero
// current lines, never a skip, so deleting a seam file reduces the credit.
// Files already claimed by another overlay are skipped (never double counted;
// the disjointness test flags any such overlap loudly). Only files with
// positive deltas are listed as credited, and only when the net credit is
// positive; a clamped-to-zero measurement credits no files.
func measureOpenCoreAdminGrowthOverlay(root string, exclude map[string]struct{}) (OverlayMeasurement, error) {
	m := OverlayMeasurement{Name: "Open-core admin seam growth", Max: OpenCoreAdminGrowthOverlayMax}
	net := 0
	var credited []string
	for _, f := range openCoreAdminGrowthFiles {
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
