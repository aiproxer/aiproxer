package acp

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ToolUpdateSink receives ACP tool_call / tool_call_update payloads. The
// default text-summary sink keeps generated assistant text for connectors
// without structured consumers; specs opting into StructuredToolProvenance
// receive canonical lifecycle events instead. Nil means the mapper emits
// warnings or drops tool updates per SessionUpdateMapperOptions.
// FlushIncomplete closes calls that never reported completion; the stream is at
// EOF when it runs.
type ToolUpdateSink interface {
	HandleToolUpdate(ctx context.Context, kind string, update map[string]any) ([]aipapi.Event, error)
	FlushIncomplete() []aipapi.Event
}
