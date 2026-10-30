package workspace

import (
	"context"
	"maps"
	"slices"
)

type workspaceViewContextKey struct{}

// WithWorkspaceView attaches a defensive copy of the proxy-authoritative workspace
// snapshot to ctx.
//
// It is the workspace member of the projection trio core already publishes for feature
// plugins, alongside [github.com/aiproxer/aiproxer/pkg/aipsdk/session.WithSessionView],
// [github.com/aiproxer/aiproxer/pkg/aipsdk/scope.WithScope], and
// [github.com/aiproxer/aiproxer/pkg/aipsdk/execview.WithPrincipal], and it
// exists because a plugin reached at a stage whose own metadata carries no workspace
// view otherwise has no sanctioned way to read the authoritative project root: the SDK
// offers [Resolver] for a stage that may RESOLVE, and a projection for a stage that may
// only READ what the proxy already decided.
//
// The attached value is a PINNED snapshot, not a resolution instruction. Reading it
// cannot reach a host, run an I/O boundary, or observe a project root other than the one
// the proxy already projected for this request, so two participants reading the same
// context cannot disagree about which workspace a request belongs to.
//
// A nil parent is treated as [context.TODO], so the result is always non-nil, and the
// view is cloned so a later mutation of the caller's maps or slices cannot change the
// stored snapshot.
func WithWorkspaceView(ctx context.Context, view WorkspaceView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, workspaceViewContextKey{}, cloneWorkspaceView(view))
}

// WorkspaceViewFromContext returns a defensive copy of the proxy-authoritative workspace
// snapshot attached with [WithWorkspaceView], if any.
//
// The second result is false when nothing was projected. That is deliberately distinct
// from an attached EMPTY view, which is a genuine view that simply has no project root,
// so a consumer can tell "no authority" apart from "an authority with nothing in it".
// A nil ctx is tolerated and returns (WorkspaceView{}, false).
func WorkspaceViewFromContext(ctx context.Context) (WorkspaceView, bool) {
	if ctx == nil {
		return WorkspaceView{}, false
	}
	view, ok := ctx.Value(workspaceViewContextKey{}).(WorkspaceView)
	if !ok {
		return WorkspaceView{}, false
	}
	return cloneWorkspaceView(view), true
}

// cloneWorkspaceView detaches the view's two reference-typed payloads. It is the
// producer-side copy the same way session's is: neither public view type exposes a Clone
// method, so the projection owns detachment for the value it stores.
func cloneWorkspaceView(view WorkspaceView) WorkspaceView {
	view.Labels = maps.Clone(view.Labels)
	view.Markers = slices.Clone(view.Markers)
	return view
}
