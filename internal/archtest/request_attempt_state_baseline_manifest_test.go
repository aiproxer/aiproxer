package archtest

var requestAttemptStateBeforeManifest = RequestAttemptStateInventory{
	PreparedRequestFields: []string{
		"aLeg", "aScope", "baseline", "billingAccountID", "billingCallID", "billingCallState",
		"billingChargePolicy", "billingCustomerPricing", "billingIdentityStamped", "bus",
		"compactionOpenMeta", "execSpan", "metering", "recvViews", "recvViewsOK", "routeAuth",
		"routePrefs", "secureTurn", "secureTurnOK", "streamReturned", "traceID",
	},
	RoutePlanStateFields: []string{
		"affinityKey", "affinitySet", "budget", "contextLimitExhaustion", "excluded", "failoverReq",
		"interleaved", "lastAdmissionErr", "lastParallelFailure", "lastReject", "lastTransportReject",
		"requestSize", "rng", "sel", "session", "transformExcludes", "ttft",
	},
	AttemptOpenParamsFields: []string{
		"aLegID", "aScope", "affinityKey", "affinitySet", "baseline", "billingCallID", "billingCallState",
		"budget", "bus", "deferMemoInjectionCommit", "excluded", "failoverReq", "interleaved",
		"isContextLimitExhaustion", "isRetryPath", "lastAdmissionErr", "lastParallelFailure", "lastReject",
		"lastTransportReject", "requestSize", "rng", "sel", "session", "suppressThinker",
		"suppressVisibleMemo", "traceID", "transformExcludes", "ttft",
	},
	AttemptOpenResultFields: []string{
		"authority", "bleg", "cand", "interleaved", "memoUpdate", "opened", "registered", "stream",
	},
	PointerOutFields:              nil,
	RouteProgressDuplicatedFields: []string{"budget", "excluded", "interleaved", "requestSize", "rng", "sel", "session", "ttft"},
	TranslationSites:              []string{"internal/core/runtime/executor_route_plan.go:68"},
	DirectFieldCopyAssignments:    373,
	ContextBusinessStateRereads:   17,
	PreHandoffCleanupSites:        28,
}
