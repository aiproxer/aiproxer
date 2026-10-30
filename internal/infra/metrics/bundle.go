package metrics

import (
	"database/sql"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/prometheus/client_golang/prometheus"
)

// Bundle is a dedicated Prometheus registry plus handles used by stdhttp and runtimebundle.
type Bundle struct {
	Registry *prometheus.Registry
	HTTP     *HTTPMetrics
	Executor *ExecutorProm
	// SecureSession is non-nil when metrics are enabled; secure-session begin/recorder/touch series.
	SecureSession *SecureSessionProm
	// ExtensionStages is non-nil when metrics are enabled; used for extension pipeline histograms/counters.
	ExtensionStages         *ExtensionStageProm
	SecretGuard             *SecretGuardProm
	AuthorityStages         *AuthorityStageProm
	Upstream                *UpstreamProm
	TokenAccounting         *TokenAccountingProm
	PostgresPool            *PostgresPoolProm
	TerminalWork            *TerminalWorkProm
	Reload                  *ReloadProm
	GeoIP                   *GeoIPProm
	ConversationView        *ConversationViewProm
	LargePayload            *LargePayloadProm
	SelfDefense             *SelfDefenseProm
	QuirkApplied            *QuirkAppliedProm
	PromptCacheCollapseProm *PromptCacheCollapseProm
	// PromptCacheCollapse is initialized by the process owner before publication.
	PromptCacheCollapse       *PromptCacheCollapse
	PromptCacheWriteChurnProm *PromptCacheWriteChurnProm
	// PromptCacheWriteChurn is initialized by the process owner before publication.
	PromptCacheWriteChurn *PromptCacheWriteChurn
	// SystemPromptLaunchSeq is the process-owned request launch ordinal source
	// shared by every generation executor. NewBundle creates it once, so a
	// reload or an overlapping retired generation never restarts the chronology
	// the prompt-cache collapse diagnostic compares. It carries no request
	// content and needs no closer.
	SystemPromptLaunchSeq *runtime.RequestLaunchSequence
	// seamDigestKey is the process-stable HMAC key for the seam-digest feed,
	// generated once by the process owner and borrowed (copied) by every
	// generation executor. It is never logged, persisted, or exposed; only
	// HMAC digests travel on usage samples.
	seamDigestKey       runtime.SeamDigestKey
	sink                runtime.MetricsSink
	tokenAccountingSink *TokenAccountingPromSink
	conversationSink    ConversationViewObserver
	largePayloadSink    largebody.DiagnosticsObserver
}

// NewBundle builds a registry with Go/process, inbound HTTP, executor, and upstream series.
// poolStats snapshots database/sql pool statistics for the postgres pool collector; it
// may be nil when no registry-owned pool exists (the collector then emits zeroed series).
// selfDefenseEntries reports the current bounded adaptive-source entry count for the
// ingress self-defense entry gauge; it may be nil, in which case the gauge reports zero.
func NewBundle(cfg *config.Config, poolStats func() []sql.DBStats, selfDefenseEntries func() int) *Bundle {
	r := NewRegistry()
	exemplars := cfg != nil && cfg.Observability.Metrics.ExemplarsEnabled
	httpm := RegisterHTTPMetrics(r, exemplars)
	exec := RegisterExecutorProm(r)
	ss := RegisterSecureSessionProm(r)
	ext := RegisterExtensionStageProm(r)
	sg := RegisterSecretGuardProm(r)
	auth := RegisterAuthorityStageProm(r)
	up := RegisterUpstreamProm(r, exemplars)
	tok := RegisterTokenAccountingProm(r)
	pg := RegisterPostgresPoolProm(r, poolStats)
	tw := RegisterTerminalWorkProm(r)
	reload := RegisterReloadProm(r)
	geoip := RegisterGeoIPProm(r)
	cv := RegisterConversationViewProm(r)
	lp := RegisterLargePayloadProm(r)
	sd := RegisterSelfDefenseProm(r)
	sd.SetEntryCountSource(selfDefenseEntries)
	return &Bundle{
		Registry:                  r,
		HTTP:                      httpm,
		Executor:                  exec,
		SecureSession:             ss,
		ExtensionStages:           ext,
		SecretGuard:               sg,
		AuthorityStages:           auth,
		Upstream:                  up,
		TokenAccounting:           tok,
		PostgresPool:              pg,
		TerminalWork:              tw,
		Reload:                    reload,
		GeoIP:                     geoip,
		ConversationView:          cv,
		LargePayload:              lp,
		SelfDefense:               sd,
		QuirkApplied:              RegisterQuirkAppliedProm(r),
		PromptCacheCollapseProm:   RegisterPromptCacheCollapseProm(r),
		PromptCacheWriteChurnProm: RegisterPromptCacheWriteChurnProm(r),
		SystemPromptLaunchSeq:     runtime.NewRequestLaunchSequence(),
		sink:                      NewExecutorPromSink(exec),
		tokenAccountingSink:       NewTokenAccountingPromSink(tok),
		conversationSink:          NewConversationViewSink(cv),
		largePayloadSink:          NewLargePayloadPromSink(lp),
	}
}

// ExecutorSink returns a [runtime.MetricsSink] backed by this bundle's executor metrics.
func (b *Bundle) ExecutorSink() runtime.MetricsSink {
	if b == nil {
		return nil
	}
	return b.sink
}

// SetSeamDigestKey installs the process-stable HMAC key for the seam-digest
// feed. Called once by the process owner at startup; generations borrow it
// via SeamDigestKey. Nil-safe so disabled-metrics builds stay keyless.
func (b *Bundle) SetSeamDigestKey(key runtime.SeamDigestKey) {
	if b == nil {
		return
	}
	b.seamDigestKey = key
}

// SeamDigestKey returns the process-stable HMAC key for the seam-digest
// feed. Generation executors borrow the copy; the key itself is never
// logged, persisted, or exposed.
func (b *Bundle) SeamDigestKey() runtime.SeamDigestKey {
	if b == nil {
		return runtime.SeamDigestKey{}
	}
	return b.seamDigestKey
}

// ExtensionStageSink returns an [extensions.StageMetrics] backed by this bundle's extension-stage series.
func (b *Bundle) ExtensionStageSink() extensions.StageMetrics {
	if b == nil {
		return nil
	}
	return NewExtensionStageSink(b.ExtensionStages)
}

// SecretGuardDecisionSink returns [extensions.SecretGuardDecisionMetrics] backed by this bundle.
func (b *Bundle) SecretGuardDecisionSink() extensions.SecretGuardDecisionMetrics {
	if b == nil {
		return nil
	}
	return NewSecretGuardDecisionSink(b.SecretGuard)
}

// AuthorityStageSink returns usage-authority stage metrics (req 16.5).
func (b *Bundle) AuthorityStageSink() usagelimit.StageMetrics {
	if b == nil {
		return nil
	}
	return NewAuthorityStageSink(b.AuthorityStages)
}

// SecureSessionMetricsSink returns a [runtime.SecureSessionMetrics] backed by this bundle, or nil when b is nil.
func (b *Bundle) SecureSessionMetricsSink() runtime.SecureSessionMetrics {
	if b == nil {
		return nil
	}
	return SecureSessionMetricsSink(b.SecureSession)
}

// TokenAccountingObservabilitySink returns a Prometheus sink for bounded token-accounting observations.
func (b *Bundle) TokenAccountingObservabilitySink() *TokenAccountingPromSink {
	if b == nil {
		return nil
	}
	return b.tokenAccountingSink
}

// ConversationViewObserver returns a bounded conversation-view observer (nil when metrics disabled).
func (b *Bundle) ConversationViewObserver() ConversationViewObserver {
	if b == nil {
		return nil
	}
	return b.conversationSink
}

// LargePayloadDiagnostics returns a bounded large-payload diagnostics observer.
// Wired into production HTTP runtime startup via buildProcessSpoolLedger and
// buildStandardLargePayloadConfig.
func (b *Bundle) LargePayloadDiagnostics() largebody.DiagnosticsObserver {
	if b == nil {
		return largebody.NoopDiagnosticsObserver{}
	}
	return b.largePayloadSink
}

var _ interface {
	Record(accountingobs.Observation)
} = (*TokenAccountingPromSink)(nil)
