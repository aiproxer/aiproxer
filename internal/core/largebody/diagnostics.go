package largebody

import "time"

// PipelineStage defines a bounded stage in the large-payload fast path
// (Task 19.1; Requirements 20, 22; design sections 2, 8).
type PipelineStage string

const (
	// StageConsidered marks candidate evaluation entry.
	StageConsidered PipelineStage = "considered"
	// StageStaticCanonical marks pre-capture static decline to canonical processing.
	StageStaticCanonical PipelineStage = "static_canonical"
	// StageCaptured marks successful body capture into replay storage.
	StageCaptured PipelineStage = "captured"
	// StageProfileProven marks successful protocol proof compilation and validation.
	StageProfileProven PipelineStage = "profile_proven"
	// StageAssessmentEligible marks side-effect-free assessment acceptance.
	StageAssessmentEligible PipelineStage = "assessment_eligible"
	// StageWire marks one-way wire commit execution.
	StageWire PipelineStage = "wire"
	// StageCanonical marks canonical pipeline execution (direct or fallback).
	StageCanonical PipelineStage = "canonical"
)

// SizeBucket returns a bounded static label for payload size in bytes (Requirement 22.2).
// Buckets: lt_32k, 32k_256k, 256k_1m, 1m_5m, 5m_20m, gte_20m.
func SizeBucket(sizeBytes int64) string {
	switch {
	case sizeBytes < 32*1024:
		return "lt_32k"
	case sizeBytes < 256*1024:
		return "32k_256k"
	case sizeBytes < 1024*1024:
		return "256k_1m"
	case sizeBytes < 5*1024*1024:
		return "1m_5m"
	case sizeBytes < 20*1024*1024:
		return "5m_20m"
	default:
		return "gte_20m"
	}
}

// StorageKind classifies capture storage (memory vs file spill).
type StorageKind string

const (
	// StorageMemory indicates body was held entirely within the in-memory window.
	StorageMemory StorageKind = "memory"
	// StorageFile indicates body spilled to a secure temporary spill file.
	StorageFile StorageKind = "file"
)

// Static decline reasons (bounded taxonomy per Task 19.1 and Requirements 20, 22).
const (
	DeclineReasonLocalTurn             = "local_turn"
	DeclineReasonSecretGuard           = "secret_guard"
	DeclineReasonTerminalDecision      = "terminal_decision"
	DeclineReasonFrontendRouteResolver = "frontend_route_resolver"
	DeclineReasonTraffic               = "traffic"
	DeclineReasonAccountingCounting    = "accounting_counting"
	DeclineReasonCustomCallCallback    = "custom_call_callback"
	DeclineReasonBackendDomain         = "backend_domain"
	DeclineReasonFeatureDisabled       = "feature_disabled"
	DeclineReasonBelowThreshold        = "below_threshold"
	DeclineReasonGzipCompressed        = "gzip_compressed"
	DeclineReasonStaticBlocker         = "static_blocker"
	DeclineReasonSpoolBudgetExhausted  = "spool_budget_exhausted"
	DeclineReasonLimitExceeded         = "limit_exceeded"
	DeclineReasonReadError             = "read_error"
)

// DiagnosticsObserver is the pure diagnostic observation interface for the large payload lane.
// Implementations must not retain prompt bodies, file paths, or sensitive tokens (Requirement 22).
type DiagnosticsObserver interface {
	OnPipelineStage(stage PipelineStage)
	OnDecline(reason string)
	OnCapture(sizeBucket string, storage StorageKind)
	OnReplay()
	OnRewrite()
	OnStageDuration(stage string, d time.Duration)
	OnActiveSpoolBytes(seq uint64, bytes int64)
}

// SpoolObserver receives active spool byte updates from SpoolLedger.
type SpoolObserver interface {
	OnActiveSpoolBytes(seq uint64, bytes int64)
}

// NoopDiagnosticsObserver implements DiagnosticsObserver with no-op operations.
type NoopDiagnosticsObserver struct{}

func (NoopDiagnosticsObserver) OnPipelineStage(PipelineStage)         {}
func (NoopDiagnosticsObserver) OnDecline(string)                      {}
func (NoopDiagnosticsObserver) OnCapture(string, StorageKind)         {}
func (NoopDiagnosticsObserver) OnReplay()                             {}
func (NoopDiagnosticsObserver) OnRewrite()                            {}
func (NoopDiagnosticsObserver) OnStageDuration(string, time.Duration) {}
func (NoopDiagnosticsObserver) OnActiveSpoolBytes(uint64, int64)      {}

// SafeDiagnosticsObserver wraps a DiagnosticsObserver and guards against nil receivers.
type SafeDiagnosticsObserver struct {
	inner DiagnosticsObserver
}

// NewSafeDiagnosticsObserver returns a non-nil DiagnosticsObserver wrapping inner.
func NewSafeDiagnosticsObserver(inner DiagnosticsObserver) DiagnosticsObserver {
	if inner == nil {
		return NoopDiagnosticsObserver{}
	}
	return &SafeDiagnosticsObserver{inner: inner}
}

func (s *SafeDiagnosticsObserver) OnPipelineStage(stage PipelineStage) {
	if s != nil && s.inner != nil {
		s.inner.OnPipelineStage(stage)
	}
}

func (s *SafeDiagnosticsObserver) OnDecline(reason string) {
	if s != nil && s.inner != nil {
		s.inner.OnDecline(reason)
	}
}

func (s *SafeDiagnosticsObserver) OnCapture(sizeBucket string, storage StorageKind) {
	if s != nil && s.inner != nil {
		s.inner.OnCapture(sizeBucket, storage)
	}
}

func (s *SafeDiagnosticsObserver) OnReplay() {
	if s != nil && s.inner != nil {
		s.inner.OnReplay()
	}
}

func (s *SafeDiagnosticsObserver) OnRewrite() {
	if s != nil && s.inner != nil {
		s.inner.OnRewrite()
	}
}

func (s *SafeDiagnosticsObserver) OnStageDuration(stage string, d time.Duration) {
	if s != nil && s.inner != nil {
		s.inner.OnStageDuration(stage, d)
	}
}

func (s *SafeDiagnosticsObserver) OnActiveSpoolBytes(seq uint64, bytes int64) {
	if s != nil && s.inner != nil {
		s.inner.OnActiveSpoolBytes(seq, bytes)
	}
}

// ClassifySummaryBlocker maps a WireEligibilitySummary with static blockers to a specific static decline reason.
func ClassifySummaryBlocker(summary WireEligibilitySummary) string {
	if !summary.Sealed() {
		return DeclineReasonStaticBlocker
	}
	// Check plane blockers:
	// Local Turn
	if idx, ok := WireEligibilityPlaneIndex("local_turn_handlers"); ok && (summary.PlaneBlockers()&(1<<idx)) != 0 {
		return DeclineReasonLocalTurn
	}
	// Secret Guard
	if idx, ok := WireEligibilityPlaneIndex("secret_guards"); ok && (summary.PlaneBlockers()&(1<<idx)) != 0 {
		return DeclineReasonSecretGuard
	}
	if idx, ok := WireEligibilityPlaneIndex("secret_guard_execution"); ok && (summary.PlaneBlockers()&(1<<idx)) != 0 {
		return DeclineReasonSecretGuard
	}
	// Terminal Decision
	if idx, ok := WireEligibilityPlaneIndex("terminal_decision_provider"); ok && (summary.PlaneBlockers()&(1<<idx)) != 0 {
		return DeclineReasonTerminalDecision
	}
	// Check port blockers:
	ports := summary.PortBlockers()
	if ports&WirePortTrafficCapturing != 0 {
		return DeclineReasonTraffic
	}
	if ports&(WirePortTokenCounting|WirePortPreflightCountOnly|WirePortStreamUsage|WirePortAdminCountService) != 0 {
		return DeclineReasonAccountingCounting
	}
	if ports&(WirePortCustomCallCallbacks|WirePortBillingIdentityCallbacks) != 0 {
		return DeclineReasonCustomCallCallback
	}
	if ports&WirePortBackendsEmpty != 0 {
		return DeclineReasonBackendDomain
	}
	return DeclineReasonStaticBlocker
}

// ResolveStaticDeclineReason maps static pre-capture gate reasons and summary blockers to a bounded diagnostic decline reason string.
func ResolveStaticDeclineReason(summary WireEligibilitySummary, reason StaticWireReason) string {
	switch reason {
	case StaticWireReasonFeatureDisabled:
		return DeclineReasonFeatureDisabled
	case StaticWireReasonBelowThreshold:
		return DeclineReasonBelowThreshold
	case StaticWireReasonGzipCompressed:
		return DeclineReasonGzipCompressed
	case StaticWireReasonLegacyResolverConfigured:
		return DeclineReasonFrontendRouteResolver
	case StaticWireReasonStaticBlocker:
		return ClassifySummaryBlocker(summary)
	default:
		if summary.HasStaticBlocker() {
			return ClassifySummaryBlocker(summary)
		}
		return "unknown"
	}
}
