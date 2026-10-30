package capsule

import "errors"

var (
	ErrInvalidCapsule            = errors.New("capsule: invalid capsule")
	ErrInvalidBranch             = errors.New("capsule: invalid branch binding")
	ErrDigestMismatch            = errors.New("capsule: content digest mismatch")
	ErrBranchMismatch            = errors.New("capsule: branch binding mismatch")
	ErrStaleRevision             = errors.New("capsule: stale revision")
	ErrUnknownSupersede          = errors.New("capsule: unknown supersedes reference")
	ErrFactConflict              = errors.New("capsule: conflicting duplicate fact")
	ErrCapsuleTooLarge           = errors.New("capsule: cannot fit capsule budget")
	ErrInvalidDecisionTransition = errors.New("capsule: invalid decision transition")
)
