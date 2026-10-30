package processhost

type RequirementID string

const (
	ReqPublicABIOwnership          RequirementID = "public_abi_ownership"
	ReqProtocolNegotiation         RequirementID = "protocol_negotiation"
	ReqBidirectionalStreaming      RequirementID = "bidirectional_streaming"
	ReqTransportRetriesDisabled    RequirementID = "transport_retries_disabled"
	ReqExactByteLaunch             RequirementID = "exact_byte_launch"
	ReqExpectedProcessPeerIdentity RequirementID = "expected_process_peer_identity"
	ReqProtectedBootstrap          RequirementID = "protected_bootstrap"
	ReqMinimalEnvHandleControl     RequirementID = "minimal_env_handle_control"
	ReqProcessTreeCleanup          RequirementID = "process_tree_cleanup"
	ReqBoundedMessagesLogs         RequirementID = "bounded_messages_logs"
	ReqDeclaredProcessModels       RequirementID = "declared_process_models"
	ReqReattachProhibition         RequirementID = "reattach_prohibition"
	ReqLicenseEvidence             RequirementID = "license_evidence"
)

type RequirementSpec struct {
	ID        RequirementID
	Mandatory bool
	Summary   string
}

func MandatoryRequirements() []RequirementSpec {
	return []RequirementSpec{
		{ID: ReqPublicABIOwnership, Mandatory: true, Summary: "AIProxer owns the public backend-plugin ABI; substrate must not dictate domain DTOs"},
		{ID: ReqProtocolNegotiation, Mandatory: true, Summary: "Host and plugin negotiate protocol major/minor before configure"},
		{ID: ReqBidirectionalStreaming, Mandatory: true, Summary: "Execution uses bounded bidirectional streaming over the local channel"},
		{ID: ReqTransportRetriesDisabled, Mandatory: true, Summary: "Transport-level automatic retries are disabled"},
		{ID: ReqExactByteLaunch, Mandatory: true, Summary: "Launch binds verified executable bytes per supported OS profile"},
		{ID: ReqExpectedProcessPeerIdentity, Mandatory: true, Summary: "Local IPC authenticates the expected spawned process generation separately from executable digest"},
		{ID: ReqProtectedBootstrap, Mandatory: true, Summary: "Private bootstrap material uses a protected OS channel, never the child environment"},
		{ID: ReqMinimalEnvHandleControl, Mandatory: true, Summary: "Child environment and inherited handles are minimized and non-secret"},
		{ID: ReqProcessTreeCleanup, Mandatory: true, Summary: "Host owns process-tree cleanup, kill, and exactly-once reap"},
		{ID: ReqBoundedMessagesLogs, Mandatory: true, Summary: "gRPC message and plugin log/diagnostic bounds are enforced"},
		{ID: ReqDeclaredProcessModels, Mandatory: true, Summary: "Shared-artifact and per-instance process models are host-supervised declarations"},
		{ID: ReqReattachProhibition, Mandatory: true, Summary: "Process reattach to foreign or prior generations is prohibited"},
		{ID: ReqLicenseEvidence, Mandatory: true, Summary: "Third-party substrate license and version evidence are recorded"},
	}
}
