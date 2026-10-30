package processhost

import (
	"fmt"
	"strings"
)

func ValidateCatalog(catalog EvidenceCatalog) error {
	required := []CandidateID{
		CandidateStockGoPluginV180,
		CandidateCustomGoPluginV18x,
		CandidateProjectOwnedHost,
	}
	matrix := MandatoryRequirements()
	known := make(map[RequirementID]struct{}, len(matrix))
	for _, req := range matrix {
		known[req.ID] = struct{}{}
	}

	for _, id := range required {
		dims, ok := catalog[id]
		if !ok {
			return fmt.Errorf("catalog missing candidate %q", id)
		}
		if err := validateCandidateDims(id, dims, matrix, known); err != nil {
			return err
		}
	}
	for id := range catalog {
		switch id {
		case CandidateStockGoPluginV180, CandidateCustomGoPluginV18x, CandidateProjectOwnedHost:
		default:
			return fmt.Errorf("catalog has unknown candidate %q", id)
		}
	}
	return nil
}

func validateCandidateDims(id CandidateID, dims []DimensionEvidence, matrix []RequirementSpec, known map[RequirementID]struct{}) error {
	seen := make(map[RequirementID]struct{}, len(dims))
	byReq := make(map[RequirementID]DimensionEvidence, len(dims))
	for _, dim := range dims {
		if _, ok := known[dim.Requirement]; !ok {
			return fmt.Errorf("candidate %q: unknown requirement %q", id, dim.Requirement)
		}
		if _, dup := seen[dim.Requirement]; dup {
			return fmt.Errorf("candidate %q: duplicate requirement %q", id, dim.Requirement)
		}
		seen[dim.Requirement] = struct{}{}
		if strings.TrimSpace(dim.Notes) == "" {
			return fmt.Errorf("candidate %q: empty notes for %q", id, dim.Requirement)
		}
		switch dim.Level {
		case EvidenceFailed, EvidenceMissing, EvidenceSourceVerified, EvidenceRuntimeVerified:
		default:
			return fmt.Errorf("candidate %q: invalid level %q for %q", id, dim.Level, dim.Requirement)
		}
		if needsSourceRef(id) {
			if err := validateSourceRef(id, dim); err != nil {
				return err
			}
		}
		byReq[dim.Requirement] = dim
	}
	for _, req := range matrix {
		if _, ok := byReq[req.ID]; !ok {
			return fmt.Errorf("candidate %q: incomplete; missing %q", id, req.ID)
		}
	}
	return nil
}

func needsSourceRef(id CandidateID) bool {
	return id == CandidateStockGoPluginV180 || id == CandidateCustomGoPluginV18x
}

func validateSourceRef(id CandidateID, dim DimensionEvidence) error {
	if dim.Source == nil {
		return fmt.Errorf("candidate %q: missing source ref for %q", id, dim.Requirement)
	}
	src := dim.Source
	if strings.TrimSpace(src.Module) == "" || strings.TrimSpace(src.Version) == "" {
		return fmt.Errorf("candidate %q: source ref for %q needs module and version", id, dim.Requirement)
	}
	if strings.TrimSpace(src.Path) == "" && strings.TrimSpace(src.URL) == "" {
		return fmt.Errorf("candidate %q: source ref for %q needs path or url", id, dim.Requirement)
	}
	if dim.Requirement == ReqLicenseEvidence {
		if src.License == "" && src.LicenseURL == "" {
			return fmt.Errorf("candidate %q: license evidence needs License or LicenseURL", id)
		}
	}
	return nil
}
