// Package productsmoke holds the product-behavior smoke: Go tests, compiled
// only with the productsmoke build tag, that start the real aipstd binary,
// point it at loopback fake providers and drive client journeys through it.
//
// This file deliberately carries no build tag and imports only the standard
// library. Tooling (for example the devcheck product-smoke task) imports the
// required-journey list from here without compiling or running the journeys.
package productsmoke

// Journey is one required product-smoke journey.
type Journey struct {
	// ID is the exact `go test` subtest name, for example
	// "TestProduct_Startup/ready".
	ID string
	// POSIXOnly marks a journey that needs POSIX signal or process semantics.
	// A Windows required list excludes these entries.
	POSIXOnly bool
}

// Required lists every journey the product smoke must observe passing.
var Required = []Journey{
	{ID: "TestProduct_Startup/ready"},
	{ID: "TestProduct_Startup/invalid_config"},
	{ID: "TestProduct_Lifecycle/canary_credential"},
	{ID: "TestProduct_Lifecycle/reload_valid"},
	{ID: "TestProduct_Lifecycle/reload_invalid"},
	{ID: "TestProduct_Protocols/openai-legacy/stream"},
	{ID: "TestProduct_Protocols/openai-legacy/collect"},
	{ID: "TestProduct_Protocols/openai-responses/stream"},
	{ID: "TestProduct_Protocols/openai-responses/collect"},
	{ID: "TestProduct_Protocols/anthropic/stream"},
	{ID: "TestProduct_Protocols/anthropic/collect"},
	{ID: "TestProduct_Protocols/gemini/stream"},
	{ID: "TestProduct_Protocols/gemini/collect"},
	{ID: "TestProduct_Protocols/openresponses/stream"},
	{ID: "TestProduct_Protocols/openresponses/collect"},
	// SystemOne is collect-only: the shipped frontend hardcodes non-streaming
	// decision delivery, so there is deliberately no systemone/stream journey.
	{ID: "TestProduct_Protocols/systemone/collect"},
	// Feature journeys prove one opt-in feature through the shipped binary.
	{ID: "TestProduct_Features/model_system_prompt"},
	{ID: "TestProduct_Continuity/tool_round_trip"},
	{ID: "TestProduct_Failure/pre_output_fallback"},
	{ID: "TestProduct_Failure/post_output_no_retry"},
	{ID: "TestProduct_Failure/client_cancel"},
	// The shutdown journey needs POSIX signal semantics; the Windows required
	// list excludes it.
	{ID: "TestProduct_Shutdown/graceful", POSIXOnly: true},
}

// RequiredForOS returns the required journeys that apply to goos. A Windows
// required list excludes POSIX-only journeys; every other host requires the
// whole list. The devcheck product-smoke task reconciles a run with the same
// POSIXOnly rule.
func RequiredForOS(goos string) []Journey {
	out := make([]Journey, 0, len(Required))
	for _, journey := range Required {
		if journey.POSIXOnly && goos == "windows" {
			continue
		}
		out = append(out, journey)
	}
	return out
}
