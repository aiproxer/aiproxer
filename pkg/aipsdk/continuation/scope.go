package continuation

import "fmt"

// Scope isolates continuation records to an authoritative client/session identity.
// Client-controlled metadata must not widen or substitute a scope.
type Scope struct {
	TenantID     string
	PrincipalID  string
	SessionID    string
	ConnectionID string // non-empty for connection-local store:false records
}

// String returns a stable diagnostic label without embedding secrets.
func (s Scope) String() string {
	return fmt.Sprintf("tenant=%q principal=%q session=%q connection=%q", s.TenantID, s.PrincipalID, s.SessionID, s.ConnectionID)
}

// Equal reports whether two scopes denote the same isolation boundary.
func (s Scope) Equal(o Scope) bool {
	return s.TenantID == o.TenantID &&
		s.PrincipalID == o.PrincipalID &&
		s.SessionID == o.SessionID &&
		s.ConnectionID == o.ConnectionID
}

// IsZero reports whether the scope carries no authoritative identity.
func (s Scope) IsZero() bool {
	return s.TenantID == "" && s.PrincipalID == "" && s.SessionID == "" && s.ConnectionID == ""
}
