package controlplane

import (
	"fmt"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ScopeFlattener converts a safe [scope.PrincipalScopeView] into a
// presence-aware [cp.ScopeSnapshot] for storage and query filtering, and
// reconstructs a safe view from a stored snapshot (requirement 4.1, 4.2, 4.3,
// 4.7, 4.8, 9.1).
//
// It preserves unknown vs known-empty values for every filterable dimension,
// deep-clones roles, safe claims, and policy labels at the boundary so query
// results cannot expose unsafe or mutable caller-owned data, and enforces
// bounded map/slice sizes before persistence (requirement 4.8, performance &
// scalability).
type ScopeFlattener struct{}

// NewScopeFlattener returns a stateless ScopeFlattener safe for concurrent use.
func NewScopeFlattener() *ScopeFlattener { return &ScopeFlattener{} }

// MustFlatten converts a safe view into a presence-aware snapshot. It panics only
// if the view carries oversized roles/claims/labels maps; callers that handle
// untrusted input should use [ScopeFlattener.FlattenE] instead.
func (f *ScopeFlattener) MustFlatten(view scope.PrincipalScopeView) cp.ScopeSnapshot {
	snap, err := f.FlattenE(view)
	if err != nil {
		panic(err)
	}
	return snap
}

// FlattenE converts a safe view into a presence-aware snapshot and returns an
// error when the view carries oversized roles, safe claims, or policy labels
// (requirement 4.7, 4.8). The returned snapshot always carries deep-cloned
// slices and maps so the caller cannot mutate the source view through the
// snapshot or vice versa.
func (f *ScopeFlattener) FlattenE(view scope.PrincipalScopeView) (cp.ScopeSnapshot, error) {
	if err := validateScopeSnapshot(cp.ScopeSnapshot{Principal: view}); err != nil {
		return cp.ScopeSnapshot{}, err
	}
	clone := view.Clone()
	return cp.ScopeSnapshot{
		Principal:      clone,
		PrincipalID:    clone.PrincipalID,
		CredentialID:   clone.CredentialID,
		TenantID:       clone.TenantID,
		OrganizationID: clone.OrganizationID,
		WorkspaceID:    clone.WorkspaceID,
		ProjectID:      clone.ProjectID,
		DepartmentID:   clone.DepartmentID,
		CostCenterID:   clone.CostCenterID,
	}, nil
}

// Reconstruct rebuilds a safe [scope.PrincipalScopeView] from a stored
// snapshot, deep-cloning roles, safe claims, and policy labels so callers
// cannot mutate the stored snapshot through the returned view (requirement 4.2,
// 4.7, 9.1).
func (f *ScopeFlattener) Reconstruct(snap cp.ScopeSnapshot) scope.PrincipalScopeView {
	view := snap.Principal.Clone()
	if view.PrincipalID.IsUnknown() && !snap.PrincipalID.IsUnknown() {
		view.PrincipalID = snap.PrincipalID
	}
	return view
}

// flattenOrError is a helper used by the normalizer to convert a possibly-nil
// scope view into a snapshot. A nil view yields a zero (all-unknown) snapshot
// without an error so sources without scope attribution still produce valid
// events (requirement 4.1: attribution preserved when available, unknown when
// not supplied).
func flattenOrError(f *ScopeFlattener, view *scope.PrincipalScopeView) (cp.ScopeSnapshot, error) {
	if view == nil {
		return cp.ScopeSnapshot{}, nil
	}
	snap, err := f.FlattenE(*view)
	if err != nil {
		return cp.ScopeSnapshot{}, fmt.Errorf("%w: scope flatten: %v", ErrUnsafeEvidence, err)
	}
	return snap, nil
}
