package backendplugin

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// CredentialMode is the closed credential posture for a factory export.
// Wire values match aipsdk.BackendCredentialMode; Validate() is the ABI gate.
type CredentialMode = aipsdk.BackendCredentialMode

const (
	CredentialModeUnspecified CredentialMode = ""
	CredentialModeStatic      CredentialMode = aipsdk.CredentialStatic
	CredentialModeWorkload    CredentialMode = aipsdk.CredentialWorkload
	CredentialModeOAuthUser   CredentialMode = aipsdk.CredentialOAuthUser
	CredentialModeNone        CredentialMode = aipsdk.CredentialNone
	CredentialModeUnknown     CredentialMode = aipsdk.CredentialUnknown
)

// AccessScope is the closed multi-user access posture for a factory export.
// Wire values match aipsdk.BackendAccessScope; Validate() is the ABI gate.
type AccessScope = aipsdk.BackendAccessScope

const (
	AccessScopeUnspecified AccessScope = ""
	AccessScopeAny         AccessScope = aipsdk.BackendAccessAny
	AccessScopeLocalOnly   AccessScope = aipsdk.BackendAccessLocalOnly
)

// ProcessSharing declares the plugin process model.
type ProcessSharing string

const (
	ProcessSharingUnspecified    ProcessSharing = ""
	ProcessSharingPerInstance    ProcessSharing = "per_instance"
	ProcessSharingSharedArtifact ProcessSharing = "shared_artifact"
)

// Role is a canonical message role (alias of aipapi.Role).
type Role = aipapi.Role

const (
	RoleUnspecified Role = ""
	RoleSystem      Role = aipapi.RoleSystem
	RoleDeveloper   Role = aipapi.RoleDeveloper
	RoleUser        Role = aipapi.RoleUser
	RoleAssistant   Role = aipapi.RoleAssistant
	RoleTool        Role = aipapi.RoleTool
)

// UsagePresence records which usage counters were explicitly supplied (alias of aipapi.UsagePresence).
type UsagePresence = aipapi.UsagePresence

// PartKind identifies a canonical content part.
type PartKind string

const (
	PartKindUnspecified  PartKind = ""
	PartKindText         PartKind = "text"
	PartKindImageRef     PartKind = "image_ref"
	PartKindFileRef      PartKind = "file_ref"
	PartKindReasoning    PartKind = "reasoning"
	PartKindToolCall     PartKind = "tool_call"
	PartKindToolResult   PartKind = "tool_result"
	PartKindJSON         PartKind = "json"
	PartKindVideoRef     PartKind = "video_ref"
	PartKindRefusal      PartKind = "refusal"
	PartKindSummary      PartKind = "summary"
	PartKindAnnotation   PartKind = "annotation"
	PartKindAssistantRef PartKind = "assistant_ref"
	// PartKindExtension is an opaque vendor-prefixed custom content part preserved
	// losslessly (minor >= ProtocolMinorExactOpenResponsesFields).
	PartKindExtension PartKind = "extension"
)

// ErrorCode classifies plugin errors.
type ErrorCode string

const (
	ErrorCodeUnspecified        ErrorCode = ""
	ErrorCodeInvalidArgument    ErrorCode = "invalid_argument"
	ErrorCodeUnauthenticated    ErrorCode = "unauthenticated"
	ErrorCodePermissionDenied   ErrorCode = "permission_denied"
	ErrorCodeNotFound           ErrorCode = "not_found"
	ErrorCodeResourceExhausted  ErrorCode = "resource_exhausted"
	ErrorCodeFailedPrecondition ErrorCode = "failed_precondition"
	ErrorCodeAborted            ErrorCode = "aborted"
	ErrorCodeUnavailable        ErrorCode = "unavailable"
	ErrorCodeInternal           ErrorCode = "internal"
	ErrorCodeProviderTransient  ErrorCode = "provider_transient"
	ErrorCodeProviderTerminal   ErrorCode = "provider_terminal"
	ErrorCodeProtocolViolation  ErrorCode = "protocol_violation"
	ErrorCodeCancelled          ErrorCode = "cancelled"
)

// CancelReason classifies cancellation.
type CancelReason string

const (
	CancelReasonUnspecified CancelReason = ""
	CancelReasonClient      CancelReason = "client"
	CancelReasonHost        CancelReason = "host"
	CancelReasonDeadline    CancelReason = "deadline"
	CancelReasonShutdown    CancelReason = "shutdown"
)

// CancelMode reports how cancellation was performed (alias of aipapi.CancelMode).
type CancelMode = aipapi.CancelMode

const (
	CancelModeUnspecified CancelMode = ""
	CancelModeNone        CancelMode = aipapi.CancelModeNone
	CancelModeProvider    CancelMode = aipapi.CancelModeProvider
	CancelModeTransport   CancelMode = aipapi.CancelModeTransport
	CancelModeCloseOnly   CancelMode = aipapi.CancelModeCloseOnly
)

// ClientFrameKind is a host-to-plugin Execute frame kind.
type ClientFrameKind string

const (
	ClientFrameUnspecified ClientFrameKind = ""
	ClientFrameStart       ClientFrameKind = "start"
	ClientFrameCancel      ClientFrameKind = "cancel"
	ClientFrameCloseInput  ClientFrameKind = "close_input"
)

// ServerFrameKind is a plugin-to-host Execute frame kind.
type ServerFrameKind string

const (
	ServerFrameUnspecified            ServerFrameKind = ""
	ServerFrameAccepted               ServerFrameKind = "accepted"
	ServerFrameEvent                  ServerFrameKind = "event"
	ServerFrameDiagnostic             ServerFrameKind = "diagnostic"
	ServerFrameCancelOutcome          ServerFrameKind = "cancel_outcome"
	ServerFrameTerminal               ServerFrameKind = "terminal"
	ServerFrameAccountingEvidence     ServerFrameKind = "accounting_evidence"
	ServerFramePromptCacheObservation ServerFrameKind = "prompt_cache_observation"
)

type AccountingSource string

const (
	AccountingSourceUnknown          AccountingSource = ""
	AccountingSourceProviderReported AccountingSource = "provider_reported"
	AccountingSourceProviderCountAPI AccountingSource = "provider_count_api"
	AccountingSourceLocalEstimator   AccountingSource = "local_estimator"
	AccountingSourceLocalTokenizer   AccountingSource = "local_tokenizer"
)

type AccountingAuthority string

const (
	AccountingAuthorityUnknown       AccountingAuthority = ""
	AccountingAuthorityAuthoritative AccountingAuthority = "authoritative"
	AccountingAuthorityEstimated     AccountingAuthority = "estimated"
	AccountingAuthorityDelegated     AccountingAuthority = "delegated"
	AccountingAuthorityAdvisory      AccountingAuthority = "advisory"
)

type AccountingPlane string

const (
	AccountingPlaneUnknown          AccountingPlane = ""
	AccountingPlaneProviderBillable AccountingPlane = "provider_billable"
)

// EventKind identifies a canonical stream event (alias of aipapi.EventKind).
// Validate() rejects values outside the ABI wire subset (no reasoning_part on the plugin ABI).
type EventKind = aipapi.EventKind

const (
	EventUnspecified             EventKind = ""
	EventResponseStarted         EventKind = aipapi.EventResponseStarted
	EventMessageStarted          EventKind = aipapi.EventMessageStarted
	EventTextDelta               EventKind = aipapi.EventTextDelta
	EventReasoningDelta          EventKind = aipapi.EventReasoningDelta
	EventReasoningSignatureDelta EventKind = aipapi.EventReasoningSignatureDelta
	EventReasoningOpaqueDelta    EventKind = aipapi.EventReasoningOpaqueDelta
	EventReasoningPart           EventKind = aipapi.EventReasoningPart
	EventToolCallStarted         EventKind = aipapi.EventToolCallStarted
	EventToolCallArgsDelta       EventKind = aipapi.EventToolCallArgsDelta
	EventToolCallFinished        EventKind = aipapi.EventToolCallFinished
	EventUsageDelta              EventKind = aipapi.EventUsageDelta
	EventWarning                 EventKind = aipapi.EventWarning
	EventError                   EventKind = aipapi.EventError
	EventResponseFinished        EventKind = aipapi.EventResponseFinished
	EventAssistantImageRef       EventKind = aipapi.EventAssistantImageRef
	EventAssistantFileRef        EventKind = aipapi.EventAssistantFileRef
)

// TerminalStatus is the execute terminal outcome.
type TerminalStatus string

const (
	TerminalUnspecified TerminalStatus = ""
	TerminalSuccess     TerminalStatus = "success"
	TerminalFailure     TerminalStatus = "failure"
	TerminalCancelled   TerminalStatus = "cancelled"
)

// CredentialModeFromAipsdk maps public aipsdk registration modes into ABI modes.
func CredentialModeFromAipsdk(m aipsdk.BackendCredentialMode) (CredentialMode, error) {
	cm := CredentialMode(m)
	if err := ValidateCredentialMode(cm); err != nil {
		return CredentialModeUnspecified, err
	}
	return cm, nil
}

// AccessScopeFromAipsdk maps public aipsdk access scopes into ABI scopes.
func AccessScopeFromAipsdk(s aipsdk.BackendAccessScope) (AccessScope, error) {
	as := AccessScope(s)
	if err := ValidateAccessScope(as); err != nil {
		return AccessScopeUnspecified, err
	}
	return as, nil
}

// ValidateEventKind rejects unspecified and unknown canonical event kinds (fail closed).
func ValidateEventKind(k EventKind) error {
	switch k {
	case EventResponseStarted, EventMessageStarted, EventTextDelta, EventReasoningDelta, EventReasoningPart,
		EventReasoningSignatureDelta, EventReasoningOpaqueDelta, EventToolCallStarted,
		EventToolCallArgsDelta, EventToolCallFinished, EventUsageDelta, EventWarning,
		EventError, EventResponseFinished, EventAssistantImageRef, EventAssistantFileRef:
		return nil
	default:
		return ErrUnknownEventKind
	}
}

// ValidateCredentialMode rejects unspecified and unknown credential modes.
func ValidateCredentialMode(m CredentialMode) error {
	switch m {
	case CredentialModeStatic, CredentialModeWorkload, CredentialModeOAuthUser, CredentialModeNone, CredentialModeUnknown:
		return nil
	default:
		return ErrUnknownEnum
	}
}

// ValidateAccessScope rejects unspecified and unknown access scopes.
func ValidateAccessScope(s AccessScope) error {
	switch s {
	case AccessScopeAny, AccessScopeLocalOnly:
		return nil
	default:
		return ErrUnknownEnum
	}
}

func (p ProcessSharing) Validate() error {
	switch p {
	case ProcessSharingPerInstance, ProcessSharingSharedArtifact:
		return nil
	default:
		return ErrUnknownEnum
	}
}

// ValidateCancelMode rejects unspecified and unknown cancel modes.
func ValidateCancelMode(m CancelMode) error {
	switch m {
	case CancelModeNone, CancelModeProvider, CancelModeTransport, CancelModeCloseOnly:
		return nil
	default:
		return ErrUnknownEnum
	}
}
