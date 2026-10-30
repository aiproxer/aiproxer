// Package ingressdefense owns provider- and protocol-neutral ingress
// self-defense policy plus the bounded, process-local, exact-address hostile
// source state it drives. It is default kernel security for the standard HTTP
// data plane and stays required when optional feature plugins are absent, so it
// is not a feature plugin and not infrastructure policy.
//
// The package knows nothing about HTTP paths, headers, auth providers,
// frontends, routing, providers, storage, or clocks. Request classification and
// responses belong to internal/stdhttp; configuration decoding and defaults
// belong to internal/core/config; process and generation composition belongs to
// internal/infra/runtimebundle.
package ingressdefense
