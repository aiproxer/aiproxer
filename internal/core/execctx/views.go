package execctx

import (
	"context"
	"maps"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type ctxKey int

const keyViews ctxKey = iota + 6200 // offset avoids collision with diag keys

// Views bundles typed snapshots for one request (design §2, R3).
// Scope is the authoritative principal/scope snapshot; Principal is the legacy compatibility
// projection derived from it. Annotations carry lifecycle-only notes kept separate from
// trusted attribution (requirements 4.2, 4.3, 4.6, 5.1).
type Views struct {
	Principal   execview.PrincipalView
	Scope       scope.PrincipalScopeView
	Session     session.SessionView
	Attempt     execview.AttemptView
	Workspace   workspace.WorkspaceView
	Annotations map[string]string
}

// WithViews returns a child context carrying v. String maps are copied so later mutation
// of the caller's maps does not affect stored snapshots.
// A nil parent is treated as [context.TODO] so the result is always non-nil.
func WithViews(ctx context.Context, v Views) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	v = copyViews(v)
	// Keep the internal aggregate for core consumers and project the same
	// defensive snapshots onto the public SDK context seams for feature
	// plugins. Session is always attached, including an empty view, so a
	// detached child cannot inherit the parent's authoritative session.
	ctx = context.WithValue(ctx, keyViews, v)
	ctx = session.WithSessionView(ctx, v.Session)
	if strings.TrimSpace(v.Session.AuthoritativeSessionID) == "" {
		ctx = session.WithoutSecureTurnPolicy(ctx)
	}
	ctx = scope.WithScope(ctx, v.Scope)
	ctx = execview.WithPrincipal(ctx, v.Principal)
	// Workspace is projected for the same reason, and with the same always-attach rule:
	// a request-part hook's metadata (sdkhooks.PartMeta) carries no workspace view, so
	// without this the late outbound stages of a feature plugin had no sanctioned way to
	// read the project root core had already pinned for the turn. It is the PINNED
	// snapshot, so a consumer that reads it derives from exactly the authority the early
	// stages read out of the same frozen views, and two workspace tags cannot reach one
	// request. An EMPTY view is attached too, so a child that pins no workspace - the
	// detached auxiliary path - cannot inherit a parent's through the context chain.
	ctx = workspace.WithWorkspaceView(ctx, v.Workspace)
	return ctx
}

// FromContext returns the views attached with [WithViews], if any. The returned Views is a
// deep copy of the stored snapshot so callers cannot mutate the stored scope, principal,
// session, workspace, or annotation slices/maps through the returned value (requirement 5.5).
func FromContext(ctx context.Context) (Views, bool) {
	if ctx == nil {
		return Views{}, false
	}
	raw := ctx.Value(keyViews)
	if raw == nil {
		return Views{}, false
	}
	v, ok := raw.(Views)
	if !ok {
		return Views{}, false
	}
	return copyViews(v), true
}

func copyViews(v Views) Views {
	if len(v.Principal.Claims) > 0 {
		v.Principal.Claims = maps.Clone(v.Principal.Claims)
	}
	if len(v.Principal.Roles) > 0 {
		v.Principal.Roles = append([]string(nil), v.Principal.Roles...)
	}
	v.Scope = v.Scope.Clone()
	if len(v.Session.Labels) > 0 {
		v.Session.Labels = maps.Clone(v.Session.Labels)
	}
	if len(v.Workspace.Labels) > 0 {
		v.Workspace.Labels = maps.Clone(v.Workspace.Labels)
	}
	if len(v.Workspace.Markers) > 0 {
		v.Workspace.Markers = append([]string(nil), v.Workspace.Markers...)
	}
	if len(v.Annotations) > 0 {
		v.Annotations = maps.Clone(v.Annotations)
	}
	return v
}
