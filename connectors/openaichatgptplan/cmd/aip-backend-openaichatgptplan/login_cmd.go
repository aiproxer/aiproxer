package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The operator login command.
//
// The browser and the loopback callback are an operator surface, not a core
// HTTP API: the connector process starts the callback listener, the
// operator's browser completes the authorization, and the connector exchanges
// the code and stores the registration.
//
// The command drives exactly the same authorization service the tests
// exercise, so the flow that the tests prove is the flow an operator runs.

// LoginDependencies are the collaborators of the login command.
//
// The HTTP client and the browser opener are replaceable so that a test can
// drive the command against a fake authorization server. The validator is
// replaceable for the same reason: the ID-token validation is the OIDC
// phase of the migration, and a test provides a stand-in for it.
type LoginDependencies struct {
	// HTTPClient performs the code exchange. Empty means the default
	// client.
	HTTPClient *http.Client
	// BrowserOpener opens the authorization URL. Empty means the
	// platform's default opener.
	BrowserOpener siwc.BrowserOpener
	// Validator validates the ID token of the token response. Empty means
	// the connector's own validator: the ID token is verified against the
	// provider's published key set, and a token that does not verify is
	// refused.
	Validator siwc.IDTokenValidator
}

// RunLogin executes the operator login command.
//
// The command registers a new ChatGPT account, or reauthorizes a saved one,
// against the documented SIWC endpoints. It prints the outcome of the
// attempt; it never prints a token, a code or an authorization URL that
// carries a retained ID-token hint.
func RunLogin(ctx context.Context, stdout, stderr io.Writer, deps LoginDependencies, args []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(stderr)

	profileIDFlag := flags.String("profile-id", "", "Local id of the registration (default: a generated id)")
	labelFlag := flags.String("label", "", "Operator-facing label of the registration")
	hostRecordFlag := flags.String("host-record", "", "Path of the host identity record (default: the per-installation default)")
	profileStoreFlag := flags.String("profile-store", "", "Path of the profile store (default: the per-installation default)")
	reauthorizeFlag := flags.Bool("reauthorize", false, "Reauthorize the selected registration instead of registering a new one")
	// The endpoint overrides exist for a private deployment and for the
	// tests. They are a deliberate REDIRECTION of the sign-in: an endpoint
	// that is not OpenAI's receives the authorization code, the PKCE
	// verifier and the token set. The default is the documented endpoint,
	// and an operator that changes it changes where the credentials go.
	authorizationEndpointFlag := flags.String(
		"authorization-endpoint",
		"",
		"Override the authorization endpoint (a non-default endpoint receives the sign-in)",
	)
	tokenEndpointFlag := flags.String(
		"token-endpoint",
		"",
		"Override the token endpoint (a non-default endpoint receives the code and the PKCE verifier)",
	)
	discoveryEndpointFlag := flags.String(
		"discovery-endpoint",
		"",
		"Override the OIDC discovery endpoint: this REPLACES the trust root, so the expected issuer and the trusted signing keys become the ones that endpoint publishes",
	)
	timeoutFlag := flags.Duration("timeout", siwc.DefaultAttemptTTL, "How long the authorization attempt waits for the browser")

	if err := flags.Parse(args); err != nil {
		return err
	}

	// The discovery override REPLACES the trust root: the expected issuer
	// and the trusted signing keys become the ones that endpoint
	// publishes. The notice says so, and it does NOT repeat the URL: a
	// URL may carry credentials in its userinfo or its query, and those
	// must never reach a diagnostic.
	//
	// The override is an OPERATOR affordance. It is a local choice about
	// this installation's own credential store, and it is not a remote
	// input: nothing in the provider's answers selects it.
	if strings.TrimSpace(*discoveryEndpointFlag) != "" {
		_, _ = fmt.Fprintln(
			stderr,
			"the OIDC discovery endpoint was overridden: it REPLACES the trust root, so the expected issuer "+
				"and the trusted signing keys are the ones that endpoint publishes",
		)
	}

	hostRecordPath, err := siwc.ResolveHostRecordPath(strings.TrimSpace(*hostRecordFlag))
	if err != nil {
		return err
	}
	profileStorePath, err := siwc.ResolveProfileStorePath(strings.TrimSpace(*profileStoreFlag))
	if err != nil {
		return err
	}
	hosts, err := siwc.NewHostStore(hostRecordPath)
	if err != nil {
		return err
	}
	profiles, err := siwc.NewProfileStore(profileStorePath)
	if err != nil {
		return err
	}
	validator := deps.Validator
	if validator == nil {
		// The validator is the connector's own: it verifies the ID token
		// against the key set the provider's OIDC configuration publishes,
		// and it refuses a token that does not verify. A command that
		// cannot build one fails closed rather than signing in without a
		// validated identity.
		built, err := siwc.NewOIDCValidator(siwc.OIDCValidatorConfig{
			OIDCClientConfig: siwc.OIDCClientConfig{
				DiscoveryURL: strings.TrimSpace(*discoveryEndpointFlag),
			},
		})
		if err != nil {
			return err
		}
		validator = built
	}
	opener := deps.BrowserOpener
	if opener == nil {
		opener = openBrowser
	}
	authorizer, err := siwc.NewAuthorizer(siwc.AuthorizerConfig{
		AuthorizationEndpoint: strings.TrimSpace(*authorizationEndpointFlag),
		TokenEndpoint:         strings.TrimSpace(*tokenEndpointFlag),
		HTTPClient:            deps.HTTPClient,
		BrowserOpener:         opener,
		AttemptTTL:            *timeoutFlag,
		Profiles:              profiles,
		Hosts:                 hosts,
		Validator:             validator,
		Notice: func(message string) {
			_, _ = fmt.Fprintln(stderr, message)
		},
	})
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(stdout, "Waiting for the browser to complete the Sign in with ChatGPT authorization...")
	var record siwc.ProfileRecord
	if *reauthorizeFlag {
		record, err = authorizer.Reauthorize(ctx, siwc.ReauthorizeRequest{
			ProfileID: strings.TrimSpace(*profileIDFlag),
			Label:     strings.TrimSpace(*labelFlag),
		})
	} else {
		record, err = authorizer.Register(ctx, siwc.RegisterRequest{
			ProfileID: strings.TrimSpace(*profileIDFlag),
			Label:     strings.TrimSpace(*labelFlag),
		})
	}
	if err != nil {
		return err
	}
	// The summary names the registration through its local, non-secret
	// key. No token, no code and no hint is printed.
	if record.AllowsPlanUsage() {
		_, _ = fmt.Fprintf(stdout, "Signed in. Registration %s is ready.\n", record.ID)
		return nil
	}
	// The sign-in is retained, and the plan usage is not authorized. The
	// guidance is the flow's notice: the flow owns the policy, and the
	// command repeats the policy only as a fact about this registration.
	_, _ = fmt.Fprintf(
		stdout,
		"Signed in. Registration %s is saved, and its ChatGPT plan usage is disabled.\n",
		record.ID,
	)
	return nil
}

// openBrowser opens a URL in the operator's browser.
//
// The launcher is DETACHED: this command never becomes the parent of the
// process it starts. The launcher is started by a shell that backgrounds it
// and exits immediately, so the launcher is orphaned and the OPERATING
// SYSTEM reaps it when it exits.
//
// The alternative was worse on both sides of the contract:
//
//   - a launcher that is this command's own child stays a ZOMBIE child of a
//     command that is still running the authorization, until the command
//     exits; and
//   - reaping it here would mean waiting for it, and a launcher that has
//     become the user's browser session must NOT be waited for: the
//     browser legitimately outlives the login.
//
// The shell is this command's child, and the command reaps it. That wait is
// bounded by construction: the shell starts a background job and returns.
//
// The browser the launcher opens is a DIFFERENT process: it may outlive the
// login, and this command never signals it.
//
// The ownership is therefore explicit and bounded: the command starts the
// launcher at most once, keeps no handle to it, and leaves no worker
// behind.
//
// RESIDUAL (platform-inherent, bounded): the authorization URL is the
// launcher's ARGUMENT, and an argument list is world-readable on a
// multi-user host - it is visible in /proc/<pid>/cmdline and in the output
// of ps. A returning authorization's URL carries the retained identity
// hints, so a local principal could read them from the process table.
//
// The exposure cannot be engineered away while the protocol is honoured:
// the platform's URL handlers take the URL as an argument and offer no
// argument-free channel. The connector's OWN surfaces - the standard
// output, the standard error, the notices and the errors - never carry the
// hint-bearing URL, and the manual URL the operator is given is the
// HINT-FREE one. Switching the launcher to the hint-free URL as well would
// cost the provider's account-selector screen, which is an owner decision
// and not a repair this command may take on its own.
func openBrowser(ctx context.Context, target string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	if err := launchDetached(target); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	return nil
}

// launcherCommand returns the platform's handler for a URL.
func launcherCommand(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}
