package auth

import "strings"

// PrincipalSnapshot is a minimal non-secret identity fragment for core-local audit helpers
// without depending on pkg/aipsdk/execview in call signatures.
type PrincipalSnapshot struct {
	ID          string
	DisplayName string
}

// NewPrincipalSnapshot trims stable identity fields for session-start and related audit paths.
func NewPrincipalSnapshot(id, displayName string) PrincipalSnapshot {
	return PrincipalSnapshot{
		ID:          strings.TrimSpace(id),
		DisplayName: strings.TrimSpace(displayName),
	}
}
