package continuation

import "errors"

var (
	// ErrPreviousResponseNotFound is returned for missing, expired, unauthorized,
	// evicted, or incompatible previous_response_id lookups. Callers must treat
	// all causes identically on the wire.
	ErrPreviousResponseNotFound = errors.New("continuation: previous response not found")

	// ErrChainDepthExceeded rejects continuation chains beyond configured depth.
	ErrChainDepthExceeded = errors.New("continuation: chain depth exceeded")

	// ErrCycleDetected rejects cyclic previous_response_id chains.
	ErrCycleDetected = errors.New("continuation: cycle detected")

	// ErrLineageMismatch rejects a chain whose provider-bound links disagree.
	ErrLineageMismatch = errors.New("continuation: lineage mismatch")

	// ErrMaterializedSizeExceeded rejects reconstructed context above byte bounds.
	ErrMaterializedSizeExceeded = errors.New("continuation: materialized size exceeded")

	// ErrMaterializedItemsExceeded rejects reconstructed context above item bounds.
	ErrMaterializedItemsExceeded = errors.New("continuation: materialized items exceeded")

	// ErrRecordNotReady rejects lookup of a reserved but non-terminal record.
	ErrRecordNotReady = errors.New("continuation: record not terminal")

	// ErrInvalidPolicy rejects malformed persistence policy values.
	ErrInvalidPolicy = errors.New("continuation: invalid storage policy")

	// ErrStorageLimitExceeded rejects records or stores above configured bounds.
	ErrStorageLimitExceeded = errors.New("continuation: storage limit exceeded")

	// ErrIncompleteNotEligible rejects incomplete responses when policy disallows them.
	ErrIncompleteNotEligible = errors.New("continuation: incomplete record is not eligible")

	// ErrRecordNotEligible rejects terminal records that cannot be continued.
	ErrRecordNotEligible = errors.New("continuation: record is not eligible")

	// ErrStorageFailure classifies an unavailable or failed persistence boundary.
	ErrStorageFailure = errors.New("continuation: storage failure")

	// ErrNativeReferencesUnprotected rejects durable writes when provider-native
	// evidence has no configured encryption/protection boundary.
	ErrNativeReferencesUnprotected = errors.New("continuation: native references require protected storage")

	// ErrStoreClosed rejects operations attempted after a store has been closed.
	ErrStoreClosed = errors.New("continuation: store closed")
)
