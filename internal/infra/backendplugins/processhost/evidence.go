package processhost

type EvidenceCatalog map[CandidateID][]DimensionEvidence

func goPluginRef(path, symbol, lines, fragment string) *SourceRef {
	url := "https://github.com/hashicorp/go-plugin/blob/v1.8.0/" + path
	if fragment != "" {
		url += fragment
	}
	return &SourceRef{
		Module:  "github.com/hashicorp/go-plugin",
		Version: "v1.8.0",
		Path:    path,
		Symbol:  symbol,
		Lines:   lines,
		URL:     url,
	}
}

func goPluginLicenseRef() *SourceRef {
	return &SourceRef{
		Module:     "github.com/hashicorp/go-plugin",
		Version:    "v1.8.0",
		Path:       "LICENSE",
		License:    "MPL-2.0",
		LicenseURL: "https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE",
		URL:        "https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE",
	}
}

func DefaultEvidenceCatalog() EvidenceCatalog {
	return cloneCatalog(builtInEvidenceCatalog())
}

func builtInEvidenceCatalog() EvidenceCatalog {
	return EvidenceCatalog{
		CandidateStockGoPluginV180:  stockGoPluginEvidence(),
		CandidateCustomGoPluginV18x: customGoPluginEvidence(),
		CandidateProjectOwnedHost:   projectOwnedHostEvidence(),
	}
}

func stockGoPluginEvidence() []DimensionEvidence {
	return []DimensionEvidence{
		{
			Requirement: ReqPublicABIOwnership,
			Level:       EvidenceSourceVerified,
			Notes:       "Public ABI remains AIProxer-owned; stock go-plugin supplies process plumbing only",
			Source:      goPluginRef("plugin.go", "Serve", "1-80", ""),
		},
		{
			Requirement: ReqProtocolNegotiation,
			Level:       EvidenceSourceVerified,
			Notes:       "PLUGIN_PROTOCOL_VERSIONS handshake env negotiation exists",
			Source:      goPluginRef("client.go", "ClientConfig", "642", "#L642"),
		},
		{
			Requirement: ReqBidirectionalStreaming,
			Level:       EvidenceSourceVerified,
			Notes:       "gRPC mode supports plugin RPC streams",
			Source:      goPluginRef("grpc_client.go", "GRPCClient", "1-80", ""),
		},
		{
			Requirement: ReqTransportRetriesDisabled,
			Level:       EvidenceMissing,
			Notes:       "Stock client defaults do not document AIProxer-owned disabled transport retries",
			Source:      goPluginRef("grpc_client.go", "dial", "1-80", ""),
		},
		{
			Requirement: ReqExactByteLaunch,
			Level:       EvidenceFailed,
			Notes:       "SecureConfig.Check hashes a pathname then Client starts cmd.Path; not digest-bound exact-byte launch",
			Source:      goPluginRef("client.go", "SecureConfig.Check", "334-356,661-665", "#L334-L356"),
		},
		{
			Requirement: ReqExpectedProcessPeerIdentity,
			Level:       EvidenceFailed,
			Notes:       "Windows forces TCP loopback listener; Unix listen has no SO_PEERCRED/expected-PID binding",
			Source:      goPluginRef("server.go", "serverListener", "528-575", "#L528-L575"),
		},
		{
			Requirement: ReqProtectedBootstrap,
			Level:       EvidenceFailed,
			Notes:       "AutoMTLS writes PLUGIN_CLIENT_CERT into the child environment",
			Source:      goPluginRef("client.go", "AutoMTLS", "671-684", "#L671-L684"),
		},
		{
			Requirement: ReqMinimalEnvHandleControl,
			Level:       EvidenceFailed,
			Notes:       "Child env is assembled for cookies/certs/socket dirs without AIProxer minimal non-secret policy",
			Source:      goPluginRef("client.go", "start", "639-717", "#L639-L717"),
		},
		{
			Requirement: ReqProcessTreeCleanup,
			Level:       EvidenceMissing,
			Notes:       "Client kill/wait helpers exist but do not prove AIProxer process-tree/job ownership and exactly-once reap",
			Source:      goPluginRef("client.go", "Client", "1-200", ""),
		},
		{
			Requirement: ReqBoundedMessagesLogs,
			Level:       EvidenceFailed,
			Notes:       "gRPC client defaults MaxCallRecvMsgSize/MaxCallSendMsgSize to math.MaxInt32",
			Source:      goPluginRef("grpc_client.go", "MaxCallRecvMsgSize", "40-41", "#L40-L41"),
		},
		{
			Requirement: ReqDeclaredProcessModels,
			Level:       EvidenceFailed,
			Notes:       "No shared-artifact or per-instance process-model supervisor API",
			Source:      goPluginRef("client.go", "ClientConfig", "161-250", "#L161-L250"),
		},
		{
			Requirement: ReqReattachProhibition,
			Level:       EvidenceFailed,
			Notes:       "ReattachConfig is a first-class ClientConfig path",
			Source:      goPluginRef("client.go", "ReattachConfig", "161-164,296-308", "#L161-L164"),
		},
		{
			Requirement: ReqLicenseEvidence,
			Level:       EvidenceSourceVerified,
			Notes:       "go-plugin v1.8.0 LICENSE is MPL-2.0",
			Source:      goPluginLicenseRef(),
		},
	}
}

func customGoPluginEvidence() []DimensionEvidence {
	return []DimensionEvidence{
		{
			Requirement: ReqPublicABIOwnership,
			Level:       EvidenceSourceVerified,
			Notes:       "ABI can remain AIProxer-owned under a customized wrapper",
			Source:      goPluginRef("plugin.go", "Serve", "1-80", ""),
		},
		{
			Requirement: ReqProtocolNegotiation,
			Level:       EvidenceSourceVerified,
			Notes:       "Handshake plumbing is reusable; AIProxer still owns domain negotiation",
			Source:      goPluginRef("client.go", "PLUGIN_PROTOCOL_VERSIONS", "642", "#L642"),
		},
		{
			Requirement: ReqBidirectionalStreaming,
			Level:       EvidenceSourceVerified,
			Notes:       "gRPC streaming plumbing is reusable after customization",
			Source:      goPluginRef("grpc_client.go", "GRPCClient", "1-80", ""),
		},
		{
			Requirement:   ReqTransportRetriesDisabled,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing stock dial/options defaults; not runtime-proven here",
			Source:        goPluginRef("grpc_client.go", "MaxCallRecvMsgSize", "40-41", "#L40-L41"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqExactByteLaunch,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing SecureConfig pathname check and cmd.Path launch",
			Source:        goPluginRef("client.go", "SecureConfig.Check", "334-356,661-665", "#L334-L356"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqExpectedProcessPeerIdentity,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing Windows TCP and Unix listen peer model",
			Source:        goPluginRef("server.go", "serverListener", "528-575", "#L528-L575"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqProtectedBootstrap,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing AutoMTLS PLUGIN_CLIENT_CERT env bootstrap",
			Source:        goPluginRef("client.go", "AutoMTLS", "671-684", "#L671-L684"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqMinimalEnvHandleControl,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing stock child env/handle assembly",
			Source:        goPluginRef("client.go", "start", "639-717", "#L639-L717"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqProcessTreeCleanup,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only with host-owned process-group/job supervision outside stock Client",
			Source:        goPluginRef("client.go", "Client", "1-200", ""),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqBoundedMessagesLogs,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by replacing MaxInt32 default message ceilings",
			Source:        goPluginRef("grpc_client.go", "MaxCallRecvMsgSize", "40-41", "#L40-L41"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqDeclaredProcessModels,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by adding AIProxer process-model supervision outside stock go-plugin",
			Source:        goPluginRef("client.go", "ClientConfig", "161-250", "#L161-L250"),
			ReplacesStock: true,
		},
		{
			Requirement:   ReqReattachProhibition,
			Level:         EvidenceSourceVerified,
			Notes:         "Feasible only by removing/disabling ReattachConfig paths",
			Source:        goPluginRef("client.go", "ReattachConfig", "161-164,296-308", "#L161-L164"),
			ReplacesStock: true,
		},
		{
			Requirement: ReqLicenseEvidence,
			Level:       EvidenceSourceVerified,
			Notes:       "Customized go-plugin remains MPL-2.0 with copyleft on modified files",
			Source:      goPluginLicenseRef(),
		},
	}
}

func projectOwnedHostEvidence() []DimensionEvidence {
	note := func(s string) string {
		return s + " Design/source feasibility only; runtime proof deferred to Tasks 2.3/3.1."
	}
	return []DimensionEvidence{
		{Requirement: ReqPublicABIOwnership, Level: EvidenceSourceVerified, Notes: note("Public ABI stays in api/ and pkg/aipsdk/backendplugin.")},
		{Requirement: ReqProtocolNegotiation, Level: EvidenceSourceVerified, Notes: note("Host will own AIProxer protocol negotiation before configure.")},
		{Requirement: ReqBidirectionalStreaming, Level: EvidenceSourceVerified, Notes: note("Host will own gRPC bidirectional Execute adaptation.")},
		{Requirement: ReqTransportRetriesDisabled, Level: EvidenceSourceVerified, Notes: note("Host will configure gRPC clients with retries disabled.")},
		{Requirement: ReqExactByteLaunch, Level: EvidenceSourceVerified, Notes: note("Host will own Linux descriptor-bound launch and macOS/Windows protected staging.")},
		{Requirement: ReqExpectedProcessPeerIdentity, Level: EvidenceSourceVerified, Notes: note("Host will own Unix peercred/peerpid and Windows named-pipe DACL/token/PID/job checks.")},
		{Requirement: ReqProtectedBootstrap, Level: EvidenceSourceVerified, Notes: note("Host will bootstrap private material via inherited handle or one-shot OS channel.")},
		{Requirement: ReqMinimalEnvHandleControl, Level: EvidenceSourceVerified, Notes: note("Host will construct minimal non-secret env and close inherited handles.")},
		{Requirement: ReqProcessTreeCleanup, Level: EvidenceSourceVerified, Notes: note("Host will own process groups/job objects, kill, and exactly-once wait.")},
		{Requirement: ReqBoundedMessagesLogs, Level: EvidenceSourceVerified, Notes: note("Host will enforce message size and bounded stderr diagnostics.")},
		{Requirement: ReqDeclaredProcessModels, Level: EvidenceSourceVerified, Notes: note("Host will supervise declared shared-artifact and per-instance models.")},
		{Requirement: ReqReattachProhibition, Level: EvidenceSourceVerified, Notes: note("Host will never expose reattach; peers are generation-bound.")},
		{Requirement: ReqLicenseEvidence, Level: EvidenceSourceVerified, Notes: "No go-plugin substrate dependency; root-module licenses cover stdlib/gRPC when added later."},
	}
}

func cloneCatalog(in EvidenceCatalog) EvidenceCatalog {
	out := make(EvidenceCatalog, len(in))
	for id, dims := range in {
		out[id] = cloneDims(dims)
	}
	return out
}

func cloneDims(in []DimensionEvidence) []DimensionEvidence {
	out := make([]DimensionEvidence, len(in))
	for i := range in {
		out[i] = in[i]
		if in[i].Source != nil {
			src := *in[i].Source
			out[i].Source = &src
		}
	}
	return out
}
