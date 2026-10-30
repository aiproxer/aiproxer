package service

import (
	"fmt"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Env var constants name the approved host environment namespaces. They are
// host-side configuration only: the connector never reads process
// environment, auth.json or any other connector-side credential source.
const (
	EnvOpenCodeGoAPIKey  = "OPENCODE_GO_API_KEY"
	EnvOpenCodeZenAPIKey = "OPENCODE_ZEN_API_KEY"
	EnvOpenCodeAPIKey    = "OPENCODE_API_KEY"
)

// ResolveAPIKeys resolves ordered credentials for the given OpenCode factory
// kind from host-issued Configure secrets only. The legacy YAML api_key
// (including ${VAR} interpolation), kind-specific and generic environment
// lookup, and auth.json file discovery are removed: a missing or invalid
// secret bundle fails closed naming only the kind-specific environment root.
// Go and Zen scopes stay distinct; each export admits only its own root.
func ResolveAPIKeys(kind string, secrets backendplugin.SecretBundle) ([]string, error) {
	keys, err := openaicompat.DecodeAPIKeySlots(secrets)
	if err != nil {
		return nil, fmt.Errorf("opencode: %w (want %s[_N] via host Configure secrets)", err, envRootForKind(kind))
	}
	return keys, nil
}
