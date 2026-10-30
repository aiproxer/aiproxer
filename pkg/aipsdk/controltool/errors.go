package controltool

import "errors"

// Validation sentinels classify malformed control-tool contracts without
// exposing implementation details to callers.
var (
	ErrInvalidProvider = errors.New("controltool: invalid provider")
	ErrInvalidSpec     = errors.New("controltool: invalid spec")
	ErrInvalidCall     = errors.New("controltool: invalid call")
	ErrInvalidMeta     = errors.New("controltool: invalid meta")
	ErrInvalidOutcome  = errors.New("controltool: invalid outcome")
)
