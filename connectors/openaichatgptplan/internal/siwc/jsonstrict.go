package siwc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// The strict JSON reading of this package.
//
// A JSON document that repeats a key is MALFORMED here. The decoder keeps
// the LAST value of the repeated key, so the document decides which of the
// two values is read, and the value that is dropped is invisible to every
// check that follows: a header that repeats "alg", a payload that repeats
// "aud" and a key set that repeats a key id are the same defect, and the
// same rule refuses all three.
//
// The rule is COLLECT, not SAMPLE. The document is either understood in
// full or refused; the connector never reads the first of two values and
// drops the rest.
//
// # Member names are EXACT
//
// A member is read by its EXACT name. Go's typed JSON decoder matches a
// struct field's name case-INsensitively, so a document that carries both
// "aud" and "AUD" decides by itself which value the typed field holds:
// the typed decode SAMPLES by a different rule, and the check that follows
// would read a value the document never carried under the name it was
// asked for. A token whose audience is checked that way can carry the
// wrong audience and still pass.
//
// The members are therefore decoded into a map, whose keys are the exact
// names of the document. A member whose name differs only in case is a
// DIFFERENT member: it is carried and ignored, exactly like an extension
// claim the connector does not recognize. Nothing is refused for carrying
// an unknown name, so a legitimate extension claim is never collateral of
// the rule.
//
// The exact duplicate detection above is preserved: a document that repeats
// one name exactly is still refused.

// errDuplicateJSONKey reports a document that repeats a key.
//
// The error names neither the key nor the value: a document is untrusted
// input, and an attacker-chosen key name has no place in a diagnostic.
var errDuplicateJSONKey = errors.New("siwc: the JSON document repeats a key")

// errJSONTrailingContent reports content after the document's own value.
var errJSONTrailingContent = errors.New("siwc: the JSON document carries trailing content")

// errJSONNotAnObject reports a document that is not a JSON object.
var errJSONNotAnObject = errors.New("siwc: the JSON document is not an object")

// jsonMembers decodes a JSON object into its members, keyed by the EXACT
// member name.
//
// The failure is a CONSTANT: a document is untrusted input, and a
// diagnostic that repeated a part of it would put unvetted text into an
// operator- or support-facing error.
func jsonMembers(data []byte) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, errMalformedDocument
	}
	if members == nil {
		return nil, errJSONNotAnObject
	}
	return members, nil
}

// memberState is what a member's presence and shape are.
//
// The three states are DISTINCT on purpose. A member that is ABSENT says
// nothing at all; a member that is INVALID says something the connector
// cannot use. Reading the two alike is the defect that lets an arrived
// value become an omission - and an omission is the PERMISSIVE state: it
// enables a fallback, skips a check, or keeps a superseded authority
// alive.
//
// ARRIVED-BUT-UNUSABLE IS NOT ABSENT. A member that arrived and cannot be
// read is either used or refused, visibly; it is never quietly read as
// though it had not arrived.
type memberState int

const (
	// memberAbsent means the document does not carry the member, or
	// carries it as the JSON null.
	//
	// The JSON null IS the document's explicit "no value", and it says
	// exactly what an omitted member says. Every member is read the same
	// way, so the rule has no exception to remember.
	memberAbsent memberState = iota
	// memberInvalid means the document carries the member and the
	// connector cannot read it as the shape the member carries.
	memberInvalid
	// memberUsable means the document carries the member and it was read.
	memberUsable
)

// memberValue is a member's value together with the state of its presence
// and shape.
//
// The value is meaningful only when the state is memberUsable.
type memberValue[T any] struct {
	// Value is what the member carried.
	Value T
	// State says whether the member was absent, unusable, or read.
	State memberState
}

// usable reports whether the member was read.
func (v memberValue[T]) usable() bool { return v.State == memberUsable }

// memberRaw returns one member's raw bytes and the state of its presence.
//
// A member that is absent, or that carries the JSON null, is absent. A
// member that carries a value is returned with its bytes, and the CALLER
// decides whether the shape of that value is one it can use.
func memberRaw(members map[string]json.RawMessage, name string) memberValue[json.RawMessage] {
	raw, ok := members[name]
	if !ok || len(raw) == 0 {
		return memberValue[json.RawMessage]{State: memberAbsent}
	}
	if isJSONNull(raw) {
		return memberValue[json.RawMessage]{State: memberAbsent}
	}
	return memberValue[json.RawMessage]{Value: raw, State: memberUsable}
}

// isJSONNull reports whether a member's value is the JSON null.
func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// memberString returns one member's string and the state of its presence.
//
// A member that carries a value of another shape is INVALID: it ARRIVED,
// and the connector cannot read it. It is not an omission, and the caller
// must not treat it as one.
func memberString(members map[string]json.RawMessage, name string) memberValue[string] {
	raw := memberRaw(members, name)
	if raw.State != memberUsable {
		return memberValue[string]{State: raw.State}
	}
	var value string
	if err := json.Unmarshal(raw.Value, &value); err != nil {
		return memberValue[string]{State: memberInvalid}
	}
	return memberValue[string]{Value: value, State: memberUsable}
}

// memberNumber returns one member's number and the state of its presence.
//
// The number must be a JSON NUMBER. A QUOTED number is a string, and a
// string is not a NumericDate: Go's decoder accepts a quoted number into
// a json.Number, and that looseness would let a document express a date
// in a shape the specification does not allow.
//
// A member that carries a value of another shape is INVALID: it ARRIVED,
// and the connector cannot read it.
func memberNumber(members map[string]json.RawMessage, name string) memberValue[json.Number] {
	raw := memberRaw(members, name)
	if raw.State != memberUsable {
		return memberValue[json.Number]{State: raw.State}
	}
	trimmed := bytes.TrimSpace(raw.Value)
	if len(trimmed) == 0 || trimmed[0] == '"' {
		return memberValue[json.Number]{State: memberInvalid}
	}
	var value json.Number
	if err := json.Unmarshal(raw.Value, &value); err != nil {
		return memberValue[json.Number]{State: memberInvalid}
	}
	return memberValue[json.Number]{Value: value, State: memberUsable}
}

// memberArray returns one member's elements and the state of its presence.
//
// The elements are read as raw bytes, so a caller that needs to look at
// each of them does so itself: the array is never sampled.
//
// A member that carries a value of another shape is INVALID: it ARRIVED,
// and the connector cannot read it.
func memberArray(members map[string]json.RawMessage, name string) memberValue[[]json.RawMessage] {
	raw := memberRaw(members, name)
	if raw.State != memberUsable {
		return memberValue[[]json.RawMessage]{State: raw.State}
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw.Value, &values); err != nil {
		return memberValue[[]json.RawMessage]{State: memberInvalid}
	}
	if values == nil {
		return memberValue[[]json.RawMessage]{State: memberInvalid}
	}
	return memberValue[[]json.RawMessage]{Value: values, State: memberUsable}
}

// documentString reads one member of a document that carries a string.
//
// The contract is the SAME for every member of every document: an absent
// member is the zero value, and a member that ARRIVED with a shape the
// connector cannot read makes the document UNREADABLE.
//
// The two states are never read alike. An omission is the PERMISSIVE
// state - it enables a fallback, skips a check, or keeps a superseded
// authority alive - and a value that arrived and cannot be read must not
// become one.
func documentString(members map[string]json.RawMessage, name string) (string, error) {
	value := memberString(members, name)
	switch value.State {
	case memberAbsent:
		return "", nil
	case memberInvalid:
		return "", errMalformedDocument
	default:
		return value.Value, nil
	}
}

// documentNumber reads one member of a document that carries a number.
//
// An absent member is the zero value, and a member that ARRIVED with a
// shape the connector cannot read makes the document unreadable.
//
// The PRECISION is stated, and it is this path's own: the member must be
// a WHOLE number of seconds. The value is narrowed to an integer here, and
// a downward rounding would move a lifetime or an earliest refresh time
// silently - a token that expires half a second later than the provider
// said, or a refresh that is attempted half a second early.
//
// The JWT path is different ON PURPOSE: a JWT NumericDate may carry a
// fraction, and there the fraction is honoured (numericDate keeps it and
// the comparison happens in seconds). A provider's OAuth response is not a
// JWT, and its lifetime and its earliest refresh time are whole seconds.
func documentNumber(members map[string]json.RawMessage, name string) (int64, error) {
	value := memberNumber(members, name)
	switch value.State {
	case memberAbsent:
		return 0, nil
	case memberInvalid:
		return 0, errMalformedDocument
	}
	seconds, state := numericDate(value)
	if state != memberUsable {
		return 0, errMalformedDocument
	}
	if seconds != math.Trunc(seconds) {
		return 0, errMalformedDocument
	}
	return int64(seconds), nil
}

// rejectDuplicateJSONKeys refuses a JSON document that repeats a key.
//
// The returned errors are CONSTANT: they name no key, no value and no
// offset, so a document that carries credential material in an unexpected
// place cannot put that material into a classified cause.
func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	// The document holds exactly one value: trailing content is refused
	// rather than ignored, so a document cannot hide a second document
	// behind the first.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errJSONTrailingContent
	}
	return nil
}

// scanJSONValue reads one JSON value and refuses a repeated key inside it.
func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errDuplicateJSONKey
			}
			if _, duplicate := seen[key]; duplicate {
				return errDuplicateJSONKey
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		// The closing brace.
		if _, err := decoder.Token(); err != nil {
			return err
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		// The closing bracket.
		if _, err := decoder.Token(); err != nil {
			return err
		}
	}
	return nil
}
