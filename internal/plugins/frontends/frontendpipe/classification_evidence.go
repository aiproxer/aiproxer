package frontendpipe

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/identitywire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// CompileClassificationEvidence compiles the bounded classification evidence a
// certified profile puts into its large-body proof.
//
// A classification-enabled large request must stay wire-eligible, so the wire
// path may not materialize a canonical aipapi.Call just to classify the turn.
// Instead it derives exactly the evidence the canonical path derives, using the
// very same two helpers, so both paths agree bit-for-bit for one client turn
// (requirements 5.2, 5.3, 11.1, 11.2, 12.7):
//
//   - the accepted client identity is lifted by identitywire, the frontend
//     identity helper every canonical decoder uses. The wire compiler therefore
//     applies one acceptance policy rather than a second, looser one;
//   - the tool catalog is reduced by the shared fixed-bit SDK accumulator
//     sessionclassification.ToolCategorySet.AddToolName, which maps names through
//     the canonical aipapi.ClassifyToolName, so the wire path cannot drift into a
//     second coding-tool taxonomy (requirements 3.9, 11.2).
//
// tools are the definitions the profile has already scanned and validated in its
// own tool shape; only their names are read, and only presence bits survive. The
// ToolDef slice, tool descriptions and tool parameter schemas never reach the
// returned value, so the compiled evidence stays fixed-width apart from the
// bounded accepted identity string (requirements 5.2, 5.5).
//
// An absent User-Agent is legal and yields an empty identity. A User-Agent the
// shared policy rejects is dropped rather than truncated, which is what keeps the
// wire and canonical derivations identical for the same header bytes. Unknown
// tool names are legal and only record the unknown-seen bit.
func CompileClassificationEvidence(
	operation aipapi.Operation,
	headers http.Header,
	tools []aipapi.ToolDef,
) sessionclassification.Evidence {
	categories := sessionclassification.ToolCategorySet(0)
	for _, tool := range tools {
		categories = categories.AddToolName(tool.Name)
	}

	// The accepted identity is captured through the frontend identity helper on a
	// throwaway invocation rather than by re-reading the header here, so there is
	// exactly one place that decides which client identities are admissible.
	var invocation aipapi.Invocation
	identitywire.CaptureClientUserAgent(&invocation, headers)

	return sessionclassification.Evidence{
		Operation:       operation,
		ClientUserAgent: invocation.ClientUserAgent,
		ToolCategories:  categories,
	}
}
