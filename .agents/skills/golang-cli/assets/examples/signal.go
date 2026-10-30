package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// Construct the command with its server dependency; the process owns signals.
func newServeWithSignalCmd(srv *http.Server) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serveUntilCanceled(ctx, srv)
		},
	}
}

func serveUntilCanceled(ctx context.Context, srv *http.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.ListenAndServe() }()

	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Shutdown closes listeners before returning; ListenAndServe completing does
	// not prove that in-flight handlers have drained. Wait for Shutdown itself.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, srv.Close())
	}
	serveErr := <-serveDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr)
}
