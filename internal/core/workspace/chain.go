// Package workspace implements core-owned workspace resolution chains for the execution snapshot (design §9, R5).
package workspace

import (
	"context"
	"maps"
	"strings"

	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// ResolverChain runs workspace [aipworkspace.Resolver] entries in registration order with fail-open
// semantics: resolver errors are ignored and the next resolver runs.
type ResolverChain struct {
	list []aipworkspace.Resolver
}

// NewResolverChain returns a [aipworkspace.Resolver] backed by resolvers (nil entries skipped).
// An empty list returns [aipworkspace.DisabledResolver].
func NewResolverChain(resolvers []aipworkspace.Resolver) aipworkspace.Resolver {
	filtered := make([]aipworkspace.Resolver, 0, len(resolvers))
	for _, r := range resolvers {
		if r == nil {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		return aipworkspace.DisabledResolver{}
	}
	return ResolverChain{list: filtered}
}

// strictChain is like [ResolverChain] but propagates the first resolver error instead of fail-open
// skipping (used when secure_session.workspace_resolve_on_error is fail_closed).
type strictChain struct {
	list []aipworkspace.Resolver
}

// NewStrictChain returns a workspace resolver that runs entries in order and stops on the first error.
// Nil entries are skipped. An empty list returns [aipworkspace.DisabledResolver].
func NewStrictChain(resolvers []aipworkspace.Resolver) aipworkspace.Resolver {
	filtered := make([]aipworkspace.Resolver, 0, len(resolvers))
	for _, r := range resolvers {
		if r == nil {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		return aipworkspace.DisabledResolver{}
	}
	return strictChain{list: filtered}
}

// Resolve implements [aipworkspace.Resolver].
func (c strictChain) Resolve(ctx context.Context) (aipworkspace.WorkspaceView, error) {
	var out aipworkspace.WorkspaceView
	for _, r := range c.list {
		v, err := r.Resolve(ctx)
		if err != nil {
			return aipworkspace.WorkspaceView{}, err
		}
		out = mergeWorkspaceViews(out, v)
	}
	return out, nil
}

// Resolve implements [aipworkspace.Resolver].
func (c ResolverChain) Resolve(ctx context.Context) (aipworkspace.WorkspaceView, error) {
	var out aipworkspace.WorkspaceView
	for _, r := range c.list {
		v, err := r.Resolve(ctx)
		if err != nil {
			continue
		}
		out = mergeWorkspaceViews(out, v)
	}
	return out, nil
}

// mergeWorkspaceViews accumulates non-empty fields from resolvers. ID and ProjectRoot
// are overwritten when the additive view has a non-empty value (last writer wins
// in registration order).
func mergeWorkspaceViews(base, add aipworkspace.WorkspaceView) aipworkspace.WorkspaceView {
	if strings.TrimSpace(add.ID) != "" {
		base.ID = strings.TrimSpace(add.ID)
	}
	if strings.TrimSpace(add.ProjectRoot) != "" {
		base.ProjectRoot = strings.TrimSpace(add.ProjectRoot)
	}
	base.DirtyTree = base.DirtyTree || add.DirtyTree
	if len(add.Markers) > 0 {
		seen := make(map[string]struct{}, len(base.Markers)+len(add.Markers))
		for _, m := range base.Markers {
			seen[m] = struct{}{}
		}
		for _, m := range add.Markers {
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			base.Markers = append(base.Markers, m)
		}
	}
	if len(add.Labels) > 0 {
		if base.Labels == nil {
			base.Labels = make(map[string]string, len(add.Labels))
		}
		maps.Copy(base.Labels, add.Labels)
	}
	return base
}
