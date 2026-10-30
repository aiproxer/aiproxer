// package catalog provides the auto-discovered Codex model catalog shared
// by the openai-codex, openai-codex-v2 and openai-codex-app-server connectors.
//
// The catalog is sourced at runtime from `codex debug models` (verbatim JSON)
// and parsed by [Parse]. On discovery failure the connectors fall back to a
// shipped snapshot embedded via [FallbackBytes]. No model slugs are hardcoded
// in the connectors; the routable slug list and per-model reasoning-effort
// settings come from the parsed catalog.
package catalog
