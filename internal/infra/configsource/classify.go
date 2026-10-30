package configsource

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

// ClassifyBytes classifies a raw snapshot against the bounded source contract
// before typed YAML decode. It does not decode YAML; empty/whitespace/oversize
// are rejected here so decode never sees them.
//
// maxBytes <= 0 selects DefaultMaxBytes. Classification is secret-safe: errors
// never include the raw payload.
func ClassifyBytes(raw []byte, maxBytes int64) (Category, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if int64(len(raw)) > maxBytes {
		return CategoryOversize, oversizeErr(maxBytes)
	}
	if len(raw) == 0 {
		return CategoryEmpty, integrityErr(CategoryEmpty)
	}
	if isWhitespaceOnly(raw) {
		return CategoryWhitespace, integrityErr(CategoryWhitespace)
	}
	return CategoryOK, nil
}

func isWhitespaceOnly(raw []byte) bool {
	i := 0
	for i < len(raw) {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			// Non-UTF8 bytes are not whitespace; leave to decode.
			return false
		}
		if !unicode.IsSpace(r) {
			return false
		}
		i += size
	}
	return true
}

// ClassifyPathPresence maps filesystem presence to a source category without
// reading contents. Used by table fixtures and the future ReadStable path.
func ClassifyPathPresence(exists, isRegular bool) (Category, error) {
	if !exists {
		return CategoryMissing, integrityErr(CategoryMissing)
	}
	if !isRegular {
		return CategoryUnsupportedType, integrityErr(CategoryUnsupportedType)
	}
	return CategoryOK, nil
}

// ClassifyStability rejects torn/partial reads where handle metadata changed
// across the bounded read window (requirement 2.1–2.2).
func ClassifyStability(beforeSize, afterSize int64, beforeID, afterID FileIdentity) (Category, error) {
	if beforeSize != afterSize || beforeID != afterID {
		return CategoryUnstable, integrityErr(CategoryUnstable)
	}
	return CategoryOK, nil
}

// ClassifyAtomicReplacement enforces requirement 2.9: changed content with the
// same handle identity is a non-atomic in-place update; identical digest may
// no-op; a new identity is eligible for reload.
type AtomicResult string

const (
	AtomicEligible AtomicResult = "eligible"
	AtomicNoop     AtomicResult = "noop"
	AtomicReject   AtomicResult = "reject_non_atomic"
)

func ClassifyAtomicReplacement(active ActiveSourceVersion, candidate SourceSnapshot) (AtomicResult, Category, error) {
	// Linux's ext4 dev+inode identity is safe only while the accepted source
	// descriptor remains pinned. Validate and hold both leases for the complete
	// comparison, including direct callers of this classifier.
	if active.HandleIdentity.Platform == "linux" || candidate.HandleIdentity.Platform == "linux" {
		if active.HandleIdentity.Platform != "linux" || candidate.HandleIdentity.Platform != "linux" ||
			active.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease ||
			candidate.HandleIdentity.Scheme != identitySchemeLinuxExt4Lease ||
			!active.leaseRequired || !candidate.leaseRequired || active.leaseUnavailable || candidate.leaseUnavailable {
			return AtomicReject, CategoryNonAtomicUpdate, leaseIntegrityErr("lease_unavailable")
		}
		activeBorrow, err := active.Borrow()
		if err != nil {
			return AtomicReject, CategoryNonAtomicUpdate, err
		}
		defer activeBorrow.Release()
		candidateBorrow, err := candidate.borrowCandidate()
		if err != nil {
			return AtomicReject, CategoryNonAtomicUpdate, err
		}
		defer candidateBorrow.Release()
		if err := activeBorrow.validateProvenance(&active); err != nil {
			return AtomicReject, CategoryNonAtomicUpdate, err
		}
		if err := candidateBorrow.validateSnapshot(&candidate); err != nil {
			return AtomicReject, CategoryNonAtomicUpdate, err
		}
		if !sameLeaseDevice(active, candidate) {
			return AtomicReject, CategoryNonAtomicUpdate, leaseIntegrityErr("lease_device")
		}
	}
	// Identities from different platforms or provenance schemes are not
	// comparable. A coarser identity cannot prove the candidate is a different
	// physical file, so fail closed instead of treating it as a new identity.
	if active.HandleIdentity.Platform != candidate.HandleIdentity.Platform ||
		active.HandleIdentity.Scheme != candidate.HandleIdentity.Scheme {
		return AtomicReject, CategoryNonAtomicUpdate, integrityErr(CategoryNonAtomicUpdate)
	}
	sameID := active.HandleIdentity == candidate.HandleIdentity
	sameDigest := bytes.Equal(active.PrivateDigest[:], candidate.PrivateDigest[:])
	switch {
	case sameID && sameDigest:
		return AtomicNoop, CategoryOK, nil
	case sameID && !sameDigest:
		return AtomicReject, CategoryNonAtomicUpdate, integrityErr(CategoryNonAtomicUpdate)
	default:
		return AtomicEligible, CategoryOK, nil
	}
}

func sameLeaseDevice(active ActiveSourceVersion, candidate SourceSnapshot) bool {
	return active.devMajor == candidate.devMajor && active.devMinor == candidate.devMinor
}
