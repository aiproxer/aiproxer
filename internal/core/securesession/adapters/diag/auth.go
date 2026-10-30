package diag

import (
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// HeaderOwnerScope optionally constrains diagnostics to a single owner principal id.
// When non-empty, session detail, transcript, audit, and by-A-leg lookups must match this owner
// or the handler returns a non-enumerating not-found response. When empty, the caller is treated
// as an unconstrained operator (still protected by diagnostics shared-secret at the HTTP edge).
const HeaderOwnerScope = "X-AIP-Diagnostics-Owner-Scope"

// SessionDiagOp classifies which diagnostics surface is being accessed.
type SessionDiagOp int

const (
	OpSummaryList SessionDiagOp = iota
	OpSessionDetail
	OpTranscript
	OpAudit
)

// AuthDecision is the outcome of operator authorization for one session-bound operation.
type AuthDecision struct {
	Allow          bool
	DenyAsNotFound bool
	// EffectivePolicy merges deployment redaction default with the session record for payload shaping.
	EffectivePolicy domain.PolicyMetadata
	// RawAuditAllowed mirrors session audit policy (full audit mode only).
	RawAuditAllowed bool
}

// SessionDiagnosticsAuthorizer enforces operator scope and derives redaction policy for diagnostics.
type SessionDiagnosticsAuthorizer interface {
	// ListFilters returns owner/workspace filters for Summary. When deny is true, the handler must
	// return an empty list without indicating that another owner's data exists.
	ListFilters(r *http.Request, redactionDefault string) (ownerID, workspaceID string, deny bool)

	// AuthorizeSession evaluates owner scope for a loaded session row.
	AuthorizeSession(r *http.Request, rec domain.Record, op SessionDiagOp, redactionDefault string) (AuthDecision, error)
}

// ScopedOwnerAuthorizer is the default [SessionDiagnosticsAuthorizer]: optional owner scope header
// must match the session owner for non-list operations; list requests with a conflicting owner query
// are denied as an empty result set.
type ScopedOwnerAuthorizer struct{}

// NewScopedOwnerAuthorizer returns a stateless authorizer.
func NewScopedOwnerAuthorizer() *ScopedOwnerAuthorizer {
	return &ScopedOwnerAuthorizer{}
}

// ListFilters implements [SessionDiagnosticsAuthorizer].
func (*ScopedOwnerAuthorizer) ListFilters(r *http.Request, redactionDefault string) (ownerID, workspaceID string, deny bool) {
	_ = redactionDefault
	scope := strings.TrimSpace(r.Header.Get(HeaderOwnerScope))
	qOwner := strings.TrimSpace(r.URL.Query().Get("owner"))
	workspaceID = strings.TrimSpace(r.URL.Query().Get("workspace"))
	if scope != "" {
		if qOwner != "" && qOwner != scope {
			return "", "", true
		}
		if qOwner == "" {
			qOwner = scope
		}
	}
	return qOwner, workspaceID, false
}

// AuthorizeSession implements [SessionDiagnosticsAuthorizer].
func (*ScopedOwnerAuthorizer) AuthorizeSession(r *http.Request, rec domain.Record, op SessionDiagOp, redactionDefault string) (AuthDecision, error) {
	_ = op
	scope := strings.TrimSpace(r.Header.Get(HeaderOwnerScope))
	if scope != "" && scope != strings.TrimSpace(rec.Owner.ID) {
		return AuthDecision{Allow: false, DenyAsNotFound: true}, nil
	}
	eff := mergeRedactionDefault(rec.Policy, redactionDefault)
	return AuthDecision{
		Allow:           true,
		EffectivePolicy: eff,
		RawAuditAllowed: app.RawAuditAllowed(rec.Policy),
	}, nil
}

func mergeRedactionDefault(pol domain.PolicyMetadata, redactionDefault string) domain.PolicyMetadata {
	out := pol
	defStrict := strings.EqualFold(strings.TrimSpace(redactionDefault), "strict")
	sessStrict := strings.EqualFold(strings.TrimSpace(pol.RedactionProfile), "strict")
	if defStrict || sessStrict {
		out.RedactionProfile = "strict"
	}
	return out
}
