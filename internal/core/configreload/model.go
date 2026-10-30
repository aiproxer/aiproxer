package configreload

// Canonical reload vocabulary lives in pkg/aipsdk/configreload.
// This package owns stage constants plus policy/history/sanitization algorithms
// that are not part of the public contract. Consumers of trigger/result/status
// shapes import the SDK package directly.

// Stage names used in ReasonCategory / diagnostics (bounded, non-secret).
const (
	StageRead      = "read"
	StageLoad      = "load"
	StageNoop      = "noop"
	StageClassify  = "classify"
	StageCompile   = "compile"
	StagePrepare   = "prepare"
	StageRetention = "retention"
	StagePublish   = "publish"
	StageRollback  = "rollback"
	StageShutdown  = "shutdown"
	StageBusy      = "busy"
	StageCoalesce  = "coalesce"
	StagePanic     = "panic"
)
