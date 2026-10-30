// Package openresponses hosts the reusable full-path conformance deployment
// harness integration tests (spec Phase 7, Task 7.3). Each test selects one
// authoritative FE×BE cell through the generic conformance.Deploy selector and
// exercises a real frontend, the real core executor, a real backend, and an
// injectable reference-provider origin across JSON, SSE, compact, and WebSocket
// client transports.
//
// Harness API surface (test-support, in internal/testkit/conformance):
//
//	Deploy(tb, DeploymentSpec) *Deployment   // generic cell selector
//	Deployment.Client / RequestCount / OriginFor / CandidateOrigin / Close
//	ClientEntrypoint.RoundTrip(ctx, prompt) (RoundTripResult, error)
//
// Base smoke runs with contract fakes; independent-emulator evidence is
// deferred to Phase 8. The OpenResponses→ACP item projector (Phase 8.3) and the
// OpenRouter/NVIDIA configured provider-mode routes (Phase 8.3) are green. The
// explicit-legacy-operation → OpenResponses backend column (Phase 8.4) still
// returns before any upstream request.
package openresponses
