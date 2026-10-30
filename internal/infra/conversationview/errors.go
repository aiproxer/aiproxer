package conversationview

import (
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
)

var (
	// ErrALegNotFound is returned when conversation-view state is requested for an unknown A-leg.
	ErrALegNotFound = conversationprojection.ErrALegNotFound

	// ErrInvalidALegID is returned when an A-leg identifier is missing or malformed.
	ErrInvalidALegID = errors.New("conversationview: invalid a-leg id")

	// ErrInvalidTagRequest is returned when a TagRequest fails validation.
	ErrInvalidTagRequest = errors.New("conversationview: invalid tag request")

	// ErrTagLimitExceeded is returned when a tag mutation would exceed the 4096 unique-identity cap.
	ErrTagLimitExceeded = errors.New("conversationview: never_backend tag limit exceeded")

	// ErrInvalidOverlayID is returned when an overlay identifier is missing or exceeds bounds.
	ErrInvalidOverlayID = conversationprojection.ErrInvalidOverlayID

	// ErrInvalidSteeringRequest is returned when a PutSteeringRequest fails validation.
	ErrInvalidSteeringRequest = errors.New("conversationview: invalid steering request")

	// ErrInvalidSteeringMessage is returned when a StoredMessageV1 fails validation.
	ErrInvalidSteeringMessage = conversationprojection.ErrInvalidSteeringMessage

	// ErrInvalidPlacement is returned when a StoredPlacement fails validation.
	ErrInvalidPlacement = conversationprojection.ErrInvalidPlacement

	// ErrInvalidAnchorMissingPolicy is returned when an AnchorMissingPolicy value is invalid.
	ErrInvalidAnchorMissingPolicy = conversationprojection.ErrInvalidAnchorMissingPolicy

	// ErrInvalidReasonCode is returned when a ReasonCode is missing or exceeds bounds.
	ErrInvalidReasonCode = conversationprojection.ErrInvalidReasonCode

	// ErrSteeringLimitExceeded is returned when a steering mutation would exceed a count or byte cap.
	ErrSteeringLimitExceeded = errors.New("conversationview: steering limit exceeded")

	// ErrOverlayNotFound is returned when an overlay cannot be found for deactivation.
	ErrOverlayNotFound = conversationprojection.ErrOverlayNotFound

	// ErrRevisionExhausted is returned when a revision counter would overflow.
	ErrRevisionExhausted = errors.New("conversationview: revision exhausted")

	// ErrSteeringAnchorExcluded is returned when a steering registration would newly bind an
	// after_message anchor whose identity is already never_backend at the atomic persistence point.
	ErrSteeringAnchorExcluded = errors.New("conversationview: steering anchor identity is never_backend")

	// Re-exported kernel projection and safety errors.
	ErrNonMessageItem             = conversationprojection.ErrNonMessageItem
	ErrEmptyMessage               = conversationprojection.ErrEmptyMessage
	ErrInvalidRole                = conversationprojection.ErrInvalidRole
	ErrInvalidMessageIdentity     = conversationprojection.ErrInvalidMessageIdentity
	ErrInvalidMessageAnchor       = conversationprojection.ErrInvalidMessageAnchor
	ErrAnchorNotFound             = conversationprojection.ErrAnchorNotFound
	ErrPartialContentNotSupported = conversationprojection.ErrPartialContentNotSupported
	ErrAnchorMissing              = conversationprojection.ErrAnchorMissing
	ErrProjectionFailed           = conversationprojection.ErrProjectionFailed
	ErrTerminalUserNotFound       = conversationprojection.ErrTerminalUserNotFound
	ErrTerminalNotUser            = conversationprojection.ErrTerminalNotUser
)
