package diag

// CompatibleInventoryHealth is bounded live inventory posture for one compatible
// backend instance. Values never include credential secrets or raw provider errors.
type CompatibleInventoryHealth struct {
	Status          string                           `json:"status"`
	Source          string                           `json:"source,omitempty"`
	ModelCount      int                              `json:"model_count"`
	ErrorCode       string                           `json:"error_code,omitempty"`
	RefreshedAt     string                           `json:"refreshed_at,omitempty"`
	LastSuccessHeld bool                             `json:"last_success_held,omitempty"`
	SampleModels    []CompatibleInventoryModelSample `json:"sample_models,omitempty"`
}

// CompatibleInventoryModelSample is a bounded operator view of one inventory row.
type CompatibleInventoryModelSample struct {
	InstanceID       string `json:"instance_id"`
	FactoryKind      string `json:"factory_kind"`
	Prefix           string `json:"prefix"`
	CanonicalID      string `json:"canonical_id"`
	NativeID         string `json:"native_id"`
	Source           string `json:"source"`
	CapabilitySource string `json:"capability_source"`
}
