package workspace

import (
	"context"
	"errors"
)

// ErrResolverNotConfigured means workspace resolution is not bound for this execution snapshot.
var ErrResolverNotConfigured = errors.New("aipsdk/workspace: workspace resolver not configured")

// Resolver supplies the resolved workspace snapshot for the active request (design §9).
type Resolver interface {
	Resolve(ctx context.Context) (WorkspaceView, error)
}

// DisabledResolver returns [ErrResolverNotConfigured] until core workspace plumbing exists.
type DisabledResolver struct{}

func (DisabledResolver) Resolve(context.Context) (WorkspaceView, error) {
	return WorkspaceView{}, ErrResolverNotConfigured
}
