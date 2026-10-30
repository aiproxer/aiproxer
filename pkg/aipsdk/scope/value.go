package scope

import "fmt"

var _ fmt.Stringer = Value{}

// Value is a presence-aware string used for attribution fields where unknown
// must be distinguished from a known-but-empty value. The zero Value is unknown.
type Value struct {
	Known bool   `json:"known"`
	Value string `json:"value"`
}

// Unknown returns a Value representing an unknown attribution field.
func Unknown() Value { return Value{Known: false} }

// Known returns a Value representing a known attribution field, including a
// known-empty string ("").
func Known(s string) Value { return Value{Known: true, Value: s} }

// IsUnknown reports whether the value is unknown (not supplied).
func (v Value) IsUnknown() bool { return !v.Known }

// IsKnown reports whether the value is known, including known-empty.
func (v Value) IsKnown() bool { return v.Known }

// IsKnownEmpty reports whether the value is known and intentionally empty.
func (v Value) IsKnownEmpty() bool { return v.Known && v.Value == "" }

// String returns the underlying string for known values and "" for unknown.
func (v Value) String() string { return v.Value }

// Equal reports whether two Values share presence and string content.
func (v Value) Equal(o Value) bool { return v.Known == o.Known && v.Value == o.Value }
