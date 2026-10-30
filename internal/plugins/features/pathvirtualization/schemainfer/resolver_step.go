package schemainfer

import (
	"encoding/json"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// This file is the seam between the schema-assisted inference step and the selector
// resolution order that consults it. It exists because the two halves have
// deliberately different dependencies:
//
//   - the resolution order lives in the lexical core, which imports no repository
//     package at all so its host-authority guard can prove the pure path policy
//     holds no operating-system or filesystem authority;
//   - the inference step reads declared tool-schema bytes, which the canonical tool
//     contract owns, so it lives in this subpackage and may import that contract.
//
// The lexical core therefore declares the optional-inference port and this package
// satisfies it. The port takes declared bytes only, so nothing here widens it: no
// tool name and no description crosses the seam, which is what keeps exact-name
// authority in the profile layers and prose out of selector selection.

// InferArgumentSelectors implements the lexical core's optional-inference port, so
// one compiled Inferrer can be bound straight into a resolver as step 3 of the
// resolution order.
//
// It adds nothing to the inference policy. The declared bytes are read exactly as
// InferArguments reads them, through the same canonical tool contract, and the same
// refusal applies: a schema that proves no path key publishes no selector, and the
// port has no way to report a mode, so binding inference can never enable opaque
// rewriting for a tool nothing explicitly marked as path-oriented (requirement 3.8).
//
// A nil *Inferrer satisfies the port and publishes nothing, which is what a refused
// vocabulary leaves behind.
func (r *Inferrer) InferArgumentSelectors(declaredSchema []byte) pathvirtualization.SelectorSet {
	result := r.InferArguments(aipapi.ToolDef{Parameters: json.RawMessage(declaredSchema)})
	if result.Outcome != OutcomeInferred {
		// The bounded reason stays with the caller that recorded it. The port has one
		// shape only, and a non-inferred outcome publishes nothing at all rather than
		// the subset that happened to be provable.
		return nil
	}
	return result.Pointers
}
