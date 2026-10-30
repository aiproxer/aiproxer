package metrics

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/prometheus/client_golang/prometheus"
)

// CacheDiscontinuityKind is the bounded cache-discontinuity operation recorded for steering mutations.
type CacheDiscontinuityKind string

const (
	CacheDiscontinuityNone       CacheDiscontinuityKind = "none"
	CacheDiscontinuityCreate     CacheDiscontinuityKind = "create"
	CacheDiscontinuityReplace    CacheDiscontinuityKind = "replace"
	CacheDiscontinuityMove       CacheDiscontinuityKind = "move"
	CacheDiscontinuityDeactivate CacheDiscontinuityKind = "deactivate"
)

func (k CacheDiscontinuityKind) Validate() error {
	switch k {
	case CacheDiscontinuityNone, CacheDiscontinuityCreate, CacheDiscontinuityReplace, CacheDiscontinuityMove, CacheDiscontinuityDeactivate:
		return nil
	default:
		return fmt.Errorf("unknown cache discontinuity kind %q", k)
	}
}

// ConversationViewObserver is the narrow diagnostics observer interface for conversation-view metrics.
type ConversationViewObserver interface {
	conversationprojection.Observer
	OnSteeringMutation(kind CacheDiscontinuityKind, placement conversationprojection.PlacementKind)
}

// ConversationViewProm holds Prometheus collectors for bounded conversation-view diagnostics.
// All labels are bounded enums (operation, placement, policy, stage), never OverlayID/ALegID/digest/plaintext.
type ConversationViewProm struct {
	filtered       *prometheus.CounterVec
	injected       *prometheus.CounterVec
	mutations      *prometheus.CounterVec
	anchorFallback *prometheus.CounterVec
	anchorFailure  *prometheus.CounterVec
	projectionFail *prometheus.CounterVec
	discontinuity  *prometheus.CounterVec
}

// RegisterConversationViewProm registers aip_conversation_view_* series on reg.
func RegisterConversationViewProm(reg prometheus.Registerer) *ConversationViewProm {
	m := &ConversationViewProm{
		filtered: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_filtered_tags_total",
				Help:      "Tags filtered by never_backend (bounded stage).",
			},
			[]string{"stage"},
		),
		injected: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_injected_overlays_total",
				Help:      "Steering overlays injected by placement class (bounded).",
			},
			[]string{"placement"},
		),
		mutations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_steering_mutations_total",
				Help:      "Steering mutations by operation and placement (bounded).",
			},
			[]string{"operation", "placement"},
		),
		anchorFallback: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_anchor_fallbacks_total",
				Help:      "Anchor missing fallbacks by stage and policy (bounded).",
			},
			[]string{"stage", "policy"},
		),
		anchorFailure: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_anchor_failures_total",
				Help:      "Anchor failures by policy (bounded policy).",
			},
			[]string{"policy"},
		),
		projectionFail: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_projection_failures_total",
				Help:      "Projection/reassert failures by stage (bounded stage).",
			},
			[]string{"stage"},
		),
		discontinuity: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "conversation_view_cache_discontinuities_total",
				Help:      "Explicit cache discontinuities by operation and placement (bounded).",
			},
			[]string{"operation", "placement"},
		),
	}
	reg.MustRegister(m.filtered, m.injected, m.mutations, m.anchorFallback, m.anchorFailure, m.projectionFail, m.discontinuity)
	return m
}

type conversationViewSink struct {
	p *ConversationViewProm
}

// NewConversationViewSink adapts ConversationViewProm to ConversationViewObserver.
func NewConversationViewSink(p *ConversationViewProm) ConversationViewObserver {
	if p == nil {
		return nil
	}
	return &conversationViewSink{p: p}
}

func sanitizePlacement(k conversationprojection.PlacementKind) string {
	switch k {
	case conversationprojection.PlacementStablePrefix, conversationprojection.PlacementAfterMessage:
		return string(k)
	default:
		return "unknown"
	}
}

func sanitizeOperation(k CacheDiscontinuityKind) string {
	switch k {
	case CacheDiscontinuityCreate, CacheDiscontinuityReplace, CacheDiscontinuityMove, CacheDiscontinuityDeactivate:
		return string(k)
	default:
		return "unknown"
	}
}

func sanitizePolicy(p conversationprojection.AnchorMissingPolicy) string {
	switch p {
	case conversationprojection.AnchorStablePrefixFallback, conversationprojection.AnchorFailClosed:
		return string(p)
	default:
		return "unknown"
	}
}

func sanitizeStage(s string) string {
	switch s {
	case conversationprojection.StageEarly, conversationprojection.StageFinal, conversationprojection.StageSDKResolve:
		return s
	default:
		return "unknown"
	}
}

func (s *conversationViewSink) OnProjection(stage string, summary conversationprojection.ProjectionSummary) {
	if s == nil || s.p == nil {
		return
	}
	st := sanitizeStage(stage)
	if summary.FilteredCount > 0 {
		s.p.filtered.WithLabelValues(st).Add(float64(summary.FilteredCount))
	}
	if summary.StablePrefixCount > 0 {
		s.p.injected.WithLabelValues(string(conversationprojection.PlacementStablePrefix)).Add(float64(summary.StablePrefixCount))
	}
	if summary.AfterMessageCount > 0 {
		s.p.injected.WithLabelValues(string(conversationprojection.PlacementAfterMessage)).Add(float64(summary.AfterMessageCount))
	}
}

func (s *conversationViewSink) OnProjectionFailure(stage string) {
	if s == nil || s.p == nil {
		return
	}
	s.p.projectionFail.WithLabelValues(sanitizeStage(stage)).Inc()
}

func (s *conversationViewSink) OnAnchorFallback(stage string, policy conversationprojection.AnchorMissingPolicy) {
	if s == nil || s.p == nil {
		return
	}
	s.p.anchorFallback.WithLabelValues(sanitizeStage(stage), sanitizePolicy(policy)).Inc()
}

func (s *conversationViewSink) OnAnchorFailure(policy conversationprojection.AnchorMissingPolicy) {
	if s == nil || s.p == nil {
		return
	}
	s.p.anchorFailure.WithLabelValues(sanitizePolicy(policy)).Inc()
}

func (s *conversationViewSink) OnSteeringMutation(kind CacheDiscontinuityKind, placement conversationprojection.PlacementKind) {
	if s == nil || s.p == nil {
		return
	}
	op := sanitizeOperation(kind)
	pl := sanitizePlacement(placement)
	s.p.mutations.WithLabelValues(op, pl).Inc()
}
