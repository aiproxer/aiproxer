package secretguard

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const msgSecretGuardDecision = "aip.secret_guard.decision"

// NewSlogObserver returns a sdk.Observer that writes secret-safe decision
// events to log. log must be non-nil. Finding values are never logged; only
// bounded references, provenance, counts, and source categories are emitted.
func NewSlogObserver(log *slog.Logger) (sdk.Observer, error) {
	if log == nil {
		return nil, fmt.Errorf("secretaudit: nil logger")
	}
	return slogObserver{log: log}, nil
}

type slogObserver struct {
	log *slog.Logger
}

func (o slogObserver) OnSecretDecision(ctx context.Context, ev sdk.DecisionEvent) error {
	if o.log == nil {
		return nil
	}
	attrs := []slog.Attr{
		slog.Time("timestamp", ev.Timestamp),
		slog.String("event_id", ev.EventID),
		slog.String("trace_id", ev.TraceID),
		slog.String("session_id", ev.SessionID),
		slog.String("a_leg_id", ev.ALegID),
		slog.String("turn_id", ev.TurnID),
		slog.String("principal_id", ev.PrincipalID),
		slog.String("tenant_id", ev.TenantID),
		slog.String("org_id", ev.OrgID),
		slog.String("workspace_id", ev.WorkspaceID),
		slog.String("peer_ip", ev.PeerIP),
		slog.String("source", ev.Source),
		slog.String("frontend_id", ev.FrontendID),
		slog.String("operation", ev.Operation),
		slog.String("agent_identity_digest", ev.AgentIdentityDigest),
		slog.String("requested_route", ev.RequestedRoute),
		slog.String("requested_model", ev.RequestedModel),
		slog.String("action", ev.Action),
		slog.String("outcome", string(ev.Outcome)),
		slog.String("access_mode", ev.AccessMode),
		slog.String("config_version", ev.ConfigVersion),
		slog.String("quarantine_result", ev.QuarantineResult),
		slog.Bool("backend_dispatched", ev.BackendDispatched),
		slog.String("guard_id", ev.GuardID),
		slog.Bool("scan_limit_hit", ev.ScanLimitHit),
		slog.Any("findings", safeFindings(ev.Findings)),
		findingSummaryAttr(ev.Findings),
	}
	o.log.LogAttrs(ctx, slog.LevelInfo, msgSecretGuardDecision, attrs...)
	return nil
}

type safeFinding struct {
	SecretRefName   string   `json:"secret_ref_name"`
	Aliases         []string `json:"aliases,omitempty"`
	SourceCategory  string   `json:"source_category"`
	Location        string   `json:"location,omitempty"`
	OccurrenceCount int      `json:"occurrence_count"`
	DetectorID      string   `json:"detector_id,omitempty"`
	RuleID          string   `json:"rule_id,omitempty"`
	Confidence      string   `json:"confidence,omitempty"`
}

func safeFindings(findings []sdk.Finding) []safeFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]safeFinding, len(findings))
	for i := range findings {
		out[i] = safeFinding{
			SecretRefName:   findings[i].SecretRefName,
			Aliases:         append([]string(nil), findings[i].Aliases...),
			SourceCategory:  string(findings[i].SourceCategory),
			Location:        findings[i].Location,
			OccurrenceCount: findings[i].OccurrenceCount,
			DetectorID:      findings[i].DetectorID,
			RuleID:          findings[i].RuleID,
			Confidence:      findings[i].Confidence,
		}
	}
	return out
}

func findingSummaryAttr(findings []sdk.Finding) slog.Attr {
	count := len(findings)
	firstRef := ""
	categories := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for i, f := range findings {
		if i == 0 {
			firstRef = f.SecretRefName
		}
		cat := string(f.SourceCategory)
		if cat == "" {
			continue
		}
		if _, ok := seen[cat]; ok {
			continue
		}
		seen[cat] = struct{}{}
		categories = append(categories, cat)
	}
	slices.Sort(categories)
	return slog.Group(
		"finding_summary",
		slog.Int("count", count),
		slog.String("first_secret_ref", firstRef),
		slog.Any("source_categories", categories),
	)
}

// Ensure interface satisfaction at compile time.
var _ sdk.Observer = slogObserver{}
