// Command aip-backend-openaichatgptplan serves the openai-chatgpt-plan
// backend plugin to the AIProxer host over the versioned backend-plugin ABI.
//
// The command also carries the operator surfaces: the "login" subcommand
// performs the official Sign in with ChatGPT authorization and stores the
// registration in the connector's protected local store, the "profiles"
// subcommand lists the saved registrations and records the one the operator
// chose, and the "logout" subcommand ends a renewable session and clears
// the local tokens of a registration.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			// The HTTP client is deliberately NOT supplied here: the
			// authorization service installs a bounded one, and an unbounded
			// client would let a stalled provider hold the exchange open
			// past the attempt's deadline.
			deps := LoginDependencies{BrowserOpener: openBrowser}
			if err := RunLogin(ctx, os.Stdout, os.Stderr, deps, os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "openai-chatgpt-plan login error: %v\n", err)
				os.Exit(1)
			}
			return
		case logoutCommandName:
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			if err := RunLogout(ctx, os.Stdout, os.Stderr, LogoutDependencies{}, os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "openai-chatgpt-plan logout error: %v\n", err)
				os.Exit(1)
			}
			return
		case profilesCommandName:
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
			defer cancel()
			if err := RunProfiles(ctx, os.Stdout, os.Stderr, os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "openai-chatgpt-plan profiles error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	backendplugin.Main("aip-backend-openaichatgptplan", service.New())
}
