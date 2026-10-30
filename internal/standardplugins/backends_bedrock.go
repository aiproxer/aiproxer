package standardplugins

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"gopkg.in/yaml.v3"
)

type bedrockBackendYAML struct {
	Region string `yaml:"region"`
	// AccessKeyID/SecretAccessKey/SessionToken are the explicitly classified
	// cloud-workload identity mode for this backend (requirement 2.4): AWS
	// SigV4 workload signing, not an interchangeable static bearer-key pool.
	// They are intentionally outside the central static API-key guard, which
	// covers provider bearer/token/PAT fields; literal provider secrets in
	// AIProxer YAML stay prohibited on every other built-in path.
	AccessKeyID              string             `yaml:"access_key_id"`
	SecretAccessKey          string             `yaml:"secret_access_key"`
	SessionToken             string             `yaml:"session_token"`
	BaseEndpoint             string             `yaml:"base_endpoint"`
	DisableHTTPS             bool               `yaml:"disable_https"`
	AllowInsecureNonLoopback bool               `yaml:"allow_insecure_non_loopback"`
	Models                   modelInventoryYAML `yaml:"models"`
}

func backendBedrock(n yaml.Node, upstream *http.Client, idCfg identity.Config) (execbackend.Backend, error) {
	var y bedrockBackendYAML
	if err := config.DecodeYAMLNode(n, &y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("bedrock backend config: %w", err)
	}
	if err := validateBedrockEndpointPolicy(y); err != nil {
		return execbackend.Backend{}, fmt.Errorf("bedrock backend config: %w", err)
	}
	httpClient, err := resolveIdentityHTTP(upstream, idCfg, n, "bedrock backend config")
	if err != nil {
		return execbackend.Backend{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
	defer cancel()
	return applyConfiguredModelInventory(bedrock.NewWithContext(ctx, bedrock.Config{
		Region:          y.Region,
		AccessKeyID:     y.AccessKeyID,
		SecretAccessKey: y.SecretAccessKey,
		SessionToken:    y.SessionToken,
		BaseEndpoint:    y.BaseEndpoint,
		DisableHTTPS:    y.DisableHTTPS,
		HTTPClient:      httpClient,
	}), y.Models)
}

// validateBedrockEndpointPolicy is the standard-distribution endpoint security policy
// for the bedrock connector, enforced at registration: plaintext (disable_https)
// endpoints are only acceptable against loopback hosts, unless the operator explicitly
// opts into non-loopback plaintext for lab use. The bedrock adapter itself only
// performs plain input validation; this policy decision lives at registration.
func validateBedrockEndpointPolicy(y bedrockBackendYAML) error {
	if !y.DisableHTTPS {
		return nil
	}
	base := strings.TrimSpace(y.BaseEndpoint)
	if base == "" {
		return fmt.Errorf("bedrock: disable_https requires a non-empty base_endpoint")
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("bedrock: disable_https: parse base_endpoint: %w", err)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("bedrock: disable_https: base_endpoint must include a host")
	}
	host := u.Hostname()
	if isLoopbackHost(host) {
		return nil
	}
	if y.AllowInsecureNonLoopback {
		return nil
	}
	return fmt.Errorf("bedrock: disable_https is only allowed for loopback base_endpoint (got host %q); set allow_insecure_non_loopback for lab use", host)
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	h = strings.TrimSuffix(h, ".")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
