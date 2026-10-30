package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// SessionAuditPolicy is a frozen access + auth handler snapshot for operator-visible audit events
// emitted from the executor (session-start) and should stay aligned with HTTP auth policy snapshots.
type SessionAuditPolicy struct {
	AccessMode    sdkauth.AccessMode
	HandlerKind   sdkauth.HandlerKind
	RequiredLevel sdkauth.RequiredLevel
}

// SessionStartBuildInput carries resolved, non-secret session identity inputs for [BuildSessionStartEvent].
type SessionStartBuildInput struct {
	Now                  time.Time
	TraceID              string
	Policy               SessionAuditPolicy
	Frontend             string
	PrincipalID          string
	PrincipalDisplayName string

	AuthoritativeSessionID string
	ClientSessionIDRaw     string
	ALegID                 string
	IsNew                  bool
	// SyntheticLocalPrincipal is true when the composition root supplies a dev-only inferred principal.
	SyntheticLocalPrincipal bool
}

// OpaqueRefDigest returns a short stable hex digest for opaque client hints (same algorithm as
// runtime.HashOpaqueIDForLog) so session-start events never carry raw client session material.
func OpaqueRefDigest(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// BuildSessionStartEvent maps resolved executor state into a non-secret [sdkauth.SessionStartEvent].
func BuildSessionStartEvent(in SessionStartBuildInput) sdkauth.SessionStartEvent {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	certainty := sdkauth.SessionCertaintyKnown
	sid := strings.TrimSpace(in.AuthoritativeSessionID)
	leg := strings.TrimSpace(in.ALegID)
	if sid == "" || leg == "" {
		certainty = sdkauth.SessionCertaintyUnknown
	} else if in.SyntheticLocalPrincipal {
		// Inferred local-dev identity: session binding is real but principal is not authenticated.
		certainty = sdkauth.SessionCertaintyPartial
	}
	return sdkauth.SessionStartEvent{
		Time:                 now.UTC(),
		TraceID:              strings.TrimSpace(in.TraceID),
		AccessMode:           in.Policy.AccessMode,
		RequiredLevel:        in.Policy.RequiredLevel,
		HandlerKind:          in.Policy.HandlerKind,
		Frontend:             strings.TrimSpace(in.Frontend),
		SessionID:            sid,
		ClientSessionRef:     OpaqueRefDigest(in.ClientSessionIDRaw),
		ALegID:               leg,
		Certainty:            certainty,
		IsNew:                in.IsNew,
		PrincipalID:          strings.TrimSpace(in.PrincipalID),
		PrincipalDisplayName: strings.TrimSpace(in.PrincipalDisplayName),
	}
}
