package feature

// Stable stage IDs for the legal extension pipeline (R2, ADR 0006). Core and
// diagnostics use the same strings; plugins must not invent ad hoc stage names.
const (
	StageIDTransportAuth             = "transport_authentication"
	StageIDSessionOpen               = "session_open"
	StageIDSecretGuard               = "secret_guard"
	StageIDSessionClassification     = "session_classification"
	StageIDSubmit                    = "submit_request"
	StageIDToolCatalog               = "tool_catalog_filter"
	StageIDRequestWide               = "request_wide_shaping"
	StageIDPreRequest                = "pre_request_admission"
	StageIDRouteHinting              = "route_hinting"
	StageIDCandidateAttemptTransform = "candidate_attempt_transform"
	StageIDAttemptLifecycle          = "attempt_lifecycle"
	StageIDStreamEventMutation       = "stream_event_mutation"
	StageIDToolEventReaction         = "tool_event_reaction"
	StageIDCompletionGating          = "completion_gating"
	StageIDFinalStreamObservation    = "final_stream_observation"
	StageIDTrafficObservation        = "traffic_observation"
	StageIDEgressEncoding            = "egress_encoding"
)

// StageMutationRole documents what a stage may do to traffic or control flow (R2).
type StageMutationRole uint8

const (
	// StageRoleUnknown is the zero value: invalid or unset until assigned from the legal descriptor table.
	StageRoleUnknown StageMutationRole = iota
	StageRoleMutate
	StageRoleReject
	StageRoleObserve
	StageRoleReplace
	StageRoleMutateReject
)

// StageDescriptor is a compile-time description of one legal pipeline stage.
type StageDescriptor struct {
	ID            string
	MutationRole  StageMutationRole
	R12LayerNotes string
}

var legalStageDescriptors = []StageDescriptor{
	{ID: StageIDTransportAuth, MutationRole: StageRoleReject, R12LayerNotes: "Transport identity; distinct from canonical call mutation (R12)."},
	{ID: StageIDSessionOpen, MutationRole: StageRoleMutate, R12LayerNotes: "Session/workspace context before request shaping."},
	{ID: StageIDSecretGuard, MutationRole: StageRoleMutateReject, R12LayerNotes: "Ingress secret detection after BeginTurn; redact or reject before FE checkpoint/traffic/routing."},
	{ID: StageIDSessionClassification, MutationRole: StageRoleObserve, R12LayerNotes: "Bounded session metadata classification after secret guard; updates only session metadata."},
	{ID: StageIDSubmit, MutationRole: StageRoleMutateReject, R12LayerNotes: "Submit-time whole-call mutation and rejection."},
	{ID: StageIDToolCatalog, MutationRole: StageRoleMutate, R12LayerNotes: "Tool exposure policy before tool-use events."},
	{ID: StageIDRequestWide, MutationRole: StageRoleMutate, R12LayerNotes: "Request-wide shaping; brownfield request-part hooks map here until a dedicated stage exists."},
	{ID: StageIDPreRequest, MutationRole: StageRoleReject, R12LayerNotes: "Admission checks after canonical request shaping and before model route planning."},
	{ID: StageIDRouteHinting, MutationRole: StageRoleObserve, R12LayerNotes: "Advisory hints; core-owned routing stays authoritative."},
	{ID: StageIDCandidateAttemptTransform, MutationRole: StageRoleMutateReject, R12LayerNotes: "Per-candidate attempt transforms after interleaved shaping; continue or exclude_candidate before final capabilities."},
	{ID: StageIDAttemptLifecycle, MutationRole: StageRoleObserve, R12LayerNotes: "Attempt-scoped observers and lifecycle."},
	{ID: StageIDStreamEventMutation, MutationRole: StageRoleMutate, R12LayerNotes: "Per-event stream mutation; distinct from completion-wide gates (R12)."},
	{ID: StageIDToolEventReaction, MutationRole: StageRoleMutateReject, R12LayerNotes: "Tool-use enforcement separate from catalog filtering (R9)."},
	{ID: StageIDCompletionGating, MutationRole: StageRoleReplace, R12LayerNotes: "Whole-completion control; distinct from per-event hooks (R12)."},
	{ID: StageIDFinalStreamObservation, MutationRole: StageRoleObserve, R12LayerNotes: "Final canonical stream observation after gates; before traffic/egress."},
	{ID: StageIDTrafficObservation, MutationRole: StageRoleObserve, R12LayerNotes: "Observation and capture; non-mutating for request execution."},
	{ID: StageIDEgressEncoding, MutationRole: StageRoleMutate, R12LayerNotes: "Wire encoding toward clients."},
}

// LegalPipelineStageIDs returns a copy of the ordered legal stage id list (R2).
func LegalPipelineStageIDs() []string {
	out := make([]string, 0, len(legalStageDescriptors))
	for _, d := range legalStageDescriptors {
		out = append(out, d.ID)
	}
	return out
}

// LegalStageDescriptors returns a copy of the canonical stage descriptor table.
func LegalStageDescriptors() []StageDescriptor {
	out := make([]StageDescriptor, len(legalStageDescriptors))
	copy(out, legalStageDescriptors)
	return out
}

// StageDescriptorByID reports whether id is a legal stage and returns its descriptor.
func StageDescriptorByID(id string) (StageDescriptor, bool) {
	for _, d := range legalStageDescriptors {
		if d.ID == id {
			return d, true
		}
	}
	return StageDescriptor{}, false
}

// ValidateStageID reports whether id is one of the legal pipeline stages.
func ValidateStageID(id string) bool {
	_, ok := StageDescriptorByID(id)
	return ok
}

// LegalStageDescriptorIndex returns the 0-based pipeline index for id, or -1 if unknown.
func LegalStageDescriptorIndex(id string) int {
	for i, d := range legalStageDescriptors {
		if d.ID == id {
			return i
		}
	}
	return -1
}
