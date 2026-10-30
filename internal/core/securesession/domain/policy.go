package domain

// PrincipalIDPresent reports whether a principal id is bound (non-empty after trim).
func PrincipalIDPresent(p PrincipalRef) bool {
	return p.ID != ""
}
