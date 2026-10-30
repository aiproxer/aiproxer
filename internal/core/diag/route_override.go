package diag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
)

const (
	// RouteSelectorSourceClient is the bounded selector-source label when no admin override is active.
	RouteSelectorSourceClient = "client"
	// RouteSelectorSourceAdmin is the bounded selector-source label when an admin override was snapshotted.
	RouteSelectorSourceAdmin = "admin_override"
	// RouteOverrideMutationLogMsg is the structured mutation audit message.
	RouteOverrideMutationLogMsg = "route_override_mutation"
)

// RouteSelectorDigest returns a short hex SHA-256 prefix of the selector for logs.
func RouteSelectorDigest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

// BoundedALegID returns a hashed A-leg identifier suitable for ordinary logs.
func BoundedALegID(aLegID string) string {
	sum := sha256.Sum256([]byte(aLegID))
	return hex.EncodeToString(sum[:8])
}

// RouteOverrideMutation is the bounded audit payload for set/replace/clear.
type RouteOverrideMutation struct {
	Action        string
	Outcome       string
	Revision      int64
	ALegID        string
	Selector      string
	SelectorBytes int
	Active        bool
}

// LogRouteOverrideMutation emits a structured mutation record without raw selector or raw A-leg id.
func LogRouteOverrideMutation(ctx context.Context, log *slog.Logger, m RouteOverrideMutation) {
	if log == nil {
		return
	}
	attrs := []slog.Attr{
		slog.String("action", m.Action),
		slog.String("outcome", m.Outcome),
		slog.Int64("revision", m.Revision),
		slog.String("a_leg_hash", BoundedALegID(m.ALegID)),
	}
	if m.Active && m.Selector != "" {
		attrs = append(attrs,
			slog.String("selector_digest", RouteSelectorDigest(m.Selector)),
			slog.Int("selector_bytes", m.SelectorBytes),
		)
	}
	log.LogAttrs(ctx, slog.LevelInfo, RouteOverrideMutationLogMsg, attrs...)
}
