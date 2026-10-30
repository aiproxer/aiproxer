package execview

// PrincipalView is a generic identity snapshot visible to plugins (no HTTP or auth-provider types).
// For richer request attribution (subject kind, credential id, tenant/org/project/department/cost
// center, policy labels, origin — see [scope.PrincipalScopeView] in pkg/aipsdk/scope), this type
// is the legacy compatibility projection derived from the authoritative scope view.
type PrincipalView struct {
	ID          string
	DisplayName string
	Roles       []string
	Claims      map[string]string
}

// AttemptView captures attempt-scoped lineage metadata for one backend open (design §2).
type AttemptView struct {
	TraceID    string
	BLegID     string
	AttemptSeq int
	BackendID  string
	RouteRole  string
}
