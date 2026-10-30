package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var (
		modeFlag      string
		componentFlag string
		formatFlag    string
		jsonFlag      bool
		testFlags     string
	)

	fs := flag.NewFlagSet("dbparity", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&modeFlag, "mode", "", "Runner mode (list, sqlite, postgres-direct, all)")
	fs.StringVar(&componentFlag, "component", "", "Filter to a specific component ID")
	fs.StringVar(&componentFlag, "only", "", "Alias for -component")
	fs.StringVar(&formatFlag, "format", "text", "List output format: text or json (list mode only)")
	fs.StringVar(&formatFlag, "list-format", "text", "Alias for -format")
	fs.BoolVar(&jsonFlag, "json", false, "Output list mode in JSON format")
	fs.StringVar(&testFlags, "flags", "", "Extra flags passed through to go test (overrides GO_TEST_FLAGS env if set)")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: dbparity [flags] [mode]\n\n")
		_, _ = fmt.Fprintf(stderr, "Modes:\n")
		_, _ = fmt.Fprintf(stderr, "  list             Show catalog component and test package inventory\n")
		_, _ = fmt.Fprintf(stderr, "  sqlite           Execute canonical SQLite parity tests\n")
		_, _ = fmt.Fprintf(stderr, "  postgres-direct  Execute canonical PostgreSQL direct parity tests (fail-closed)\n")
		_, _ = fmt.Fprintf(stderr, "  all              Execute SQLite and PostgreSQL direct parity tests concurrently (default)\n\n")
		_, _ = fmt.Fprintf(stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	reordered, err := dbparity.ReorderCLIArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %s\n", dbparity.RedactDSN(err.Error()))
		fs.Usage()
		return 2
	}

	if err := fs.Parse(reordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "Error: %s\n", dbparity.RedactDSN(err.Error()))
		fs.Usage()
		return 2
	}

	var positionalMode string
	if fs.NArg() > 0 {
		positionalMode = strings.TrimSpace(fs.Arg(0))
		if fs.NArg() > 1 {
			msg := fmt.Sprintf("Error: unexpected extra positional argument %q\n", fs.Arg(1))
			_, _ = fmt.Fprint(stderr, dbparity.RedactDSN(msg))
			fs.Usage()
			return 2
		}
	}

	modeStr := strings.TrimSpace(modeFlag)
	if modeStr != "" && positionalMode != "" {
		msg := fmt.Sprintf("Error: cannot specify both -mode flag (%q) and positional mode (%q)\n", modeStr, positionalMode)
		_, _ = fmt.Fprint(stderr, dbparity.RedactDSN(msg))
		fs.Usage()
		return 2
	}
	if modeStr == "" {
		modeStr = positionalMode
	}
	if modeStr == "" {
		modeStr = string(dbparity.ModeAll)
	}

	mode, err := dbparity.ParseRunnerMode(modeStr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %s\n", dbparity.RedactDSN(err.Error()))
		fs.Usage()
		return 2
	}

	// Component filter resolution
	component := strings.TrimSpace(componentFlag)

	// Test flags resolution
	var goTestFlags []string
	if strings.TrimSpace(testFlags) != "" {
		parsedFlags, parseErr := dbparity.ParseFlagWords(testFlags)
		if parseErr != nil {
			_, _ = fmt.Fprintf(stderr, "Error: invalid -flags argument: %s\n", dbparity.RedactDSN(parseErr.Error()))
			return 2
		}
		goTestFlags = parsedFlags
	} else if envFlags := strings.TrimSpace(os.Getenv("GO_TEST_FLAGS")); envFlags != "" {
		parsedFlags, parseErr := dbparity.ParseFlagWords(envFlags)
		if parseErr != nil {
			_, _ = fmt.Fprintf(stderr, "Error: invalid GO_TEST_FLAGS environment variable: %s\n", dbparity.RedactDSN(parseErr.Error()))
			return 2
		}
		goTestFlags = parsedFlags
	}

	cat := dbparity.DefaultCatalog()

	// Handle list mode with optional JSON format
	if mode == dbparity.ModeList {
		if jsonFlag || strings.EqualFold(formatFlag, "json") {
			jsonOut, jsonErr := dbparity.FormatListJSON(cat)
			if jsonErr != nil {
				_, _ = fmt.Fprintf(stderr, "Error formatting JSON: %s\n", dbparity.RedactDSN(jsonErr.Error()))
				return 1
			}
			_, _ = fmt.Fprintln(stdout, jsonOut)
			return 0
		}
		_, _ = fmt.Fprintln(stdout, dbparity.FormatList(cat))
		return 0
	}

	opts := dbparity.PlanOptions{
		Catalog:     cat,
		GoTestFlags: goTestFlags,
		ComponentID: component,
		BaseEnv:     os.Environ(),
	}

	if runErr := dbparity.Run(ctx, mode, opts, stdout, stderr); runErr != nil {
		if stepErr, ok := errors.AsType[*dbparity.RunStepError](runErr); ok {
			exitCode := dbparity.MapExitStatus(stepErr)
			_, _ = fmt.Fprintf(stderr, "\ndbparity: test failed for component %q package %q (backend: %s, exit code: %d)\n",
				stepErr.Component, stepErr.Package, stepErr.Backend, exitCode)
			return exitCode
		}

		exitCode := dbparity.MapExitStatus(runErr)
		_, _ = fmt.Fprintf(stderr, "\nError: %s\n", dbparity.RedactDSN(runErr.Error()))
		return exitCode
	}

	return 0
}
