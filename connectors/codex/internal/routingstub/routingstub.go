package routingstub

// AttemptCandidate is a minimal routing stub for connector-local Codex payload build.
type AttemptCandidate struct {
	Primary Primary
}

type Primary struct {
	Model string
}
