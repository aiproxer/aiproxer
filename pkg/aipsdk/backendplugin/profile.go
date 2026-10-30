package backendplugin

// Validate reports inventory size bounds.
func (r ListModelsResponse) Validate(maxModels uint32) error {
	if maxModels == 0 {
		maxModels = DefaultMaxModelsPerResponse
	}
	return ValidateSize(uint64(len(r.Models)), uint64(maxModels))
}
