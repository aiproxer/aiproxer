package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

var version = "dev"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		if _, err := fmt.Fprintf(stdout, "aipstd %s\n", version); err != nil {
			_, _ = fmt.Fprintf(stderr, "aipstd: write version: %v\n", err)
			return 1
		}
		return 0
	}
	parsed, err := ParseArgsFull(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "aipstd: %v\n", err)
		return 2
	}

	return RunCommand(ctx, CommandOptions{
		Name:           parsed.Name,
		ConfigPath:     parsed.ConfigPath,
		StreamRecovery: parsed.StreamRecovery,
		MultiUser:      parsed.MultiUser,
		Components:     parsed.Components,
		InstanceID:     parsed.InstanceID,
		Output:         stdout,
		ErrorOut:       stderr,
	})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), ShutdownSignals()...)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
