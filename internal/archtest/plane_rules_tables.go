package archtest

// KnownPlaneFields maps struct field and identifier names to plane metadata.
var KnownPlaneFields = map[string]PlaneFieldMetadata{
	// Wave 1: Hooks
	"SubmitHooks":       {PlaneID: "submit_hooks", Wave: Wave1_HookBus},
	"RequestPartHooks":  {PlaneID: "request_part_hooks", Wave: Wave1_HookBus},
	"ResponsePartHooks": {PlaneID: "response_part_hooks", Wave: Wave1_HookBus},
	"ToolReactors":      {PlaneID: "tool_reactors", Wave: Wave1_HookBus},

	// Wave 2: Observers
	"TrafficObservers":        {PlaneID: "traffic_observers", Wave: Wave2_Observers},
	"UsageObservers":          {PlaneID: "usage_observers", Wave: Wave2_Observers},
	"RawCaptureSinks":         {PlaneID: "raw_capture_sinks", Wave: Wave2_Observers},
	"TrafficRedactors":        {PlaneID: "traffic_redactors", Wave: Wave2_Observers},
	"StreamObserverFactories": {PlaneID: "stream_observer_factories", Wave: Wave2_Observers},

	// Wave 3: Request shaping
	"RequestTransforms":  {PlaneID: "request_transforms", Wave: Wave3_RequestShaping},
	"PreRequestHandlers": {PlaneID: "pre_request_handlers", Wave: Wave3_RequestShaping},
	"RouteHintProviders": {PlaneID: "route_hint_providers", Wave: Wave3_RequestShaping},
	"CompletionGates":    {PlaneID: "completion_gates", Wave: Wave3_RequestShaping},
	"AttemptTransforms":  {PlaneID: "attempt_transforms", Wave: Wave3_RequestShaping},
	"SessionOpeners":     {PlaneID: "session_openers", Wave: Wave3_RequestShaping},
	"WorkspaceResolvers": {PlaneID: "workspace_resolvers", Wave: Wave3_RequestShaping},

	// Wave 4: Tools
	"ToolCatalogFilters":               {PlaneID: "tool_catalog_filters", Wave: Wave4_Tools},
	"ToolCallPolicies":                 {PlaneID: "tool_call_policies", Wave: Wave4_Tools},
	"ToolCallFinalizers":               {PlaneID: "tool_call_finalizers", Wave: Wave4_Tools},
	"ToolCallFinalizationMaxArgsBytes": {PlaneID: "tool_call_finalization_max_args_bytes", Wave: Wave4_Tools},
	"ControlToolProvider":              {PlaneID: "control_tool_provider", Wave: Wave4_Tools},
	"controlToolProviderID":            {PlaneID: "control_tool_provider", Wave: Wave4_Tools},
	"controlToolProvider":              {PlaneID: "control_tool_provider", Wave: Wave4_Tools},

	// Wave 5a: Secret guard & Compaction
	"SecretGuards":         {PlaneID: "secret_guards", Wave: Wave5a_GuardsCompaction},
	"CompactionObservers":  {PlaneID: "compaction_observers", Wave: Wave5a_GuardsCompaction},
	"CompactionPreservers": {PlaneID: "compaction_preservers", Wave: Wave5a_GuardsCompaction},

	// Wave 5b: Local turn & Terminal decision
	"LocalTurnHandlers":          {PlaneID: "local_turn_handlers", Wave: Wave5b_LocalTurnTerminal},
	"TerminalDecisionProvider":   {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
	"terminalDecisionProviderID": {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
	"terminalDecisionProvider":   {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},

	// Wave 5c: Session classification
	"SessionClassifier":   {PlaneID: "session_classifier", Wave: Wave5c_Residual},
	"sessionClassifier":   {PlaneID: "session_classifier", Wave: Wave5c_Residual},
	"sessionClassifierID": {PlaneID: "session_classifier", Wave: Wave5c_Residual},
}

// KnownPlaneIDs maps canonical plane IDs to plane metadata.
var KnownPlaneIDs = map[string]PlaneFieldMetadata{
	"submit_hooks":                          {PlaneID: "submit_hooks", Wave: Wave1_HookBus},
	"request_part_hooks":                    {PlaneID: "request_part_hooks", Wave: Wave1_HookBus},
	"response_part_hooks":                   {PlaneID: "response_part_hooks", Wave: Wave1_HookBus},
	"tool_reactors":                         {PlaneID: "tool_reactors", Wave: Wave1_HookBus},
	"traffic_observers":                     {PlaneID: "traffic_observers", Wave: Wave2_Observers},
	"usage_observers":                       {PlaneID: "usage_observers", Wave: Wave2_Observers},
	"raw_capture_sinks":                     {PlaneID: "raw_capture_sinks", Wave: Wave2_Observers},
	"traffic_redactors":                     {PlaneID: "traffic_redactors", Wave: Wave2_Observers},
	"stream_observer_factories":             {PlaneID: "stream_observer_factories", Wave: Wave2_Observers},
	"request_transforms":                    {PlaneID: "request_transforms", Wave: Wave3_RequestShaping},
	"pre_request_handlers":                  {PlaneID: "pre_request_handlers", Wave: Wave3_RequestShaping},
	"route_hint_providers":                  {PlaneID: "route_hint_providers", Wave: Wave3_RequestShaping},
	"completion_gates":                      {PlaneID: "completion_gates", Wave: Wave3_RequestShaping},
	"attempt_transforms":                    {PlaneID: "attempt_transforms", Wave: Wave3_RequestShaping},
	"session_openers":                       {PlaneID: "session_openers", Wave: Wave3_RequestShaping},
	"workspace_resolvers":                   {PlaneID: "workspace_resolvers", Wave: Wave3_RequestShaping},
	"tool_catalog_filters":                  {PlaneID: "tool_catalog_filters", Wave: Wave4_Tools},
	"tool_call_policies":                    {PlaneID: "tool_call_policies", Wave: Wave4_Tools},
	"tool_call_finalizers":                  {PlaneID: "tool_call_finalizers", Wave: Wave4_Tools},
	"tool_call_finalization_max_args_bytes": {PlaneID: "tool_call_finalization_max_args_bytes", Wave: Wave4_Tools},
	"secret_guards":                         {PlaneID: "secret_guards", Wave: Wave5a_GuardsCompaction},
	"compaction_observers":                  {PlaneID: "compaction_observers", Wave: Wave5a_GuardsCompaction},
	"compaction_preservers":                 {PlaneID: "compaction_preservers", Wave: Wave5a_GuardsCompaction},
	"local_turn_handlers":                   {PlaneID: "local_turn_handlers", Wave: Wave5b_LocalTurnTerminal},
	"terminal_decision_provider":            {PlaneID: "terminal_decision_provider", Wave: Wave5b_LocalTurnTerminal},
	"session_classifier":                    {PlaneID: "session_classifier", Wave: Wave5c_Residual},
	"control_tool_provider":                 {PlaneID: "control_tool_provider", Wave: Wave4_Tools},
}

// Whitelisted non-plane fields for individual structs.
var (
	AllowedFeatureBundleFields = map[string]bool{
		"SchemaVersion": true,
		"PlaneSet":      true,
		"Lifecycles":    true,
	}

	AllowedGeneratedMergeSurfaceFields = map[string]bool{
		"Lifecycles": true,
		"frozen":     true,
		"Frozen":     true,
		"set":        true,
	}

	AllowedGenerationOperationsFields = map[string]bool{
		"terminalProviders":  true,
		"readiness":          true,
		"frozen":             true,
		"Frozen":             true,
		"keepwarmAccounting": true,
	}

	AllowedRequestRuntimeSnapshotFields = map[string]bool{
		"hookBus":          true,
		"state":            true,
		"aux":              true,
		"obs":              true,
		"usageObs":         true,
		"raw":              true,
		"ws":               true,
		"secretGuardPlane": true,
		"policyObserver":   true,
		"timeoutBudget":    true,
		"timeoutGuard":     true,
		"featurePlanes":    true,
		"gen":              true,
	}

	AllowedExtensionRuntimeFields = map[string]bool{
		"ToolCallFinalizationMaxArgsBytes": true,
		"toolCallFinalizers":               true,
	}

	AllowedResponsePipelineFields = map[string]bool{
		"compactionObservers":  true,
		"compactionPreservers": true,
	}

	AllowedTurnTerminalFields = map[string]bool{
		"terminalDecisionProvider":   true,
		"terminalDecisionProviderID": true,
	}

	// AllowedStructFields maps fully qualified struct paths ("dir.StructName") to their allowed field names.
	AllowedStructFields = map[string]map[string]bool{
		"pkg/aipsdk/feature.FeatureBundle":                AllowedFeatureBundleFields,
		"internal/featurebundle.GeneratedMergeSurface":    AllowedGeneratedMergeSurfaceFields,
		"internal/core/runtime.generationOperations":      AllowedGenerationOperationsFields,
		"internal/core/extensions.RequestRuntimeSnapshot": AllowedRequestRuntimeSnapshotFields,
		"internal/core/runtime.ExtensionRuntime":          AllowedExtensionRuntimeFields,
		"internal/core/runtime.responsePipeline":          AllowedResponsePipelineFields,
		"internal/core/runtime.turnTerminal":              AllowedTurnTerminalFields,
		"internal/featurebundle.HostContributions": {
			"TrafficObservers": true,
			"UsageObservers":   true,
		},
		"internal/infra/runtimebundle.ProductionOptions": {
			"TrafficObservers": true,
			"UsageObservers":   true,
		},
		"pkg/aipruntime.Options": {
			"TrafficObservers": true,
			"UsageObservers":   true,
		},
		"pkg/aipsdk/controlplane.HostCapabilities": {
			"TrafficObservers": true,
			"UsageObservers":   true,
		},
	}

	// AllowedStageConsumers is the explicit allowlist of fully-qualified Go symbol paths
	// permitted to act as stage consumers accessing extension planes. Any stage-consumer accessor
	// or method outside this allowlist is rejected by the forbidden-mirror architecture scanner.
	AllowedStageConsumers = map[string]bool{
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompletionGates":                  true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RequestTransforms":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).PreRequestHandlers":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RouteHintProviders":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).AttemptTransforms":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SessionOpeners":                   true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).Workspace":                        true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCatalogFilters":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPolicies":                 true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallPoliciesExecution":        true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizers":               true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ToolCallFinalizersExecution":      true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuards":                     true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SecretGuardsExecution":            true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompactionObservers":              true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).CompactionPreservers":             true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlers":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).LocalTurnHandlersExecution":       true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProvider":         true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TerminalDecisionProviderIdentity": true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).SessionClassifier":                true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ControlToolProvider":              true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).ControlToolProviderIdentity":      true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficObserver":                  true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).UsageObserver":                    true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).RawCapture":                       true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficRedactors":                 true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).StreamObserverFactories":          true,
		"internal/core/extensions.(*RequestRuntimeSnapshot).TrafficPortBundle":                true,
		"internal/core/extensions.CompletionGatesFromContext":                                 true,
	}

	// AllowedObserverProjections is the qualified-symbol allowlist of observer and tool projection functions.
	// Any other function whose body reads observer/tool planes via Get past Wave 2/4 is forbidden.
	AllowedObserverProjections = map[string]bool{
		"internal/infra/runtimebundle.buildRuntimeSnapshot":                        true,
		"internal/infra/runtimebundle.buildExecutorRuntime":                        true,
		"internal/infra/runtimebundle.buildSecretGuardRuntime":                     true,
		"internal/standardplugins/featurehost.(*Runtime).CompileGeneration":        true,
		"internal/standardplugins/featurehost.(*Runtime).bindCompactionContinuity": true,
		"internal/standardplugins/featurehost/reasoning.Bind":                      true,
		"internal/featurebundle.FreezeBundle":                                      true,
		"internal/featurebundle.MergeBundlesGenerated":                             true,
	}

	// ClosedPlaneTargetStorageStructs lists the structs in pkg/aipsdk/feature that
	// must not contain arbitrary map storage fields.
	ClosedPlaneTargetStorageStructs = map[string]bool{
		"ContributionSet":                           true,
		"FrozenPlaneSet":                            true,
		"generatedContributions":                    true,
		"generatedFrozen":                           true,
		"pkg/aipsdk/feature.ContributionSet":        true,
		"pkg/aipsdk/feature.FrozenPlaneSet":         true,
		"pkg/aipsdk/feature.generatedContributions": true,
		"pkg/aipsdk/feature.generatedFrozen":        true,
	}

	// ClosedPlaneAllowedStorageMaps defines the explicit allowlist of legitimate attribution maps.
	ClosedPlaneAllowedStorageMaps = map[string]map[string]string{
		"ContributionSet":                    {"pluginIDs": "map[string]string"},
		"FrozenPlaneSet":                     {"pluginIDs": "map[string]string"},
		"pkg/aipsdk/feature.ContributionSet": {"pluginIDs": "map[string]string"},
		"pkg/aipsdk/feature.FrozenPlaneSet":  {"pluginIDs": "map[string]string"},
	}

	// ClosedPlaneAllowedReflectionFuncs lists exact functions in pkg/aipsdk/feature
	// permitted to use reflection for typed nil inspection.
	ClosedPlaneAllowedReflectionFuncs = map[string]bool{
		"isReflectNil":                    true,
		"isNilValue":                      true,
		"pkg/aipsdk/feature.isReflectNil": true,
		"pkg/aipsdk/feature.isNilValue":   true,
	}

	// ClosedPlaneAllowedPackageMapFuncs lists functions in pkg/aipsdk/feature permitted
	// to use maps for legitimate package tasks unrelated to closed plane value storage.
	ClosedPlaneAllowedPackageMapFuncs = map[string]bool{
		"ValidateManifest":                    true,
		"pkg/aipsdk/feature.ValidateManifest": true,
	}

	// ClosedPlaneOperationalFiles defines production files in pkg/aipsdk/feature where
	// map-backed operational logic is strictly prohibited.
	ClosedPlaneOperationalFiles = map[string]bool{
		"contributions.go":   true,
		"frozen.go":          true,
		"plane_generated.go": true,
	}

	// KnownLegacyForbiddenPlaneMapHelpers maps deprecated fallback helper names to rejection rationale.
	KnownLegacyForbiddenPlaneMapHelpers = map[string]string{
		"validateAllPlanesMap":     "map validation helper is deleted; validation must operate on generated typed storage",
		"replayAllPlanesMapTo":     "map replay helper is deleted; replay must operate on generated typed storage",
		"mapHasIdentityReplayRule": "map identity replay helper is deleted; identity checks must operate on generated typed storage",
		"contributeCandidateMapTo": "map candidate helper is deleted; candidate contributions must operate on generated typed storage",
	}
)

// IsAllowedObserverProjection reports whether qualifiedSymbol is in the qualified-symbol allowlist for observer projection.
func IsAllowedObserverProjection(qualifiedSymbol string) bool {
	return AllowedObserverProjections[qualifiedSymbol]
}

// IsAllowedStageConsumer checks if a fully-qualified Go symbol is explicitly recorded in AllowedStageConsumers.
func IsAllowedStageConsumer(qualifiedSymbol string) bool {
	return AllowedStageConsumers[qualifiedSymbol]
}
