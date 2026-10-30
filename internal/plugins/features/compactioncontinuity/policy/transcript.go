package policy

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// TranscriptAuthorizationFromContext returns the already-authorized scope for
// an optional transcript read. It refuses missing secure-session authority or
// transcript-disabled policy, and it does not fall back to client hints.
func TranscriptAuthorizationFromContext(ctx context.Context) (TranscriptAuthorization, bool) {
	views, trusted := trustedViews(ctx)
	if !trusted {
		return TranscriptAuthorization{}, false
	}
	turn, ok := session.SecureTurnPolicyFromContext(ctx)
	if !ok || !turn.TranscriptEnabled {
		return TranscriptAuthorization{}, false
	}
	trustedScope, _ := scope.ScopeFromContext(ctx)
	principal := strings.TrimSpace(trustedScope.PrincipalID.String())
	if principal == "" && trustedScope.SubjectKind != scope.SubjectLocal {
		return TranscriptAuthorization{}, false
	}
	sessionWorkspace := strings.TrimSpace(views.WorkspaceID)
	scopeWorkspace := strings.TrimSpace(trustedScope.WorkspaceID.String())
	if sessionWorkspace != "" && scopeWorkspace != "" && sessionWorkspace != scopeWorkspace {
		return TranscriptAuthorization{}, false
	}
	workspaceID := sessionWorkspace
	if workspaceID == "" {
		workspaceID = scopeWorkspace
	}
	tenantID := strings.TrimSpace(trustedScope.TenantID.String())
	if tenantID == "" && workspaceID == "" {
		return TranscriptAuthorization{}, false
	}
	return TranscriptAuthorization{
		SessionID:   strings.TrimSpace(views.AuthoritativeSessionID),
		PrincipalID: principal,
		TenantID:    tenantID,
		WorkspaceID: workspaceID,
	}, true
}

// AuthorizeTranscriptWorkspace preserves the existing tenant/workspace scope
// when a reader asks for a specific workspace. Empty requested scope means the
// caller must use the session's already-authorized workspace.
func AuthorizeTranscriptWorkspace(ctx context.Context, requestedWorkspace string) bool {
	return AuthorizeTranscriptScope(ctx, "", requestedWorkspace)
}

// AuthorizeTranscriptScope checks both tenant and workspace ownership. Empty
// requested values mean "use the already-authorized session scope"; a caller
// cannot broaden a read by omitting one dimension.
func AuthorizeTranscriptScope(ctx context.Context, requestedTenant, requestedWorkspace string) bool {
	auth, ok := TranscriptAuthorizationFromContext(ctx)
	if !ok {
		return false
	}
	requestedTenant = strings.TrimSpace(requestedTenant)
	if requestedTenant != "" && (auth.TenantID == "" || requestedTenant != auth.TenantID) {
		return false
	}
	requestedWorkspace = strings.TrimSpace(requestedWorkspace)
	if requestedWorkspace == "" {
		return auth.WorkspaceID != "" || auth.TenantID != ""
	}
	return auth.WorkspaceID == requestedWorkspace
}
