package featurehost

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// secretGuardFactoryID is the standard-distribution factory id of the
// secret-guard feature. featurehost is the only composition layer that knows
// the concrete standard feature set, so the inventory association between a
// configured instance and its composed posture belongs here.
const secretGuardFactoryID = "secrets-guard"

// InventoryMetadataFromMergeSurface reduces the diagnostic projections of a
// compiled feature surface into the immutable per-feature inventory read
// model. Generic runtimebundle captures it once when the generation surface is
// frozen so inventory reads never rebuild feature bundles.
func InventoryMetadataFromMergeSurface(surface featurebundle.GeneratedMergeSurface) []diag.InventoryFeatureMetadata {
	projections := surface.FeatureDiagnosticProjections()
	if len(projections) == 0 {
		return nil
	}
	metadata := make([]diag.InventoryFeatureMetadata, len(projections))
	for i, feature := range projections {
		metadata[i] = InventoryFeatureMetadataFromProjections(feature.InstanceID, feature.Projections)
	}
	return metadata
}

// InventoryMetadataForConfig associates the compiled secret-guard posture with
// its configured feature instance. A configured secrets-guard feature without a
// generation-composed posture keeps an empty row so the inventory shape stays
// stable; nil posture documents "no composed execution config".
func InventoryMetadataForConfig(
	cfg *config.Config,
	compiled []diag.InventoryFeatureMetadata,
	secretGuard *diag.InventorySecretGuard,
) []diag.InventoryFeatureMetadata {
	if cfg == nil {
		return nil
	}
	metadata := slices.Clone(compiled)
	byID := make(map[string]int, len(metadata))
	for i, feature := range metadata {
		byID[feature.InstanceID] = i
	}
	for _, feature := range cfg.Plugins.Features {
		if !strings.EqualFold(feature.FactoryID(), secretGuardFactoryID) {
			continue
		}
		instanceID := feature.InstanceID()
		if i, ok := byID[instanceID]; ok {
			metadata[i].SecretGuard = inventorySecretGuardForInstance(instanceID, secretGuard)
		} else {
			byID[instanceID] = len(metadata)
			metadata = append(metadata, diag.InventoryFeatureMetadata{
				InstanceID:     instanceID,
				StageOccupancy: []diag.InventoryStageOccupancy{},
				SecretGuard:    inventorySecretGuardForInstance(instanceID, secretGuard),
			})
		}
	}
	return metadata
}

func inventorySecretGuardForInstance(instanceID string, posture *diag.InventorySecretGuard) *diag.InventorySecretGuard {
	if posture == nil {
		return &diag.InventorySecretGuard{InstanceID: instanceID}
	}
	row := *posture
	row.InstanceID = instanceID
	row.SourceCategories = slices.Clone(row.SourceCategories)
	return &row
}

// InventoryFeatureMetadataFromProjections reduces a compiled feature's
// diagnostic projections into the stable inventory read model.
func InventoryFeatureMetadataFromProjections(instanceID string, projections []aipfeature.DiagnosticPlaneProjection) diag.InventoryFeatureMetadata {
	metadata := diag.InventoryFeatureMetadata{
		InstanceID:     instanceID,
		StageOccupancy: []diag.InventoryStageOccupancy{},
	}
	occupancy, privileges, err := reduceDiagnosticProjections(projections)
	if err != nil {
		metadata.BundleError = err.Error()
		return metadata
	}
	metadata.StageOccupancy = occupancy
	metadata.Privileges = privileges
	return metadata
}

func reduceDiagnosticProjections(projections []aipfeature.DiagnosticPlaneProjection) ([]diag.InventoryStageOccupancy, diag.InventoryPrivileges, error) {
	if len(projections) == 0 {
		return []diag.InventoryStageOccupancy{}, diag.InventoryPrivileges{}, nil
	}

	sorted := make([]aipfeature.DiagnosticPlaneProjection, len(projections))
	copy(sorted, projections)
	slices.SortStableFunc(sorted, func(a, b aipfeature.DiagnosticPlaneProjection) int {
		if c := cmp.Compare(a.Order, b.Order); c != 0 {
			return c
		}
		return cmp.Compare(a.PlaneID, b.PlaneID)
	})

	stageOccupancy := make([]diag.InventoryStageOccupancy, 0, len(sorted))
	coalescedGroupIndex := make(map[string]int)
	var privs diag.InventoryPrivileges

	for _, p := range sorted {
		for _, f := range p.Privileges.Flags {
			if err := applyPrivilegeFlag(&privs, f); err != nil {
				return nil, diag.InventoryPrivileges{}, err
			}
		}
		for _, occ := range p.Occupants {
			for _, f := range occ.Privileges {
				if err := applyPrivilegeFlag(&privs, f); err != nil {
					return nil, diag.InventoryPrivileges{}, err
				}
			}
		}

		if len(p.Occupants) == 0 {
			continue
		}

		labels := make([]string, len(p.Occupants))
		for i, occ := range p.Occupants {
			labels[i] = occ.Label
		}

		if p.CoalesceGroup == "" {
			stageOccupancy = append(stageOccupancy, diag.InventoryStageOccupancy{
				StageID:    p.StageID,
				HandlerIDs: labels,
				Count:      len(labels),
			})
		} else {
			if idx, exists := coalescedGroupIndex[p.CoalesceGroup]; exists {
				stageOccupancy[idx].HandlerIDs = append(stageOccupancy[idx].HandlerIDs, labels...)
				stageOccupancy[idx].Count = len(stageOccupancy[idx].HandlerIDs)
			} else {
				newIdx := len(stageOccupancy)
				coalescedGroupIndex[p.CoalesceGroup] = newIdx
				stageOccupancy = append(stageOccupancy, diag.InventoryStageOccupancy{
					StageID:    p.StageID,
					HandlerIDs: labels,
					Count:      len(labels),
				})
			}
		}
	}

	return stageOccupancy, privs, nil
}

func applyPrivilegeFlag(privs *diag.InventoryPrivileges, flag string) error {
	switch flag {
	case aipfeature.PrivilegeRawCapture:
		privs.RawCapture = true
	case aipfeature.PrivilegeAuxiliaryRequests:
		privs.AuxiliaryRequests = true
	case aipfeature.PrivilegeCompletionGate:
		privs.CompletionGate = true
	case aipfeature.PrivilegeAuthProvider:
		privs.AuthProvider = true
	default:
		// Kept verbatim from the pre-relocation diagnostics implementation so
		// the operator-visible bundle_error JSON stays byte-compatible.
		return fmt.Errorf("diag: unknown privilege flag %q", flag)
	}
	return nil
}
