package backendplugin

import backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"

func enumFromProto[P comparable, D ~string](v P, table map[P]D, zero D) (D, error) {
	if d, ok := table[v]; ok {
		return d, nil
	}
	return zero, ErrUnknownEnum
}

func enumToProto[D ~string, P comparable](v D, table map[D]P, zero P) (P, error) {
	if p, ok := table[v]; ok {
		return p, nil
	}
	return zero, ErrUnknownEnum
}

var credentialModeFromProtoTable = map[backendpluginv1.CredentialMode]CredentialMode{
	backendpluginv1.CredentialMode_CREDENTIAL_MODE_STATIC:     CredentialModeStatic,
	backendpluginv1.CredentialMode_CREDENTIAL_MODE_WORKLOAD:   CredentialModeWorkload,
	backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER: CredentialModeOAuthUser,
	backendpluginv1.CredentialMode_CREDENTIAL_MODE_NONE:       CredentialModeNone,
	backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNKNOWN:    CredentialModeUnknown,
}

var credentialModeToProtoTable = map[CredentialMode]backendpluginv1.CredentialMode{
	CredentialModeStatic:    backendpluginv1.CredentialMode_CREDENTIAL_MODE_STATIC,
	CredentialModeWorkload:  backendpluginv1.CredentialMode_CREDENTIAL_MODE_WORKLOAD,
	CredentialModeOAuthUser: backendpluginv1.CredentialMode_CREDENTIAL_MODE_OAUTH_USER,
	CredentialModeNone:      backendpluginv1.CredentialMode_CREDENTIAL_MODE_NONE,
	CredentialModeUnknown:   backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNKNOWN,
}

func credentialModeFromProto(v backendpluginv1.CredentialMode) (CredentialMode, error) {
	return enumFromProto(v, credentialModeFromProtoTable, CredentialModeUnspecified)
}

func credentialModeToProto(v CredentialMode) (backendpluginv1.CredentialMode, error) {
	return enumToProto(v, credentialModeToProtoTable, backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNSPECIFIED)
}

var accessScopeFromProtoTable = map[backendpluginv1.AccessScope]AccessScope{
	backendpluginv1.AccessScope_ACCESS_SCOPE_ANY:        AccessScopeAny,
	backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY: AccessScopeLocalOnly,
}

var accessScopeToProtoTable = map[AccessScope]backendpluginv1.AccessScope{
	AccessScopeAny:       backendpluginv1.AccessScope_ACCESS_SCOPE_ANY,
	AccessScopeLocalOnly: backendpluginv1.AccessScope_ACCESS_SCOPE_LOCAL_ONLY,
}

func accessScopeFromProto(v backendpluginv1.AccessScope) (AccessScope, error) {
	return enumFromProto(v, accessScopeFromProtoTable, AccessScopeUnspecified)
}

func accessScopeToProto(v AccessScope) (backendpluginv1.AccessScope, error) {
	return enumToProto(v, accessScopeToProtoTable, backendpluginv1.AccessScope_ACCESS_SCOPE_UNSPECIFIED)
}

var processSharingFromProtoTable = map[backendpluginv1.ProcessSharing]ProcessSharing{
	backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE:    ProcessSharingPerInstance,
	backendpluginv1.ProcessSharing_PROCESS_SHARING_SHARED_ARTIFACT: ProcessSharingSharedArtifact,
}

var processSharingToProtoTable = map[ProcessSharing]backendpluginv1.ProcessSharing{
	ProcessSharingPerInstance:    backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE,
	ProcessSharingSharedArtifact: backendpluginv1.ProcessSharing_PROCESS_SHARING_SHARED_ARTIFACT,
}

func processSharingFromProto(v backendpluginv1.ProcessSharing) (ProcessSharing, error) {
	return enumFromProto(v, processSharingFromProtoTable, ProcessSharingUnspecified)
}

func processSharingToProto(v ProcessSharing) (backendpluginv1.ProcessSharing, error) {
	return enumToProto(v, processSharingToProtoTable, backendpluginv1.ProcessSharing_PROCESS_SHARING_UNSPECIFIED)
}

var roleFromProtoTable = map[backendpluginv1.Role]Role{
	backendpluginv1.Role_ROLE_SYSTEM:    RoleSystem,
	backendpluginv1.Role_ROLE_DEVELOPER: RoleDeveloper,
	backendpluginv1.Role_ROLE_USER:      RoleUser,
	backendpluginv1.Role_ROLE_ASSISTANT: RoleAssistant,
	backendpluginv1.Role_ROLE_TOOL:      RoleTool,
}

var roleToProtoTable = map[Role]backendpluginv1.Role{
	RoleSystem:    backendpluginv1.Role_ROLE_SYSTEM,
	RoleDeveloper: backendpluginv1.Role_ROLE_DEVELOPER,
	RoleUser:      backendpluginv1.Role_ROLE_USER,
	RoleAssistant: backendpluginv1.Role_ROLE_ASSISTANT,
	RoleTool:      backendpluginv1.Role_ROLE_TOOL,
}

func roleFromProto(v backendpluginv1.Role) (Role, error) {
	return enumFromProto(v, roleFromProtoTable, RoleUnspecified)
}

func roleToProto(v Role) (backendpluginv1.Role, error) {
	return enumToProto(v, roleToProtoTable, backendpluginv1.Role_ROLE_UNSPECIFIED)
}

var partKindFromProtoTable = map[backendpluginv1.PartKind]PartKind{
	backendpluginv1.PartKind_PART_KIND_TEXT:          PartKindText,
	backendpluginv1.PartKind_PART_KIND_IMAGE_REF:     PartKindImageRef,
	backendpluginv1.PartKind_PART_KIND_FILE_REF:      PartKindFileRef,
	backendpluginv1.PartKind_PART_KIND_REASONING:     PartKindReasoning,
	backendpluginv1.PartKind_PART_KIND_TOOL_CALL:     PartKindToolCall,
	backendpluginv1.PartKind_PART_KIND_TOOL_RESULT:   PartKindToolResult,
	backendpluginv1.PartKind_PART_KIND_JSON:          PartKindJSON,
	backendpluginv1.PartKind_PART_KIND_VIDEO_REF:     PartKindVideoRef,
	backendpluginv1.PartKind_PART_KIND_REFUSAL:       PartKindRefusal,
	backendpluginv1.PartKind_PART_KIND_SUMMARY:       PartKindSummary,
	backendpluginv1.PartKind_PART_KIND_ANNOTATION:    PartKindAnnotation,
	backendpluginv1.PartKind_PART_KIND_ASSISTANT_REF: PartKindAssistantRef,
	backendpluginv1.PartKind_PART_KIND_EXTENSION:     PartKindExtension,
}

var partKindToProtoTable = map[PartKind]backendpluginv1.PartKind{
	PartKindText:         backendpluginv1.PartKind_PART_KIND_TEXT,
	PartKindImageRef:     backendpluginv1.PartKind_PART_KIND_IMAGE_REF,
	PartKindFileRef:      backendpluginv1.PartKind_PART_KIND_FILE_REF,
	PartKindReasoning:    backendpluginv1.PartKind_PART_KIND_REASONING,
	PartKindToolCall:     backendpluginv1.PartKind_PART_KIND_TOOL_CALL,
	PartKindToolResult:   backendpluginv1.PartKind_PART_KIND_TOOL_RESULT,
	PartKindJSON:         backendpluginv1.PartKind_PART_KIND_JSON,
	PartKindVideoRef:     backendpluginv1.PartKind_PART_KIND_VIDEO_REF,
	PartKindRefusal:      backendpluginv1.PartKind_PART_KIND_REFUSAL,
	PartKindSummary:      backendpluginv1.PartKind_PART_KIND_SUMMARY,
	PartKindAnnotation:   backendpluginv1.PartKind_PART_KIND_ANNOTATION,
	PartKindAssistantRef: backendpluginv1.PartKind_PART_KIND_ASSISTANT_REF,
	PartKindExtension:    backendpluginv1.PartKind_PART_KIND_EXTENSION,
}

func partKindFromProto(v backendpluginv1.PartKind) (PartKind, error) {
	return enumFromProto(v, partKindFromProtoTable, PartKindUnspecified)
}

func partKindToProto(v PartKind) (backendpluginv1.PartKind, error) {
	return enumToProto(v, partKindToProtoTable, backendpluginv1.PartKind_PART_KIND_UNSPECIFIED)
}

var errorCodeFromProtoTable = map[backendpluginv1.ErrorCode]ErrorCode{
	backendpluginv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT:    ErrorCodeInvalidArgument,
	backendpluginv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED:     ErrorCodeUnauthenticated,
	backendpluginv1.ErrorCode_ERROR_CODE_PERMISSION_DENIED:   ErrorCodePermissionDenied,
	backendpluginv1.ErrorCode_ERROR_CODE_NOT_FOUND:           ErrorCodeNotFound,
	backendpluginv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED:  ErrorCodeResourceExhausted,
	backendpluginv1.ErrorCode_ERROR_CODE_FAILED_PRECONDITION: ErrorCodeFailedPrecondition,
	backendpluginv1.ErrorCode_ERROR_CODE_ABORTED:             ErrorCodeAborted,
	backendpluginv1.ErrorCode_ERROR_CODE_UNAVAILABLE:         ErrorCodeUnavailable,
	backendpluginv1.ErrorCode_ERROR_CODE_INTERNAL:            ErrorCodeInternal,
	backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TRANSIENT:  ErrorCodeProviderTransient,
	backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TERMINAL:   ErrorCodeProviderTerminal,
	backendpluginv1.ErrorCode_ERROR_CODE_PROTOCOL_VIOLATION:  ErrorCodeProtocolViolation,
	backendpluginv1.ErrorCode_ERROR_CODE_CANCELLED:           ErrorCodeCancelled,
}

var errorCodeToProtoTable = map[ErrorCode]backendpluginv1.ErrorCode{
	ErrorCodeInvalidArgument:    backendpluginv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
	ErrorCodeUnauthenticated:    backendpluginv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED,
	ErrorCodePermissionDenied:   backendpluginv1.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
	ErrorCodeNotFound:           backendpluginv1.ErrorCode_ERROR_CODE_NOT_FOUND,
	ErrorCodeResourceExhausted:  backendpluginv1.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED,
	ErrorCodeFailedPrecondition: backendpluginv1.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
	ErrorCodeAborted:            backendpluginv1.ErrorCode_ERROR_CODE_ABORTED,
	ErrorCodeUnavailable:        backendpluginv1.ErrorCode_ERROR_CODE_UNAVAILABLE,
	ErrorCodeInternal:           backendpluginv1.ErrorCode_ERROR_CODE_INTERNAL,
	ErrorCodeProviderTransient:  backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TRANSIENT,
	ErrorCodeProviderTerminal:   backendpluginv1.ErrorCode_ERROR_CODE_PROVIDER_TERMINAL,
	ErrorCodeProtocolViolation:  backendpluginv1.ErrorCode_ERROR_CODE_PROTOCOL_VIOLATION,
	ErrorCodeCancelled:          backendpluginv1.ErrorCode_ERROR_CODE_CANCELLED,
}

func errorCodeFromProto(v backendpluginv1.ErrorCode) (ErrorCode, error) {
	return enumFromProto(v, errorCodeFromProtoTable, ErrorCodeUnspecified)
}

func errorCodeToProto(v ErrorCode) (backendpluginv1.ErrorCode, error) {
	return enumToProto(v, errorCodeToProtoTable, backendpluginv1.ErrorCode_ERROR_CODE_UNSPECIFIED)
}

var cancelReasonFromProtoTable = map[backendpluginv1.CancelReason]CancelReason{
	backendpluginv1.CancelReason_CANCEL_REASON_CLIENT:   CancelReasonClient,
	backendpluginv1.CancelReason_CANCEL_REASON_HOST:     CancelReasonHost,
	backendpluginv1.CancelReason_CANCEL_REASON_DEADLINE: CancelReasonDeadline,
	backendpluginv1.CancelReason_CANCEL_REASON_SHUTDOWN: CancelReasonShutdown,
}

var cancelReasonToProtoTable = map[CancelReason]backendpluginv1.CancelReason{
	CancelReasonClient:   backendpluginv1.CancelReason_CANCEL_REASON_CLIENT,
	CancelReasonHost:     backendpluginv1.CancelReason_CANCEL_REASON_HOST,
	CancelReasonDeadline: backendpluginv1.CancelReason_CANCEL_REASON_DEADLINE,
	CancelReasonShutdown: backendpluginv1.CancelReason_CANCEL_REASON_SHUTDOWN,
}

func cancelReasonFromProto(v backendpluginv1.CancelReason) (CancelReason, error) {
	return enumFromProto(v, cancelReasonFromProtoTable, CancelReasonUnspecified)
}

func cancelReasonToProto(v CancelReason) (backendpluginv1.CancelReason, error) {
	return enumToProto(v, cancelReasonToProtoTable, backendpluginv1.CancelReason_CANCEL_REASON_UNSPECIFIED)
}

var cancelModeFromProtoTable = map[backendpluginv1.CancelMode]CancelMode{
	backendpluginv1.CancelMode_CANCEL_MODE_NONE:       CancelModeNone,
	backendpluginv1.CancelMode_CANCEL_MODE_PROVIDER:   CancelModeProvider,
	backendpluginv1.CancelMode_CANCEL_MODE_TRANSPORT:  CancelModeTransport,
	backendpluginv1.CancelMode_CANCEL_MODE_CLOSE_ONLY: CancelModeCloseOnly,
}

var cancelModeToProtoTable = map[CancelMode]backendpluginv1.CancelMode{
	CancelModeNone:      backendpluginv1.CancelMode_CANCEL_MODE_NONE,
	CancelModeProvider:  backendpluginv1.CancelMode_CANCEL_MODE_PROVIDER,
	CancelModeTransport: backendpluginv1.CancelMode_CANCEL_MODE_TRANSPORT,
	CancelModeCloseOnly: backendpluginv1.CancelMode_CANCEL_MODE_CLOSE_ONLY,
}

func cancelModeFromProto(v backendpluginv1.CancelMode) (CancelMode, error) {
	return enumFromProto(v, cancelModeFromProtoTable, CancelModeUnspecified)
}

func cancelModeToProto(v CancelMode) (backendpluginv1.CancelMode, error) {
	return enumToProto(v, cancelModeToProtoTable, backendpluginv1.CancelMode_CANCEL_MODE_UNSPECIFIED)
}

var clientFrameKindFromProtoTable = map[backendpluginv1.ClientFrameKind]ClientFrameKind{
	backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START:       ClientFrameStart,
	backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL:      ClientFrameCancel,
	backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CLOSE_INPUT: ClientFrameCloseInput,
}

var clientFrameKindToProtoTable = map[ClientFrameKind]backendpluginv1.ClientFrameKind{
	ClientFrameStart:      backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START,
	ClientFrameCancel:     backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL,
	ClientFrameCloseInput: backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CLOSE_INPUT,
}

func clientFrameKindFromProto(v backendpluginv1.ClientFrameKind) (ClientFrameKind, error) {
	return enumFromProto(v, clientFrameKindFromProtoTable, ClientFrameUnspecified)
}

func clientFrameKindToProto(v ClientFrameKind) (backendpluginv1.ClientFrameKind, error) {
	return enumToProto(v, clientFrameKindToProtoTable, backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_UNSPECIFIED)
}

var serverFrameKindFromProtoTable = map[backendpluginv1.ServerFrameKind]ServerFrameKind{
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED:                 ServerFrameAccepted,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_EVENT:                    ServerFrameEvent,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_DIAGNOSTIC:               ServerFrameDiagnostic,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_CANCEL_OUTCOME:           ServerFrameCancelOutcome,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL:                 ServerFrameTerminal,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCOUNTING_EVIDENCE:      ServerFrameAccountingEvidence,
	backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_PROMPT_CACHE_OBSERVATION: ServerFramePromptCacheObservation,
}

var serverFrameKindToProtoTable = map[ServerFrameKind]backendpluginv1.ServerFrameKind{
	ServerFrameAccepted:               backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED,
	ServerFrameEvent:                  backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_EVENT,
	ServerFrameDiagnostic:             backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_DIAGNOSTIC,
	ServerFrameCancelOutcome:          backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_CANCEL_OUTCOME,
	ServerFrameTerminal:               backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL,
	ServerFrameAccountingEvidence:     backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCOUNTING_EVIDENCE,
	ServerFramePromptCacheObservation: backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_PROMPT_CACHE_OBSERVATION,
}

func serverFrameKindFromProto(v backendpluginv1.ServerFrameKind) (ServerFrameKind, error) {
	return enumFromProto(v, serverFrameKindFromProtoTable, ServerFrameUnspecified)
}

func serverFrameKindToProto(v ServerFrameKind) (backendpluginv1.ServerFrameKind, error) {
	return enumToProto(v, serverFrameKindToProtoTable, backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_UNSPECIFIED)
}

var accountingSourceFromProtoTable = map[backendpluginv1.AccountingSource]AccountingSource{
	backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_PROVIDER_REPORTED:  AccountingSourceProviderReported,
	backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_PROVIDER_COUNT_API: AccountingSourceProviderCountAPI,
	backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_LOCAL_ESTIMATOR:    AccountingSourceLocalEstimator,
	backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_LOCAL_TOKENIZER:    AccountingSourceLocalTokenizer,
}

var accountingSourceToProtoTable = map[AccountingSource]backendpluginv1.AccountingSource{
	AccountingSourceProviderReported: backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_PROVIDER_REPORTED,
	AccountingSourceProviderCountAPI: backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_PROVIDER_COUNT_API,
	AccountingSourceLocalEstimator:   backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_LOCAL_ESTIMATOR,
	AccountingSourceLocalTokenizer:   backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_LOCAL_TOKENIZER,
}

func accountingSourceFromProto(v backendpluginv1.AccountingSource) (AccountingSource, error) {
	return enumFromProto(v, accountingSourceFromProtoTable, AccountingSourceUnknown)
}

func accountingSourceToProto(v AccountingSource) (backendpluginv1.AccountingSource, error) {
	return enumToProto(v, accountingSourceToProtoTable, backendpluginv1.AccountingSource_ACCOUNTING_SOURCE_UNSPECIFIED)
}

var accountingAuthorityFromProtoTable = map[backendpluginv1.AccountingAuthority]AccountingAuthority{
	backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_AUTHORITATIVE: AccountingAuthorityAuthoritative,
	backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_ESTIMATED:     AccountingAuthorityEstimated,
	backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_DELEGATED:     AccountingAuthorityDelegated,
	backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_ADVISORY:      AccountingAuthorityAdvisory,
}

var accountingAuthorityToProtoTable = map[AccountingAuthority]backendpluginv1.AccountingAuthority{
	AccountingAuthorityAuthoritative: backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_AUTHORITATIVE,
	AccountingAuthorityEstimated:     backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_ESTIMATED,
	AccountingAuthorityDelegated:     backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_DELEGATED,
	AccountingAuthorityAdvisory:      backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_ADVISORY,
}

func accountingAuthorityFromProto(v backendpluginv1.AccountingAuthority) (AccountingAuthority, error) {
	return enumFromProto(v, accountingAuthorityFromProtoTable, AccountingAuthorityUnknown)
}

func accountingAuthorityToProto(v AccountingAuthority) (backendpluginv1.AccountingAuthority, error) {
	return enumToProto(v, accountingAuthorityToProtoTable, backendpluginv1.AccountingAuthority_ACCOUNTING_AUTHORITY_UNSPECIFIED)
}

var accountingPlaneFromProtoTable = map[backendpluginv1.AccountingPlane]AccountingPlane{
	backendpluginv1.AccountingPlane_ACCOUNTING_PLANE_PROVIDER_BILLABLE: AccountingPlaneProviderBillable,
}

var accountingPlaneToProtoTable = map[AccountingPlane]backendpluginv1.AccountingPlane{
	AccountingPlaneProviderBillable: backendpluginv1.AccountingPlane_ACCOUNTING_PLANE_PROVIDER_BILLABLE,
}

func accountingPlaneFromProto(v backendpluginv1.AccountingPlane) (AccountingPlane, error) {
	return enumFromProto(v, accountingPlaneFromProtoTable, AccountingPlaneUnknown)
}

func accountingPlaneToProto(v AccountingPlane) (backendpluginv1.AccountingPlane, error) {
	return enumToProto(v, accountingPlaneToProtoTable, backendpluginv1.AccountingPlane_ACCOUNTING_PLANE_UNSPECIFIED)
}

var eventKindFromProtoTable = map[backendpluginv1.EventKind]EventKind{
	backendpluginv1.EventKind_EVENT_KIND_RESPONSE_STARTED:          EventResponseStarted,
	backendpluginv1.EventKind_EVENT_KIND_MESSAGE_STARTED:           EventMessageStarted,
	backendpluginv1.EventKind_EVENT_KIND_TEXT_DELTA:                EventTextDelta,
	backendpluginv1.EventKind_EVENT_KIND_REASONING_DELTA:           EventReasoningDelta,
	backendpluginv1.EventKind_EVENT_KIND_REASONING_SIGNATURE_DELTA: EventReasoningSignatureDelta,
	backendpluginv1.EventKind_EVENT_KIND_REASONING_OPAQUE_DELTA:    EventReasoningOpaqueDelta,
	backendpluginv1.EventKind_EVENT_KIND_REASONING_PART:            EventReasoningPart,
	backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_STARTED:         EventToolCallStarted,
	backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_ARGS_DELTA:      EventToolCallArgsDelta,
	backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_FINISHED:        EventToolCallFinished,
	backendpluginv1.EventKind_EVENT_KIND_USAGE_DELTA:               EventUsageDelta,
	backendpluginv1.EventKind_EVENT_KIND_WARNING:                   EventWarning,
	backendpluginv1.EventKind_EVENT_KIND_ERROR:                     EventError,
	backendpluginv1.EventKind_EVENT_KIND_RESPONSE_FINISHED:         EventResponseFinished,
	backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_IMAGE_REF:       EventAssistantImageRef,
	backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_FILE_REF:        EventAssistantFileRef,
}

var eventKindToProtoTable = map[EventKind]backendpluginv1.EventKind{
	EventResponseStarted:         backendpluginv1.EventKind_EVENT_KIND_RESPONSE_STARTED,
	EventMessageStarted:          backendpluginv1.EventKind_EVENT_KIND_MESSAGE_STARTED,
	EventTextDelta:               backendpluginv1.EventKind_EVENT_KIND_TEXT_DELTA,
	EventReasoningDelta:          backendpluginv1.EventKind_EVENT_KIND_REASONING_DELTA,
	EventReasoningSignatureDelta: backendpluginv1.EventKind_EVENT_KIND_REASONING_SIGNATURE_DELTA,
	EventReasoningOpaqueDelta:    backendpluginv1.EventKind_EVENT_KIND_REASONING_OPAQUE_DELTA,
	EventReasoningPart:           backendpluginv1.EventKind_EVENT_KIND_REASONING_PART,
	EventToolCallStarted:         backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_STARTED,
	EventToolCallArgsDelta:       backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_ARGS_DELTA,
	EventToolCallFinished:        backendpluginv1.EventKind_EVENT_KIND_TOOL_CALL_FINISHED,
	EventUsageDelta:              backendpluginv1.EventKind_EVENT_KIND_USAGE_DELTA,
	EventWarning:                 backendpluginv1.EventKind_EVENT_KIND_WARNING,
	EventError:                   backendpluginv1.EventKind_EVENT_KIND_ERROR,
	EventResponseFinished:        backendpluginv1.EventKind_EVENT_KIND_RESPONSE_FINISHED,
	EventAssistantImageRef:       backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_IMAGE_REF,
	EventAssistantFileRef:        backendpluginv1.EventKind_EVENT_KIND_ASSISTANT_FILE_REF,
}

func eventKindFromProto(v backendpluginv1.EventKind) (EventKind, error) {
	return enumFromProto(v, eventKindFromProtoTable, EventUnspecified)
}

func eventKindToProto(v EventKind) (backendpluginv1.EventKind, error) {
	return enumToProto(v, eventKindToProtoTable, backendpluginv1.EventKind_EVENT_KIND_UNSPECIFIED)
}

var terminalStatusFromProtoTable = map[backendpluginv1.TerminalStatus]TerminalStatus{
	backendpluginv1.TerminalStatus_TERMINAL_STATUS_SUCCESS:   TerminalSuccess,
	backendpluginv1.TerminalStatus_TERMINAL_STATUS_FAILURE:   TerminalFailure,
	backendpluginv1.TerminalStatus_TERMINAL_STATUS_CANCELLED: TerminalCancelled,
}

var terminalStatusToProtoTable = map[TerminalStatus]backendpluginv1.TerminalStatus{
	TerminalSuccess:   backendpluginv1.TerminalStatus_TERMINAL_STATUS_SUCCESS,
	TerminalFailure:   backendpluginv1.TerminalStatus_TERMINAL_STATUS_FAILURE,
	TerminalCancelled: backendpluginv1.TerminalStatus_TERMINAL_STATUS_CANCELLED,
}

func terminalStatusFromProto(v backendpluginv1.TerminalStatus) (TerminalStatus, error) {
	return enumFromProto(v, terminalStatusFromProtoTable, TerminalUnspecified)
}

func terminalStatusToProto(v TerminalStatus) (backendpluginv1.TerminalStatus, error) {
	return enumToProto(v, terminalStatusToProtoTable, backendpluginv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED)
}
