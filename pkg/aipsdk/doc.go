// Package aipsdk defines stable plugin-facing contracts used by official and
// external plugins. Hook interfaces live in the nested package aipsdk/hooks.
//
// Authentication and backend security: [auth] has protocol-neutral request metadata, decisions,
// and event DTOs; [BackendSecurityProfile] declares backend credential posture for registry startup
// validation. Neither carries plugin-private config blobs—only registered metadata and stable enums.
//
// Ownership: this tree holds plugin registration, hook contracts, and SDK-facing types,
// not application orchestration. Routing, recovery, and extension-stage policy stay in
// internal/core. Do not import internal packages, internal/stdhttp, or composition assembly
// packages from here; pkg/aipsdk/execview holds the canonical principal context key;
// pkg/aipsdk/transport/httpauth holds HTTP-native auth provider and error-render contracts.
// Factory registration for the standard distribution still happens in internal/pluginreg at the
// composition root, not from types defined here.
//
// Backend, frontend, and feature factories for the reference distribution are registered in
// internal/pluginreg (RegisterBackend, RegisterFrontend, RegisterFeature) using opaque YAML nodes
// and [FrontendMountOptions] for frontend HTTP wiring;
// StandardDistributionRequirements lists ids validated at startup.
//
// [BackendBuild] is intentionally opaque (see factory.go) so this package never depends on core runtime types.
//
// HTTP seam: the root package is the sanctioned minimal net/http surface of this tree.
// It imports net/http only for [FrontendMount] wiring and the [AuthErrorRenderer]
// contracts (factory.go, auth_error_render.go), and log/slog only for the host
// logger a frontend mount receives (factory.go); that placement is deliberate so
// internal/core can reference them without pulling pkg/aipsdk/transport into the
// orchestration dependency closure (see the comment on [AuthErrorRenderInput]).
// Every other subpackage must stay free of net/http; the sole exception is
// pkg/aipsdk/transport (including transport/httpauth), which owns HTTP-native
// auth provider and error-render contracts.
package aipsdk
