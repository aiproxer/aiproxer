package conversationprojection

import "errors"

var (
	// ErrNonMessageItem is returned when an item is not a complete message item.
	ErrNonMessageItem = errors.New("conversationprojection: item is not a complete message item")

	// ErrEmptyMessage is returned when a message has no content parts.
	ErrEmptyMessage = errors.New("conversationprojection: message has no content parts")

	// ErrInvalidRole is returned when a message role is missing or invalid.
	ErrInvalidRole = errors.New("conversationprojection: invalid message role")

	// ErrInvalidMessageIdentity is returned when a message identity string is malformed or invalid.
	ErrInvalidMessageIdentity = errors.New("conversationprojection: invalid message identity")

	// ErrInvalidMessageAnchor is returned when a message anchor is invalid.
	ErrInvalidMessageAnchor = errors.New("conversationprojection: invalid message anchor")

	// ErrAnchorNotFound is returned when an anchor cannot be resolved in a trajectory.
	ErrAnchorNotFound = errors.New("conversationprojection: anchor not found")

	// ErrPartialContentNotSupported is returned when an identity or anchor operation targets a partial content part rather than a complete message.
	ErrPartialContentNotSupported = errors.New("conversationprojection: partial content part identity is not supported")

	// ErrAnchorMissing is returned when a required fixed anchor cannot be resolved.
	ErrAnchorMissing = errors.New("conversationprojection: anchor missing")

	// ErrProjectionFailed is returned when projection cannot produce a valid backend call.
	ErrProjectionFailed = errors.New("conversationprojection: projection failed")

	// ErrTerminalUserNotFound is returned when no terminal forwardable user message exists.
	ErrTerminalUserNotFound = errors.New("conversationprojection: terminal forwardable user message not found")

	// ErrTerminalNotUser is returned when the terminal forwardable message is not a user message.
	ErrTerminalNotUser = errors.New("conversationprojection: terminal forwardable message is not user")

	// ErrOverlayNotFound is returned when an overlay cannot be found for deactivation.
	ErrOverlayNotFound = errors.New("conversationprojection: overlay not found")

	// ErrALegNotFound is returned when conversation-view state is requested for an unknown A-leg.
	ErrALegNotFound = errors.New("conversationprojection: a-leg not found")

	// ErrInvalidPlacement is returned when a StoredPlacement fails validation.
	ErrInvalidPlacement = errors.New("conversationprojection: invalid placement")

	// ErrInvalidSteeringMessage is returned when a StoredMessageV1 fails validation.
	ErrInvalidSteeringMessage = errors.New("conversationprojection: invalid steering message")

	// ErrInvalidAnchorMissingPolicy is returned when an AnchorMissingPolicy value is invalid.
	ErrInvalidAnchorMissingPolicy = errors.New("conversationprojection: invalid anchor missing policy")

	// ErrInvalidReasonCode is returned when a ReasonCode is missing or exceeds bounds.
	ErrInvalidReasonCode = errors.New("conversationprojection: invalid reason code")

	// ErrInvalidOverlayID is returned when an overlay identifier is missing or exceeds bounds.
	ErrInvalidOverlayID = errors.New("conversationprojection: invalid overlay id")
)
