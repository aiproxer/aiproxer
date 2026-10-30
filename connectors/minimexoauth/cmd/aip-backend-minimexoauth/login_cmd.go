package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"gopkg.in/yaml.v3"
)

type loginConfigYAML struct {
	OAuthClientID  string `yaml:"oauth_client_id"`
	OAuthTokenFile string `yaml:"oauth_token_file"`
	PortalBaseURL  string `yaml:"portal_base_url"`
	OAuthScope     string `yaml:"oauth_scope"`
}

// RunLogin executes the operator login flow for MiniMax OAuth.
func RunLogin(ctx context.Context, stdout, stderr io.Writer, hc *http.Client, args []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if hc == nil {
		hc = http.DefaultClient
	}

	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)

	clientIDFlag := fs.String("client-id", "", "MiniMax OAuth client ID (or env MINIMAX_OAUTH_CLIENT_ID)")
	tokenFileFlag := fs.String("token-file", "", "Path to save minted OAuth token file (or env MINIMAX_OAUTH_TOKEN_FILE)")
	portalURLFlag := fs.String("portal-url", "", "MiniMax portal base URL (default: "+service.DefaultPortalBaseURL+")")
	scopeFlag := fs.String("scope", "", "MiniMax OAuth scope (default: "+service.DefaultScope+")")
	configPathFlag := fs.String("config", "", "Path to YAML configuration file containing oauth_client_id, oauth_token_file, etc.")

	if err := fs.Parse(args); err != nil {
		return err
	}

	var cfg loginConfigYAML
	if *configPathFlag != "" {
		data, err := os.ReadFile(*configPathFlag)
		if err != nil {
			return fmt.Errorf("minimax-oauth login: read config file %q: %w", *configPathFlag, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("minimax-oauth login: parse config file %q: %w", *configPathFlag, err)
		}
	}

	clientID := strings.TrimSpace(*clientIDFlag)
	if clientID == "" {
		clientID = strings.TrimSpace(cfg.OAuthClientID)
	}
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("MINIMAX_OAUTH_CLIENT_ID"))
	}
	if clientID == "" {
		return errors.New("minimax-oauth login: oauth_client_id is required (use --client-id, MINIMAX_OAUTH_CLIENT_ID, or --config)")
	}

	tokenFile := strings.TrimSpace(*tokenFileFlag)
	if tokenFile == "" {
		tokenFile = strings.TrimSpace(cfg.OAuthTokenFile)
	}
	if tokenFile == "" {
		tokenFile = strings.TrimSpace(os.Getenv("MINIMAX_OAUTH_TOKEN_FILE"))
	}
	if tokenFile == "" {
		return errors.New("minimax-oauth login: oauth_token_file is required (use --token-file, MINIMAX_OAUTH_TOKEN_FILE, or --config)")
	}

	portalURL := strings.TrimSpace(*portalURLFlag)
	if portalURL == "" {
		portalURL = strings.TrimSpace(cfg.PortalBaseURL)
	}
	if portalURL == "" {
		portalURL = strings.TrimSpace(os.Getenv("MINIMAX_PORTAL_BASE_URL"))
	}
	if portalURL == "" {
		portalURL = service.DefaultPortalBaseURL
	}

	scope := strings.TrimSpace(*scopeFlag)
	if scope == "" {
		scope = strings.TrimSpace(cfg.OAuthScope)
	}
	if scope == "" {
		scope = strings.TrimSpace(os.Getenv("MINIMAX_OAUTH_SCOPE"))
	}
	if scope == "" {
		scope = service.DefaultScope
	}

	sess, err := service.StartLogin(ctx, hc, portalURL, clientID, scope)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "%s\n", sess.Instructions())
	_, _ = fmt.Fprintf(stdout, "Waiting for browser approval...\n")

	store := oauthcred.NewFileStore(tokenFile)
	if _, err := sess.Complete(ctx, store); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(stdout, "Successfully authenticated! Token saved to %s\n", tokenFile)
	return nil
}
