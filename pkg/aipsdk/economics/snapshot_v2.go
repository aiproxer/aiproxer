package economics

import (
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// MaxSnapshotContentRefBytes bounds the durable resolver key carried by a
	// V2 contract. Resolution itself remains the responsibility of the
	// snapshot catalog; this package never performs I/O.
	MaxSnapshotContentRefBytes = 512
	// SnapshotContentHashBytes is the hexadecimal SHA-256 digest length used for
	// immutable snapshot and input-set content identity.
	SnapshotContentHashBytes = 64
)

// SnapshotContentRef identifies immutable material that a resolver can load
// and verify. A snapshot ID/version without this pair is only a label and is
// not sufficient for replayable V2 valuation.
type SnapshotContentRef struct {
	ContentRef  string `json:"content_ref"`
	ContentHash string `json:"content_hash"`
}

// Validate checks the bounded resolver key and canonical lowercase SHA-256
// content digest. It deliberately does not fetch or trust the referenced
// material; that belongs to the catalog at the validation boundary that has
// access to it.
func (r SnapshotContentRef) Validate() error {
	if err := validatePublicRef("snapshot content ref", r.ContentRef); err != nil {
		return err
	}
	if len(r.ContentRef) > MaxSnapshotContentRefBytes {
		return fmt.Errorf("economics: snapshot content ref exceeds %d bytes", MaxSnapshotContentRefBytes)
	}
	if err := validateContentHash("snapshot content hash", r.ContentHash); err != nil {
		return err
	}
	return nil
}

func cloneSnapshotContentRef(in *SnapshotContentRef) *SnapshotContentRef {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func validateContentHash(field, value string) error {
	if len(value) != SnapshotContentHashBytes {
		return fmt.Errorf("economics: %s must be a lowercase SHA-256 hex digest", field)
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("economics: %s must use lowercase hexadecimal", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("economics: %s must be hexadecimal: %v", field, err)
	}
	return nil
}

func validateSnapshotMaterial(name string, snapshotID string, content *SnapshotContentRef, required bool, target error) error {
	if content == nil {
		if required {
			return fmt.Errorf("%w: %s immutable content reference required", target, name)
		}
		return nil
	}
	if snapshotID == "" {
		return fmt.Errorf("%w: %s snapshot identity required with content reference", target, name)
	}
	if err := content.Validate(); err != nil {
		return fmt.Errorf("%w: %s: %v", target, name, err)
	}
	return nil
}

func validateInputSetIdentity(name, value string, required bool, target error) error {
	if value == "" {
		if required {
			return fmt.Errorf("%w: %s required for derived valuation", target, name)
		}
		return nil
	}
	if err := validateContentHash(name, value); err != nil {
		return fmt.Errorf("%w: %v", target, err)
	}
	return nil
}

func validateQualifierSnapshot(hash string, content *SnapshotContentRef, target error) error {
	if hash != "" {
		if err := validateContentHash("qualifier snapshot", hash); err != nil {
			return fmt.Errorf("%w: %v", target, err)
		}
	}
	if content == nil {
		return nil
	}
	if err := content.Validate(); err != nil {
		return fmt.Errorf("%w: qualifier snapshot: %v", target, err)
	}
	if hash != "" && hash != content.ContentHash {
		return fmt.Errorf("%w: qualifier snapshot hash/content mismatch", target)
	}
	return nil
}

func derivedBasis(b ValuationBasis) bool {
	switch b {
	case BasisLocalExpected, BasisProviderQuantityLocal, BasisCustomerPolicy:
		return true
	default:
		return false
	}
}

func validateRatingInputSnapshotContext(in RatingInput) error {
	derived := derivedBasis(in.Basis)
	// The trusted post-usage rater owns input-set identity. It recomputes and
	// fills an empty hash after canonical replay, while a supplied non-empty
	// hash is verified at that boundary. Public input validation must therefore
	// permit the pre-verification empty state.
	if err := validateInputSetIdentity("rating input set hash", in.InputSetHash, false, ErrInvalidRating); err != nil {
		return err
	}
	if err := validateSnapshotMaterial("rater", in.Rater.ID, in.RaterContent, derived, ErrInvalidRating); err != nil {
		return err
	}
	tariffRequired := in.Basis == BasisLocalExpected || in.Basis == BasisProviderQuantityLocal
	if err := validateSnapshotMaterial("tariff", in.Tariff.ID, in.TariffContent, tariffRequired || (derived && !isZeroRatingRef(in.Tariff)), ErrInvalidRating); err != nil {
		return err
	}
	policyRequired := in.Basis == BasisCustomerPolicy
	if err := validateSnapshotMaterial("policy", in.Policy.ID, in.PolicyContent, policyRequired || (derived && !isZeroPolicyRef(in.Policy)), ErrInvalidRating); err != nil {
		return err
	}
	if len(in.EffectiveQualifiers) > 0 && in.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required when effective qualifiers are present", ErrInvalidRating)
	}
	if derived && in.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required for derived valuation", ErrInvalidRating)
	}
	return validateQualifierSnapshot("", in.QualifierSnapshotRef, ErrInvalidRating)
}

func validateQuoteSnapshotContext(in QuoteInput) error {
	derived := derivedBasis(in.Basis)
	if err := validateInputSetIdentity("quote input set hash", in.InputSetHash, derived, ErrInvalidQuote); err != nil {
		return err
	}
	tariffRequired := in.Basis == BasisLocalExpected || in.Basis == BasisProviderQuantityLocal
	if err := validateSnapshotMaterial("tariff", in.Tariff.ID, in.TariffContent, tariffRequired || (derived && !isZeroRatingRef(in.Tariff)), ErrInvalidQuote); err != nil {
		return err
	}
	policyRequired := in.Basis == BasisCustomerPolicy
	if err := validateSnapshotMaterial("policy", in.Policy.ID, in.PolicyContent, policyRequired || (derived && !isZeroPolicyRef(in.Policy)), ErrInvalidQuote); err != nil {
		return err
	}
	if len(in.EffectiveQualifiers) > 0 && in.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required when effective qualifiers are present", ErrInvalidQuote)
	}
	if derived && in.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required for derived quote", ErrInvalidQuote)
	}
	return validateQualifierSnapshot("", in.QualifierSnapshotRef, ErrInvalidQuote)
}

func validateExposureQuoteSnapshotContext(q ExposureQuote) error {
	derived := derivedBasis(q.Basis)
	if err := validateInputSetIdentity("quote input set hash", q.InputSetHash, derived, ErrInvalidQuote); err != nil {
		return err
	}
	tariffRequired := q.Basis == BasisLocalExpected || q.Basis == BasisProviderQuantityLocal
	if err := validateSnapshotMaterial("tariff", q.Tariff.ID, q.TariffContent, tariffRequired || (derived && !isZeroRatingRef(q.Tariff)), ErrInvalidQuote); err != nil {
		return err
	}
	policyRequired := q.Basis == BasisCustomerPolicy
	if err := validateSnapshotMaterial("policy", q.Policy.ID, q.PolicyContent, policyRequired || (derived && !isZeroPolicyRef(q.Policy)), ErrInvalidQuote); err != nil {
		return err
	}
	if derived && q.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required for derived quote", ErrInvalidQuote)
	}
	return validateQualifierSnapshot("", q.QualifierSnapshotRef, ErrInvalidQuote)
}

func validateValuationSnapshotContext(v Valuation) error {
	derived := derivedBasis(v.Basis)
	if err := validateInputSetIdentity("input set hash", v.InputSetHash, derived, ErrInvalidValuation); err != nil {
		return err
	}
	if err := validateSnapshotMaterial("rater", v.Rater.ID, v.RaterContent, derived, ErrInvalidValuation); err != nil {
		return err
	}
	tariffRequired := v.Basis == BasisLocalExpected || v.Basis == BasisProviderQuantityLocal
	if err := validateSnapshotMaterial("tariff", v.Tariff.ID, v.TariffContent, tariffRequired || (derived && !isZeroRatingRef(v.Tariff)), ErrInvalidValuation); err != nil {
		return err
	}
	policyRequired := v.Basis == BasisCustomerPolicy
	if err := validateSnapshotMaterial("policy", v.Policy.ID, v.PolicyContent, policyRequired || (derived && !isZeroPolicyRef(v.Policy)), ErrInvalidValuation); err != nil {
		return err
	}
	if derived && v.QualifierSnapshotRef == nil {
		return fmt.Errorf("%w: qualifier snapshot content reference required for derived valuation", ErrInvalidValuation)
	}
	return nil
}
