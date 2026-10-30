package processhost

import "fmt"

type ReplacementCost struct {
	Launch               bool
	AuthTransport        bool
	Bootstrap            bool
	Lifecycle            bool
	Limits               bool
	ExceedsRetainedValue bool
	Rationale            string
}

type CandidateAssessment struct {
	Candidate       CandidateID
	Dimensions      []DimensionEvidence
	Feasible        bool
	Selectable      bool
	RejectReasons   []string
	ReplacementCost ReplacementCost
}

type SelectionResult struct {
	Selected       CandidateID
	Platforms      []PlatformProfile
	Assessments    []CandidateAssessment
	OfficialSource OfficialSourceEvidence
	Task12Blockers []string
}

func (r SelectionResult) Assessment(id CandidateID) (CandidateAssessment, bool) {
	for i := range r.Assessments {
		if r.Assessments[i].Candidate == id {
			return cloneAssessment(r.Assessments[i]), true
		}
	}
	return CandidateAssessment{}, false
}

func (r SelectionResult) SelectedAssessment() (CandidateAssessment, bool) {
	if r.Selected == "" {
		return CandidateAssessment{}, false
	}
	return r.Assessment(r.Selected)
}

func SelectSubstrate(catalog EvidenceCatalog) (SelectionResult, error) {
	if err := ValidateCatalog(catalog); err != nil {
		return SelectionResult{}, err
	}

	order := []CandidateID{
		CandidateStockGoPluginV180,
		CandidateCustomGoPluginV18x,
		CandidateProjectOwnedHost,
	}
	assessments := make([]CandidateAssessment, 0, len(order))
	for _, id := range order {
		assessments = append(assessments, assessCandidate(id, catalog[id]))
	}

	var selected CandidateID
	for _, a := range assessments {
		if a.Selectable {
			selected = a.Candidate
			break
		}
	}

	result := SelectionResult{
		Selected:       selected,
		Platforms:      ApprovedPlatformProfiles(),
		Assessments:    assessments,
		OfficialSource: DefaultOfficialSource(),
	}
	switch selected {
	case CandidateProjectOwnedHost:
		result.Task12Blockers = projectOwnedTask12Blockers()
	case "":
		result.Task12Blockers = []string{
			"no selectable process substrate; cannot define protobuf/public DTO contracts on an unproven host",
		}
	}
	return result, nil
}

func assessCandidate(id CandidateID, dims []DimensionEvidence) CandidateAssessment {
	matrix := MandatoryRequirements()
	byReq := make(map[RequirementID]DimensionEvidence, len(dims))
	for _, d := range dims {
		byReq[d.Requirement] = d
	}

	out := CandidateAssessment{
		Candidate:  id,
		Dimensions: make([]DimensionEvidence, 0, len(matrix)),
	}
	feasible := true
	for _, req := range matrix {
		dim := byReq[req.ID]
		out.Dimensions = append(out.Dimensions, cloneDim(dim))
		if !feasibleLevel(dim.Level) {
			feasible = false
			out.RejectReasons = append(out.RejectReasons,
				fmt.Sprintf("%s: %s (%s)", dim.Requirement, dim.Level, dim.Notes))
		}
	}
	out.Feasible = feasible
	out.ReplacementCost = computeReplacementCost(id, out.Dimensions)
	if out.ReplacementCost.ExceedsRetainedValue {
		out.RejectReasons = append(out.RejectReasons, out.ReplacementCost.Rationale)
	}
	out.Selectable = out.Feasible && !out.ReplacementCost.ExceedsRetainedValue
	return out
}

func computeReplacementCost(id CandidateID, dims []DimensionEvidence) ReplacementCost {
	if id != CandidateCustomGoPluginV18x {
		return ReplacementCost{}
	}
	flag := func(req RequirementID) bool {
		for _, d := range dims {
			if d.Requirement == req {
				return d.ReplacesStock
			}
		}
		return false
	}
	cost := ReplacementCost{
		Launch:        flag(ReqExactByteLaunch),
		AuthTransport: flag(ReqExpectedProcessPeerIdentity),
		Bootstrap:     flag(ReqProtectedBootstrap),
		Lifecycle:     flag(ReqProcessTreeCleanup) && flag(ReqDeclaredProcessModels) && flag(ReqReattachProhibition),
		Limits:        flag(ReqBoundedMessagesLogs) && flag(ReqTransportRetriesDisabled) && flag(ReqMinimalEnvHandleControl),
	}
	cost.ExceedsRetainedValue = cost.Launch && cost.AuthTransport && cost.Bootstrap && cost.Lifecycle && cost.Limits
	if cost.ExceedsRetainedValue {
		cost.Rationale = "customized go-plugin would replace launch binding, expected-process auth/transport, protected bootstrap, lifecycle/process-model supervision, and message/env limits; residual reusable value is thin negotiation/streaming plumbing that AIProxer must own for its ABI anyway, while MPL-2.0 copyleft remains on modified files"
	}
	return cost
}

func cloneAssessment(in CandidateAssessment) CandidateAssessment {
	out := in
	out.Dimensions = cloneDims(in.Dimensions)
	if in.RejectReasons != nil {
		out.RejectReasons = append([]string(nil), in.RejectReasons...)
	}
	return out
}

func cloneDim(in DimensionEvidence) DimensionEvidence {
	out := in
	if in.Source != nil {
		src := *in.Source
		out.Source = &src
	}
	return out
}

func projectOwnedTask12Blockers() []string {
	return []string{
		"define api/backendplugin/v1 protobuf and pkg/aipsdk/backendplugin DTOs without importing internal processhost types",
		"keep gRPC/protobuf out of internal/core; host adapter consumes public contracts only",
		"encode fail-closed major-version rejection and disabled transport retries in public contract tests",
		"do not implement process launch, peer auth, or secure channel in Task 1.2",
	}
}
