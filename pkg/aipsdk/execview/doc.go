// Package execview holds stable, transport-agnostic identity and attempt views
// for feature plugins (design §2), plus [WithPrincipal] / [PrincipalFromContext] for
// the canonical principal in [context.Context] (set at the transport edge, read by the core).
// [WithFrontendID] / [FrontendIDFromContext] carry the auth wire frontend label for the same request
// (session-start audit alignment with auth decision events).
// Values are snapshots; plugins must not treat
// them as authoritative for mutation—use documented extension stages instead.
package execview
