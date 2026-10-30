package controlplane

// HostCapabilities reports production wiring and active-generation identity
// derived from a live host (not cached facade booleans).
type HostCapabilities struct {
	ProductionMetering, TrafficObservers, UsageObservers bool
	ProductionEvidenceSink                               bool
	ProductionMeteringQuerier                            bool
	SnapshotGenerationID, ExecutableGenerationID         int64
	SnapshotUsageVersion, ExecutableVersion              string
	ExecutableState                                      CapabilityState
	ExecutableEvidenceObjectID                           string
}
