// Package jsonshape provides protocol-neutral JSON size and shape preflight
// using encoding/json.Decoder.Token. It is shared by frontend request-body
// guards and tool-call schema/argument hardening. Errors never include payload
// bytes, keys, or values. Request envelopes keep duplicate-member acceptance;
// tool schema and tool argument profiles reject duplicate names.
package jsonshape
