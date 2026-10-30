package authevent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// SlogEventSink implements [coreauth.EventSink] using structured logs. It never logs
// [sdkauth.AuthDecisionEvent.PrincipalSafeClaims] values—only sorted claim keys—to avoid
// leaking misconfigured secrets into observability.
type SlogEventSink struct {
	log *slog.Logger
}

// NewSlogEventSink returns a sink that writes auth events to log. log must be non-nil.
// Callers needing [coreauth.EventSink] may use the return value directly — it implements the interface.
func NewSlogEventSink(log *slog.Logger) (*SlogEventSink, error) {
	if log == nil {
		return nil, fmt.Errorf("authevent: nil logger")
	}
	return &SlogEventSink{log: log}, nil
}

const (
	msgAuthDecision       = "aip.auth.auth_decision"
	msgSessionStart       = "aip.auth.session_start"
	attrComponent         = "aip.component"
	valComponentAuth      = "auth_events"
	attrAIPRequestTraceID = "aip_request_trace_id"
)

// OnAuthDecision logs a non-secret snapshot of the auth decision event.
func (s *SlogEventSink) OnAuthDecision(ctx context.Context, ev sdkauth.AuthDecisionEvent) error {
	if s == nil || s.log == nil {
		return nil
	}
	claimKeys := safeClaimKeys(ev.PrincipalSafeClaims)
	attrs := []slog.Attr{
		slog.String(attrComponent, valComponentAuth),
		slog.Time("time", ev.Time),
		slog.String(attrAIPRequestTraceID, ev.TraceID),
		slog.String("access_mode", string(ev.AccessMode)),
		slog.String("required_level", string(ev.RequiredLevel)),
		slog.String("handler_kind", string(ev.HandlerKind)),
		slog.String("frontend", ev.Frontend),
		slog.String("outcome", string(ev.Outcome)),
		slog.String("reason_code", ev.ReasonCode),
		slog.String("principal_id", ev.PrincipalID),
		slog.String("principal_display_name", ev.PrincipalDisplayName),
		// ev is passed by value; roles slice is owned by the value (cloned at construction in stdhttp) and not mutated.
		slog.Any("principal_roles", ev.PrincipalRoles),
		slog.String("principal_safe_claim_keys", claimKeys),
		slog.String("device_id", ev.DeviceID),
		slog.String("device_key_id", ev.DeviceKeyID),
		slog.String("device_fingerprint", ev.DeviceFingerprint),
		slog.String("challenge_kind", string(ev.ChallengeKind)),
		slog.String("challenge_summary", challengeSummaryForLog(ev.ChallengeSummary)),
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, msgAuthDecision, attrs...)
	return nil
}

// OnSessionStart logs a non-secret snapshot of the session-start event.
func (s *SlogEventSink) OnSessionStart(ctx context.Context, ev sdkauth.SessionStartEvent) error {
	if s == nil || s.log == nil {
		return nil
	}
	attrs := []slog.Attr{
		slog.String(attrComponent, valComponentAuth),
		slog.Time("time", ev.Time),
		slog.String(attrAIPRequestTraceID, ev.TraceID),
		slog.String("access_mode", string(ev.AccessMode)),
		slog.String("required_level", string(ev.RequiredLevel)),
		slog.String("handler_kind", string(ev.HandlerKind)),
		slog.String("frontend", ev.Frontend),
		slog.String("session_id", ev.SessionID),
		slog.String("client_session_ref", ev.ClientSessionRef),
		slog.String("a_leg_id", ev.ALegID),
		slog.String("certainty", string(ev.Certainty)),
		slog.Bool("is_new", ev.IsNew),
		slog.String("principal_id", ev.PrincipalID),
		slog.String("principal_display_name", ev.PrincipalDisplayName),
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, msgSessionStart, attrs...)
	return nil
}

func challengeSummaryForLog(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	out := sdkauth.SanitizePublicChallengeSummary(s, "", sdkauth.PublicChallengeSummaryMaxRunes)
	if out == "" {
		return "[redacted]"
	}
	return out
}

func safeClaimKeys(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		k = strings.TrimSpace(k)
		if k != "" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}

// Discard is a no-op [coreauth.EventSink] for explicit disabled delivery in tests or wiring.
type Discard struct{}

func (Discard) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error { return nil }

func (Discard) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error { return nil }

// Ensure interface satisfaction at compile time.
var (
	_ coreauth.EventSink = (*SlogEventSink)(nil)
	_ coreauth.EventSink = Discard{}
)
