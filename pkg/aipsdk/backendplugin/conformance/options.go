package conformance

// Options customizes Run for connectors that need non-default configure YAML
// or protocol-honest execute proofs (for example ACP adapters without usage
// events, or vision advertised as input-only).
type Options struct {
	ConfigYAML []byte
	// FactoryKind selects which advertised factory to configure when a plugin
	// exports more than one kind. Empty uses Factories[0].
	FactoryKind string
	// SampleModel overrides the model id used in execute sample invocations.
	SampleModel string
	// DisableUsageRequirement skips the EventUsageDelta execute proof.
	DisableUsageRequirement bool
	// VisionInputOnly treats Vision as input capability: profile honesty still
	// applies, but execute need not emit assistant_image_ref.
	VisionInputOnly bool
	// SkipExecute skips execute.one_terminal_ordering (use only with a separate
	// stream proof suite in the connector module).
	SkipExecute bool
}
