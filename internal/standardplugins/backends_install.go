package standardplugins

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type openAIStyleYAML struct {
	BaseURL string `yaml:"base_url"`
	// SDKMaxRetries optionally overrides the SDK-internal MaxRetries.
	SDKMaxRetries *int               `yaml:"sdk_max_retries"`
	Models        modelInventoryYAML `yaml:"models"`
}

// firstResolvedAPIKey returns the first host-resolved environment key or empty
// for no-auth. Keys arrive bound at registration from the authorized host
// snapshot; plugin YAML never carries credential values (requirement 2.1).
func firstResolvedAPIKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// sdkMaxRetriesOrDefault returns the operator-configured SDK MaxRetries, or the
// standard-distribution default of 0: retry policy above the HTTP round trip lives
// in the credential-rotation loops and core failover, not in SDK-internal retries.
func sdkMaxRetriesOrDefault(v *int) *int {
	if v != nil {
		return v
	}
	return new(int)
}

func resolveUpstreamHTTP(upstream *http.Client) *http.Client {
	if upstream != nil {
		return upstream
	}
	return httpclient.Standard()
}

func applyConfiguredModelInventory(be execbackend.Backend, y modelInventoryYAML) (execbackend.Backend, error) {
	provider, ok, err := configuredModelInventory(y)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if ok {
		be.ModelInventory = provider
	}
	return be, nil
}

func configuredModelInventory(y modelInventoryYAML) (modelinventory.Provider, bool, error) {
	rows, source, ok, err := modelInventoryRows(y, false)
	if err != nil || !ok {
		return nil, false, err
	}
	return staticModelInventory(source, rows)
}
