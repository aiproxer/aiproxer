package sessionclassification

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxClassifierIDBytes caps a classifier's stable identity by UTF-8 byte length.
const MaxClassifierIDBytes = 256

// ErrInvalidClassifier identifies a nil classifier, an unavailable identity,
// or an identity that violates the bounded classifier contract.
var ErrInvalidClassifier = errors.New("sessionclassification: invalid classifier")

// Validate enforces the bounded evidence carrier contract under the caller's
// byte budget: the already-accepted client identity must fit, and the category
// bits must stay inside the derived set.
//
// It deliberately does not define a second client-identity acceptance policy:
// the User-Agent must already satisfy the canonical identity policy (or the
// same frontend acceptance helper on the large-body wire path), and this check
// only refuses to forward an unbounded or undefined carrier
// (requirements 5.2, 5.5, 7.1, 7.2).
func (e Evidence) Validate(maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("sessionclassification: evidence byte budget must be > 0, got %d", maxBytes)
	}
	if int64(len(e.ClientUserAgent)) > maxBytes {
		return fmt.Errorf("sessionclassification: evidence client user agent exceeds %d bytes", maxBytes)
	}
	if e.ToolCategories&^DefinedToolCategoryBits != 0 {
		return fmt.Errorf("sessionclassification: evidence tool categories contain undefined bits")
	}
	return nil
}

// ValidateClassifierID accepts a non-empty, bounded, control-free stable ID.
func ValidateClassifierID(id string) error {
	if id == "" || len(id) > MaxClassifierIDBytes || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return ErrInvalidClassifier
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return ErrInvalidClassifier
		}
	}
	return nil
}

// ClassifierIdentity validates a classifier's stable identity during feature
// composition and returns the bounded ID retained by the generation. Typed-nil
// classifiers and ID panics fail closed without exposing classifier text.
func ClassifierIdentity(classifier Classifier) (id string, err error) {
	if isNilClassifier(classifier) {
		return "", ErrInvalidClassifier
	}
	defer func() {
		if recover() != nil {
			id = ""
			err = fmt.Errorf("%w: classifier identity unavailable", ErrInvalidClassifier)
		}
	}()
	id = classifier.ID()
	if err := ValidateClassifierID(id); err != nil {
		return "", err
	}
	return id, nil
}

func isNilClassifier(classifier Classifier) bool {
	if classifier == nil {
		return true
	}
	value := reflect.ValueOf(classifier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}
