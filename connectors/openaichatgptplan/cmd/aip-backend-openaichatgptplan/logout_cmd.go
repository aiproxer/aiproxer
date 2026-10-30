package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The operator sign-out command.
//
// The command ends the renewable session of a registration and clears its
// local tokens. It prints the OUTCOME, because the two outcomes are
// different facts: a revocation the provider confirmed, and a sign-out that
// completed locally while the remote revocation was not confirmed.
//
// The command never prints a token, a code or a subject: it names the
// registration through its local, non-secret key.

const (
	// logoutCommandName is the name of the sign-out subcommand.
	logoutCommandName = "logout"

	// profilesCommandName is the name of the registration listing
	// subcommand.
	profilesCommandName = "profiles"
)

// LogoutDependencies are the collaborators of the sign-out command.
//
// The HTTP client and the OIDC client are replaceable so that a test can
// drive the command against a fake provider.
type LogoutDependencies struct {
	// HTTPClient performs the revocation request. Empty means the
	// default client.
	HTTPClient *http.Client
	// OIDCClient publishes the provider's OIDC configuration, which names
	// the revocation endpoint. Empty means the connector's own client.
	OIDCClient *siwc.OIDCClient
	// Sleep waits between two revocation attempts. Empty means the
	// connector's own wait.
	Sleep func(context.Context, time.Duration) error
}

// RunLogout executes the operator sign-out command.
//
// The command signs out the named registration, or the selected one when no
// registration is named. It prints the outcome of the revocation attempt
// and the guidance the outcome implies.
func RunLogout(ctx context.Context, stdout, stderr io.Writer, deps LogoutDependencies, args []string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flags := flag.NewFlagSet(logoutCommandName, flag.ContinueOnError)
	flags.SetOutput(stderr)

	profileIDFlag := flags.String("profile-id", "", "Local id of the registration to sign out (default: the selected one)")
	profileStoreFlag := flags.String("profile-store", "", "Path of the profile store (default: the per-installation default)")
	discoveryEndpointFlag := flags.String(
		"discovery-endpoint",
		"",
		"Override the OIDC discovery endpoint: this REPLACES the trust root, so the revocation endpoint is the one that endpoint publishes",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	profileStorePath, err := siwc.ResolveProfileStorePath(strings.TrimSpace(*profileStoreFlag))
	if err != nil {
		return err
	}
	store, err := siwc.NewProfileStore(profileStorePath)
	if err != nil {
		return err
	}
	oidc := deps.OIDCClient
	if oidc == nil {
		built, err := siwc.NewOIDCClient(siwc.OIDCClientConfig{
			DiscoveryURL: strings.TrimSpace(*discoveryEndpointFlag),
		})
		if err != nil {
			return err
		}
		oidc = built
	}
	revoker, err := siwc.NewRevoker(siwc.RevokerConfig{
		Profiles:   store,
		OIDC:       oidc,
		HTTPClient: deps.HTTPClient,
		Sleep:      deps.Sleep,
	})
	if err != nil {
		return err
	}

	profileID := strings.TrimSpace(*profileIDFlag)
	if profileID == "" {
		selected, err := store.Selected()
		if err != nil {
			return err
		}
		if selected == "" {
			return fmt.Errorf(
				"siwc: no registration is selected; name the registration to sign out with -%s",
				"profile-id",
			)
		}
		profileID = selected
	}

	result, err := revoker.Logout(ctx, profileID)
	if err != nil {
		return err
	}
	switch {
	case result.RevocationOutcome == siwc.RevocationOutcomeSuperseded:
		_, _ = fmt.Fprintf(stdout, "Registration %s: a newer session was retained; it was not signed out. %s\n", result.ProfileID, result.Guidance)
	case result.RemoteRevocationConfirmed:
		_, _ = fmt.Fprintf(stdout, "Signed out. The remote session of registration %s was revoked.\n", result.ProfileID)
	case result.RevocationOutcome == siwc.RevocationOutcomeNoSession:
		_, _ = fmt.Fprintf(stdout, "Signed out. Registration %s had no renewable session to revoke.\n", result.ProfileID)
	default:
		// The sign-out completed locally and the remote revocation was
		// NOT confirmed. The operator has to end the session in ChatGPT
		// Settings.
		_, _ = fmt.Fprintf(
			stdout,
			"Signed out locally. Registration %s: remote revocation was NOT confirmed: %s\n",
			result.ProfileID,
			result.Guidance,
		)
	}
	return nil
}

// RunProfiles executes the operator registration listing command.
//
// The command lists the saved registrations and, with -select, records the
// registration the operator chose. The listing names every registration
// through its local, non-secret key and its label: no token, no subject and
// no email hint.
func RunProfiles(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flags := flag.NewFlagSet(profilesCommandName, flag.ContinueOnError)
	flags.SetOutput(stderr)

	profileStoreFlag := flags.String("profile-store", "", "Path of the profile store (default: the per-installation default)")
	selectFlag := flags.String("select", "", "Record the registration the operator chose as the selected one")
	if err := flags.Parse(args); err != nil {
		return err
	}

	profileStorePath, err := siwc.ResolveProfileStorePath(strings.TrimSpace(*profileStoreFlag))
	if err != nil {
		return err
	}
	store, err := siwc.NewProfileStore(profileStorePath)
	if err != nil {
		return err
	}
	selected, err := store.Selected()
	if err != nil {
		return err
	}
	if wanted := strings.TrimSpace(*selectFlag); wanted != "" {
		if err := store.Select(wanted); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "Registration %s is now the active one.\n", wanted)
		return nil
	}

	summaries, err := store.List()
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		_, _ = fmt.Fprintln(stdout, "No registration is saved.")
		return nil
	}
	for _, summary := range summaries {
		state := "ready"
		if summary.NeedsReauth {
			state = "needs a new authorization"
		}
		marker := " "
		if summary.ID == selected {
			marker = "*"
		}
		_, _ = fmt.Fprintf(
			stdout,
			"%s %s\texpires %s\t%s\n",
			marker,
			siwc.DescribeRegistration(siwc.ProfileRecord{ID: summary.ID, Label: summary.Label}),
			summary.Expiry.UTC().Format(time.RFC3339),
			state,
		)
	}
	return nil
}
