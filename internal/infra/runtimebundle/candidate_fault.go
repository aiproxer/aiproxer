package runtimebundle

// CandidateFaultInject is a test-only CompileCandidate/CompileGeneration failure seam.
type CandidateFaultInject struct {
	After string // model|prepare|activate|handler|composer-clone|ledger-transfer
	Hook  func()
}
