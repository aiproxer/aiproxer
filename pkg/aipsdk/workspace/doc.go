// Package workspace holds workspace resolution contracts and views (design §2, §9, §16),
// plus the [WithWorkspaceView] / [WorkspaceViewFromContext] context projection that lets
// an SDK consumer read the proxy's PINNED workspace snapshot at a stage whose own
// metadata carries none.
package workspace
