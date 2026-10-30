// Package response defines the final-canonical-stream observer contract.
// Factories open read-only observers for the active surfaced B-leg; Observe
// receives post-response-hook and post-gate events immediately before client
// emit; Finish runs exactly once with a typed StreamOutcome.
package response
