package modelregistry

// EnrichBackendModelsForTest exports enrichBackendModels for contract tests.
func EnrichBackendModelsForTest(rows []BackendModel, inv BackendInventory) []BackendModel {
	return enrichBackendModels(rows, inv)
}
