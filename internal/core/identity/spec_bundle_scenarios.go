package identity

// IdentityScenarioSpec links a stable scenario identifier to proxy-identity
// invariants (issue #147) and the primary regression test (specification bundle).
// PackageRel is the repository-relative directory that owns TestName's *_test.go
// (may live in an external connector module under connectors/, outside the root go.mod).
type IdentityScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string
	PackageRel       string
}

// SpecBundleIdentityScenarios lists A-leg/B-leg identity invariants.
// Keep aligned with the referenced tests and docs/proxy-identity.md.
func SpecBundleIdentityScenarios() []IdentityScenarioSpec {
	return []IdentityScenarioSpec{
		{
			ID:               "SB-IDENTITY-default-upstream",
			InvariantSummary: "Omitted/proxy User-Agent emits literal AIProxer product identity on the B-leg wire (ID-001).",
			TestName:         "TestTransport_modesOnWire",
			PackageRel:       "internal/plugins/backends/httpidentity",
		},
		{
			ID:               "SB-IDENTITY-ua-passthrough",
			InvariantSummary: "Passthrough forwards validated call-path User-Agent verbatim; missing/invalid call-path omits (ID-010).",
			TestName:         "TestTransport_modesOnWire",
			PackageRel:       "internal/plugins/backends/httpidentity",
		},
		{
			ID:               "SB-IDENTITY-backend-override",
			InvariantSummary: "Backend nested identity override wins over proxy-wide User-Agent policy for approved connectors.",
			TestName:         "TestIdentityTransport_approvedFactoriesWireUserAgent",
			PackageRel:       "internal/standardplugins",
		},
		{
			ID:               "SB-IDENTITY-drop-suppresses",
			InvariantSummary: "Drop suppresses User-Agent including Go's Go-http-client/1.1 default (ID-030).",
			TestName:         "TestTransport_dropSuppressesBareGoDefault",
			PackageRel:       "internal/plugins/backends/httpidentity",
		},
		{
			ID:               "SB-IDENTITY-openrouter-attr",
			InvariantSummary: "OpenRouter proxy defaults emit AIProxer HTTP-Referer and X-OpenRouter-Title, overriding captured client values.",
			TestName:         "TestOpenRouterAttribution_defaultProxyOverridesCapturedClient",
			PackageRel:       "connectors/openrouter",
		},
		{
			ID:               "SB-IDENTITY-aleg-server",
			InvariantSummary: "A-leg Server defaults to literal AIProxer product identity when config is nil/omitted (ID-050).",
			TestName:         "TestDownstreamServerMiddleware_nilConfigProxyLiteral",
			PackageRel:       "internal/stdhttp",
		},
		{
			ID:               "SB-IDENTITY-exclusions",
			InvariantSummary: "Approved and excluded connector ID lists are literal, disjoint, and locked (ID-147-ALLOW).",
			TestName:         "TestIdentityTransport_ID147_allowlistAndExclusionLiterals",
			PackageRel:       "internal/archtest",
		},
		{
			ID:               "SB-IDENTITY-b2bua-failover",
			InvariantSummary: "Ordered failover applies each backend's effective User-Agent policy independently (ID-147-FO).",
			TestName:         "TestIdentityExecutor_ID147_orderedFailoverIsolatesUserAgent",
			PackageRel:       "internal/standardplugins",
		},
		{
			ID:               "SB-IDENTITY-b2bua-parallel",
			InvariantSummary: "Parallel race candidates isolate User-Agent header state per B-leg (ID-147-PR).",
			TestName:         "TestIdentityExecutor_ID147_parallelRaceIsolatesUserAgent",
			PackageRel:       "internal/standardplugins",
		},
		{
			ID:               "SB-IDENTITY-no-failover-after-output",
			InvariantSummary: "Post-output failures do not open an identity-bearing failover backend (ID-147-PO).",
			TestName:         "TestIdentityExecutor_ID147_noFailoverAfterOutputPreservesIdentityChoice",
			PackageRel:       "internal/standardplugins",
		},
	}
}
