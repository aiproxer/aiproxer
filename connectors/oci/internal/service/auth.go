package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/oracle/oci-go-sdk/v65/common"
)

// RequestSigner defines the consumer-driven interface for signing outgoing OCI HTTP requests.
type RequestSigner interface {
	Sign(r *http.Request) error
}

// RequestSignerFactory constructs an OCI RequestSigner from config and secrets.
type RequestSignerFactory func(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RequestSigner, error)

// DefaultRequestSignerFactory builds an OCI request signer.
// If explicit static credentials (private_key PEM) are provided via secrets, it requires
// tenancy_ocid, user_ocid, and fingerprint (from config or secrets) and uses NewRawConfigurationProvider.
// Otherwise, it falls back to the official OCI DefaultConfigProvider (file / instance principal).
func DefaultRequestSignerFactory(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle) (RequestSigner, error) {
	pkBytes := secrets.Values["private_key"]
	if len(pkBytes) == 0 {
		pkBytes = secrets.Values["private_key_pem"]
	}

	if len(pkBytes) > 0 {
		privateKey := string(pkBytes)

		var passphrasePtr *string
		if passBytes, ok := secrets.Values["passphrase"]; ok && len(passBytes) > 0 {
			pass := string(passBytes)
			passphrasePtr = &pass
		} else if passBytes, ok := secrets.Values["private_key_passphrase"]; ok && len(passBytes) > 0 {
			pass := string(passBytes)
			passphrasePtr = &pass
		}

		tenancy := strings.TrimSpace(cfg.TenancyOCID)
		if tenancy == "" {
			tenancy = strings.TrimSpace(string(secrets.Values["tenancy_ocid"]))
		}
		if tenancy == "" {
			return nil, fmt.Errorf("oci-generative-ai: tenancy_ocid is required when private_key is supplied")
		}

		user := strings.TrimSpace(cfg.UserOCID)
		if user == "" {
			user = strings.TrimSpace(string(secrets.Values["user_ocid"]))
		}
		if user == "" {
			return nil, fmt.Errorf("oci-generative-ai: user_ocid is required when private_key is supplied")
		}

		fingerprint := strings.TrimSpace(cfg.Fingerprint)
		if fingerprint == "" {
			fingerprint = strings.TrimSpace(string(secrets.Values["fingerprint"]))
		}
		if fingerprint == "" {
			return nil, fmt.Errorf("oci-generative-ai: fingerprint is required when private_key is supplied")
		}

		provider := common.NewRawConfigurationProvider(tenancy, user, cfg.Region, fingerprint, privateKey, passphrasePtr)
		return common.DefaultRequestSigner(provider), nil
	}

	provider := common.DefaultConfigProvider()
	return common.DefaultRequestSigner(provider), nil
}
