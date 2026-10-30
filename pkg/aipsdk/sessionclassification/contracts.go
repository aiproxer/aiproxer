package sessionclassification

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// ToolCategorySet is a fixed bitset of canonical tool-name categories.
type ToolCategorySet uint16

const (
	// ToolCategoryFileRead records at least one canonical file-read tool.
	ToolCategoryFileRead ToolCategorySet = 1 << iota
	// ToolCategoryFileSearch records at least one canonical file-search tool.
	ToolCategoryFileSearch
	// ToolCategoryOSCommand records at least one canonical operating-system command tool.
	ToolCategoryOSCommand
	// ToolCategoryFileEdit records at least one canonical file-edit tool.
	ToolCategoryFileEdit
	// ToolCategoryFileRemove records at least one canonical file-removal tool.
	ToolCategoryFileRemove
	// ToolCategoryWebAccess records at least one canonical web-access tool.
	ToolCategoryWebAccess
	// ToolCategoryUnknownSeen records at least one unrecognized tool name.
	ToolCategoryUnknownSeen
)

// ToolCategorySetBytes is the fixed in-memory width of ToolCategorySet. It is
// the constant part of the bounded evidence cost: presence bits never grow with
// tool count, body size or prompt material.
const ToolCategorySetBytes = 2

// DefinedToolCategoryBits is every category bit AddToolName can derive. Any
// other bit is a carrier defect rather than new evidence, so bounded carriers
// reject it instead of forwarding an undefined classification signal.
const DefinedToolCategoryBits = ToolCategoryFileRead | ToolCategoryFileSearch |
	ToolCategoryOSCommand | ToolCategoryFileEdit | ToolCategoryFileRemove |
	ToolCategoryWebAccess | ToolCategoryUnknownSeen

// AddToolName returns the set with the canonical category for name added.
// The name itself is not retained; unknown names set ToolCategoryUnknownSeen.
func (s ToolCategorySet) AddToolName(name string) ToolCategorySet {
	category, _ := aipapi.ClassifyToolName(name)
	switch category {
	case aipapi.ToolCategoryFileRead:
		return s | ToolCategoryFileRead
	case aipapi.ToolCategoryFileSearch:
		return s | ToolCategoryFileSearch
	case aipapi.ToolCategoryOSCommand:
		return s | ToolCategoryOSCommand
	case aipapi.ToolCategoryFileEdit:
		return s | ToolCategoryFileEdit
	case aipapi.ToolCategoryFileRemove:
		return s | ToolCategoryFileRemove
	case aipapi.ToolCategoryWebAccess:
		return s | ToolCategoryWebAccess
	default:
		return s | ToolCategoryUnknownSeen
	}
}

// Evidence carries accepted request metadata for one classification evaluation.
// ClientUserAgent must already satisfy the canonical identity acceptance and
// length policy; this SDK contract does not parse headers or define a second
// acceptance policy. Classifiers must not persist the raw User-Agent.
type Evidence struct {
	Operation       aipapi.Operation
	ClientUserAgent string
	ToolCategories  ToolCategorySet
}

// IsZero reports whether no evidence was compiled for this turn. Callers treat
// absent evidence as "stay unknown": they must not infer a negative
// classification from it (requirements 5.4, 12.7).
func (e Evidence) IsZero() bool {
	return e.Operation == "" && e.ClientUserAgent == "" && e.ToolCategories == 0
}

// MetadataBytes returns the bounded in-memory byte cost that a carrier holding
// this evidence must charge: the accepted client identity length plus the fixed
// category-bitset width.
//
// The cost never scales with tool count, body size or request content. The
// operation is a copy of an operation the carrier already holds, so it is
// deliberately not counted twice here.
func (e Evidence) MetadataBytes() int64 {
	return int64(len(e.ClientUserAgent)) + ToolCategorySetBytes
}

// Input is the bounded metadata snapshot presented to a session classifier.
// Session and Workspace are read-only projections. Classifiers may inspect
// bounded workspace markers and labels but must not persist ProjectRoot. No
// request Call, transcript, tool definitions, raw header map, or route state is
// included.
type Input struct {
	TraceID   string
	Session   session.SessionView
	Workspace workspace.WorkspaceView
	Evidence  Evidence
}

// Classifier classifies the current bounded session metadata snapshot.
type Classifier interface {
	// ID returns the stable identifier of this classifier implementation.
	ID() string
	// Classify evaluates one bounded metadata snapshot and returns its projection.
	Classify(context.Context, Input) (session.Classification, error)
}
