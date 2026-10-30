package session

import "errors"

// Kind identifies the bounded classification attached to a session.
type Kind string

const (
	// KindUnknown is the zero-value session classification.
	KindUnknown Kind = ""
	// KindCodingAgent marks a session with accepted coding-agent evidence.
	KindCodingAgent Kind = "coding_agent"
)

// ClassificationSource identifies the bounded source of a positive classification.
type ClassificationSource string

const (
	// SourceLocalIdentity records decisive local client-identity evidence.
	SourceLocalIdentity ClassificationSource = "local_identity"
	// SourceLocalTooling records decisive local tool-category evidence.
	SourceLocalTooling ClassificationSource = "local_tooling"
	// SourceRemote records a positive remote-classifier decision.
	SourceRemote ClassificationSource = "remote_classifier"
)

// ConfidenceBand is the bounded confidence vocabulary exposed to SDK consumers.
type ConfidenceBand string

const (
	// ConfidenceUnknown is used only by the all-zero unknown classification.
	ConfidenceUnknown ConfidenceBand = ""
	// ConfidenceHigh is the V1 positive confidence band.
	ConfidenceHigh ConfidenceBand = "high"
)

// MaxEvidenceCodeBytes is the maximum length of a classification evidence code.
const MaxEvidenceCodeBytes = 64

// EvidenceCode is an opaque, bounded diagnostic code. It must contain only
// ASCII letters, digits, dots, underscores, or hyphens.
type EvidenceCode string

// Classification is an immutable-by-convention scalar snapshot of advisory
// session classification. Its zero value represents unknown; it is not an
// authorization, identity, routing, or billing decision.
type Classification struct {
	Kind       Kind
	Source     ClassificationSource
	Confidence ConfidenceBand
	Evidence   EvidenceCode
	Revision   uint64
}

var (
	errUnknownClassificationMustBeZero = errors.New("session: unknown classification must be the zero value")
	errUnsupportedClassificationKind   = errors.New("session: unsupported classification kind")
	errInvalidClassificationSource     = errors.New("session: invalid classification source")
	errInvalidClassificationConfidence = errors.New("session: invalid classification confidence")
	errEmptyClassificationEvidence     = errors.New("session: positive classification requires an evidence code")
	errEvidenceCodeTooLong             = errors.New("session: classification evidence code exceeds byte limit")
	errEvidenceCodeHasUnsafeCharacters = errors.New("session: classification evidence code has unsafe characters")
	errClassificationRevisionRequired  = errors.New("session: positive classification requires a nonzero revision")
)

// IsCodingAgent reports whether this is a well-formed positive classification.
// Malformed positive values are treated as unknown by consumers.
func (c Classification) IsCodingAgent() bool {
	return c.Kind == KindCodingAgent && c.Validate() == nil
}

// Validate checks that the snapshot uses only the bounded V1 vocabulary and
// that unknown is represented by the all-zero value.
func (c Classification) Validate() error {
	if c.Kind == KindUnknown {
		if c != (Classification{}) {
			return errUnknownClassificationMustBeZero
		}
		return nil
	}
	if c.Kind != KindCodingAgent {
		return errUnsupportedClassificationKind
	}
	switch c.Source {
	case SourceLocalIdentity, SourceLocalTooling, SourceRemote:
	default:
		return errInvalidClassificationSource
	}
	if c.Confidence != ConfidenceHigh {
		return errInvalidClassificationConfidence
	}
	if c.Evidence == "" {
		return errEmptyClassificationEvidence
	}
	if len(c.Evidence) > MaxEvidenceCodeBytes {
		return errEvidenceCodeTooLong
	}
	for i := 0; i < len(c.Evidence); i++ {
		ch := c.Evidence[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return errEvidenceCodeHasUnsafeCharacters
	}
	if c.Revision == 0 {
		return errClassificationRevisionRequired
	}
	return nil
}
