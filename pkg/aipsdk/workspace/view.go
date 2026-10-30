package workspace

// WorkspaceView is a read-only workspace snapshot for safety and path policy plugins (design §2).
type WorkspaceView struct {
	// ID is an optional opaque workspace identifier when the resolver can provide one.
	ID          string `yaml:"id,omitempty"`
	ProjectRoot string
	DirtyTree   bool
	Markers     []string
	Labels      map[string]string
}
