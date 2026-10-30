package engine

// ComposeSource selects the secret source policy from effective access mode and
// feature enablement. Disabled performs no environment access. Multi-user never
// calls env (D4). Feature plugin YAML parsing remains Phase 4; callers pass
// composition-neutral options.
func ComposeSource(mode AccessMode, featureEnabled bool, env Environment, opts SingleUserOptions) (Source, error) {
	if !featureEnabled {
		return NewDisabledSource(), nil
	}
	switch mode {
	case ModeMultiUser:
		return NewMultiUserSource(env)
	default:
		return NewSingleUserSource(env, opts)
	}
}
