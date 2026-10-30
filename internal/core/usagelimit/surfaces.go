package usagelimit

import "github.com/aiproxer/aiproxer/pkg/aipsdk/authority"

// StageMetricsSetter is the optional post-construction stage-metrics surface.
// The host attaches the process metrics sink once, before serving.
type StageMetricsSetter interface{ SetStageMetrics(StageMetrics) }

// EvidenceSinkSetter is the optional post-construction evidence surface. The
// host attaches the process evidence sink once, before serving; a nil sink is
// never attached and never clears a producer-installed sink.
type EvidenceSinkSetter interface{ SetEvidenceSink(authority.EvidenceSink) }
