package session

import "strings"

// SessionView is a read-only snapshot of session state exposed to plugins (design §2).
type SessionView struct {
	// AuthoritativeSessionID is the proxy-owned secure session identifier when issued; empty before secure-session bind.
	AuthoritativeSessionID string
	// ClientSessionHint is the client-supplied conversation or session hint (never proof of ownership).
	ClientSessionHint string
	// ALegID is the resolved B2BUA A-leg identifier for this request.
	ALegID string
	// IsNew is true when the A-leg row appears newly created for this touch (CreatedAt equals LastSeenAt).
	IsNew bool
	// AuthoritativeFirstTurn is true only when secure-session BeginResult.IsNew
	// established the first turn. It is separate from IsNew, a timestamp-derived
	// B2BUA projection.
	AuthoritativeFirstTurn bool
	// WorkspaceID is the resolved workspace identifier when the workspace resolver provides one.
	WorkspaceID string
	// ResumeEligible is true when secure-session policy allows resume for this session; set from [app.Manager.BeginTurn] when applicable.
	ResumeEligible bool
	// Labels carries active treatment / policy labels (including session_open upserts).
	Labels map[string]string
	// Classification is the bounded, read-only session classification snapshot.
	Classification Classification
	// TurnID is the proxy-owned secure-session turn identifier when secure sessions are active.
	TurnID string
}

// PartitionKey returns the key used for session-scoped plugin state: authoritative id when set, otherwise the client hint.
func (v SessionView) PartitionKey() string {
	if s := strings.TrimSpace(v.AuthoritativeSessionID); s != "" {
		return s
	}
	return strings.TrimSpace(v.ClientSessionHint)
}
