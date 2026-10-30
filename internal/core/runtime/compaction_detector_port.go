package runtime

import (
	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// CompactionDetector is the repository-internal consumer port for session
// compaction detection. It exposes only the four observation operations
// consumed by the core runtime, using canonical aipapi and compaction SDK types.
type CompactionDetector interface {
	PreviewRequest(compaction.PreservationMeta, aipapi.Call) compaction.RequestPreview
	RequestOpened(compaction.PreservationMeta, aipapi.Call) []compaction.Event
	PreviewResponse(compaction.PreservationMeta, aipapi.Event) compaction.ResponsePreview
	ResponseReleased(compaction.PreservationMeta, aipapi.Event) []compaction.Event
}

// CompactionWireDetector extends CompactionDetector with exact-facts request observation
// for wire streaming execution without materializing a full aipapi.Call.
type CompactionWireDetector interface {
	CompactionDetector
	RequestOpenedFacts(compaction.PreservationMeta, compactionfacts.RequestFacts) []compaction.Event
}
