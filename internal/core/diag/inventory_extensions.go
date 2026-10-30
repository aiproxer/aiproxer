package diag

import (
	"context"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type FeatureRegistry interface {
	BuildFeatureBundle(factoryKey string, n yaml.Node) (aipfeature.FeatureBundle, error)
}

// InventoryProjection contains one shared diagnostic projection pass.
// InventoryProjection is the complete, sanitized diagnostic read model shared
// by inventory and route inspection. Callers must not rebuild its filtered
// views independently.
type InventoryProjection struct {
	CompatibleBackends     []CompatibleBackendRow
	InstanceDiagnostics    []InstanceDiagnostic
	OpenResponsesFrontends []InstanceDiagnostic
}

// ProjectInventoryDiagnostics assembles and sanitizes the complete diagnostic
// read model used by inventory and route inspection. This is the only place
// that combines compatible-backend rows with contribution-owned projectors.
func ProjectInventoryDiagnostics(cfg *config.Config, compatible []CompatibleBackendRow, projectors []InstanceDiagnosticProjector) InventoryProjection {
	raw := CompatibleBackendInstanceDiagnostics(compatible)
	for _, project := range projectors {
		if project != nil {
			raw = append(raw, project(cfg)...)
		}
	}
	projected := ProjectSanitizedInstanceDiagnostics(raw)
	return InventoryProjection{
		CompatibleBackends:     compatible,
		InstanceDiagnostics:    projected.Instances,
		OpenResponsesFrontends: projected.OpenResponsesRows,
	}
}

type InventoryExtras struct {
	Reg                FeatureRegistry
	Registrations      []aipsdk.Registration
	CompatibleBackends CompatibleBackendProjector
	// FeatureMetadata is the compiled inventory read model keyed by feature
	// instance ID; featurehost computes it once per accepted generation and
	// inventory reads never construct feature bundles.
	FeatureMetadata []InventoryFeatureMetadata
	// Precomputed is supplied by a composition root when related operator views
	// already share one projection pass. A non-nil value is authoritative.
	Precomputed                  *InventoryProjection
	InstanceDiagnosticProjectors []InstanceDiagnosticProjector
	// SecretGuard* carry the composed secret-guard posture between the
	// secretguard composition and its execution-plane projection; the operator
	// read model itself travels in FeatureMetadata.
	SecretGuardCatalogEntryCount      int
	SecretGuardSourceCategories       []string
	SecretGuardAccessMode             string
	SecretGuardAction                 string
	SecretGuardLocalAutoDiscovery     bool
	SecretGuardBetterLeaksEnabled     bool
	SecretGuardBetterLeaksVersion     string
	SecretGuardBetterLeaksConfigHash  string
	SecretGuardBetterLeaksRuleCount   int
	SecretGuardBetterLeaksConfidence  string
	SecretGuardBetterLeaksDecodeDepth int
	SecretGuardBetterLeaksWorkers     int
	SecretGuardDiscoveryDetectorCount int
}

// InventoryFeatureMetadata is the immutable diagnostic read model for one
// compiled feature instance. The feature/composition authority
// (internal/standardplugins/featurehost) computes it from the compiled merge
// surface, including the secret-guard instance association; diagnostics
// consumes these finished rows and performs no feature-plane reduction.
type InventoryFeatureMetadata struct {
	InstanceID     string
	StageOccupancy []InventoryStageOccupancy
	Privileges     InventoryPrivileges
	BundleError    string
	SecretGuard    *InventorySecretGuard
}

type InventorySecretGuard struct {
	InstanceID                string   `json:"instance_id"`
	Action                    string   `json:"action,omitempty"`
	CatalogEntryCount         int      `json:"catalog_entry_count"`
	SourceCategories          []string `json:"source_categories,omitempty"`
	AccessMode                string   `json:"access_mode,omitempty"`
	LocalAutoDiscoveryEnabled bool     `json:"local_auto_discovery_enabled"`
	BetterLeaksEnabled        bool     `json:"betterleaks_enabled"`
	BetterLeaksVersion        string   `json:"betterleaks_version,omitempty"`
	BetterLeaksConfigHash     string   `json:"betterleaks_config_hash,omitempty"`
	BetterLeaksRuleCount      *int     `json:"betterleaks_active_rule_count,omitempty"`
	BetterLeaksConfidence     string   `json:"betterleaks_minimum_confidence,omitempty"`
	BetterLeaksDecodeDepth    *int     `json:"betterleaks_max_decode_depth,omitempty"`
	BetterLeaksWorkers        int      `json:"betterleaks_workers,omitempty"`
	DiscoveryDetectorCount    int      `json:"discovery_detector_count"`
}

type InventoryExtensions struct {
	LegalPipeline []string                     `json:"legal_pipeline"`
	Stages        []InventoryExtensionStage    `json:"stages"`
	Features      []InventoryFeatureExtensions `json:"features"`
	// GenericPorts aggregates attempt-transform / final-stream observer occupancy
	// across features (counts/flags only; no payloads or session partitions).
	GenericPorts InventoryGenericPorts `json:"generic_ports"`
}

// InventoryGenericPorts is process-local aggregate posture for generic extension ports.
type InventoryGenericPorts struct {
	AttemptTransformOccupied       bool `json:"attempt_transform_occupied"`
	AttemptTransformHandlers       int  `json:"attempt_transform_handlers"`
	FinalStreamObservationOccupied bool `json:"final_stream_observation_occupied"`
	FinalStreamObservationHandlers int  `json:"final_stream_observation_handlers"`
}

type InventoryExtensionStage struct {
	ID             string `json:"id"`
	DefaultFailure string `json:"default_failure"`
}

type InventoryFeatureExtensions struct {
	InstanceID     string                    `json:"instance_id"`
	FactoryKind    string                    `json:"factory_kind"`
	Enabled        bool                      `json:"enabled"`
	BundleError    string                    `json:"bundle_error,omitempty"`
	StageOccupancy []InventoryStageOccupancy `json:"stage_occupancy"`
	Privileges     InventoryPrivileges       `json:"privileges"`
	SecretGuard    *InventorySecretGuard     `json:"secret_guard,omitempty"`
}

type InventoryStageOccupancy struct {
	StageID    string   `json:"stage_id"`
	HandlerIDs []string `json:"handler_ids"`
	Count      int      `json:"count"`
}

// InventoryPrivileges surfaces privileged contract boundaries (all false until bundles declare them).
type InventoryPrivileges struct {
	RawCapture        bool `json:"raw_capture"`
	AuxiliaryRequests bool `json:"auxiliary_requests"`
	AuthProvider      bool `json:"auth_provider"`
	CompletionGate    bool `json:"completion_gate"`
}

func buildInventoryExtensions(_ context.Context, cfg *config.Config, extras *InventoryExtras) InventoryExtensions {
	pipeline := extensions.LegalPipelineStageNames()
	stages := make([]InventoryExtensionStage, 0, len(pipeline))
	for _, id := range pipeline {
		stages = append(stages, InventoryExtensionStage{
			ID:             id,
			DefaultFailure: extensions.FailurePolicyLabel(extensions.DefaultFailurePolicyForStage(id)),
		})
	}
	if cfg == nil {
		return InventoryExtensions{
			LegalPipeline: pipeline,
			Stages:        stages,
			Features:      []InventoryFeatureExtensions{},
			GenericPorts:  InventoryGenericPorts{},
		}
	}

	metadataByID := make(map[string]InventoryFeatureMetadata)
	if extras != nil {
		for _, metadata := range extras.FeatureMetadata {
			metadataByID[metadata.InstanceID] = metadata
		}
	}

	featureRows := cfg.Plugins.Features
	feats := make([]InventoryFeatureExtensions, 0, len(featureRows))
	for _, pc := range featureRows {
		entry := InventoryFeatureExtensions{
			InstanceID:     pc.InstanceID(),
			FactoryKind:    pc.FactoryID(),
			Enabled:        pc.Enabled,
			StageOccupancy: []InventoryStageOccupancy{},
			Privileges:     InventoryPrivileges{},
		}
		if metadata, ok := metadataByID[entry.InstanceID]; ok {
			entry.StageOccupancy = metadata.StageOccupancy
			entry.Privileges = metadata.Privileges
			entry.BundleError = metadata.BundleError
			entry.SecretGuard = metadata.SecretGuard
		}
		feats = append(feats, entry)
	}
	var ports InventoryGenericPorts
	for _, f := range feats {
		for _, occ := range f.StageOccupancy {
			switch occ.StageID {
			case extensions.StageCandidateAttemptTransform:
				ports.AttemptTransformHandlers += occ.Count
			case extensions.StageFinalStreamObservation:
				ports.FinalStreamObservationHandlers += occ.Count
			}
		}
	}
	ports.AttemptTransformOccupied = ports.AttemptTransformHandlers > 0
	ports.FinalStreamObservationOccupied = ports.FinalStreamObservationHandlers > 0
	return InventoryExtensions{LegalPipeline: pipeline, Stages: stages, Features: feats, GenericPorts: ports}
}
