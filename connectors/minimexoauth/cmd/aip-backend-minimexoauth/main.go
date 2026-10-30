package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/aiproxer/aiproxer/connectors/minimexoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "login" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		if err := RunLogin(ctx, os.Stdout, os.Stderr, http.DefaultClient, os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "minimax-oauth login error: %v\n", err)
			os.Exit(1)
		}
		return
	}
	backendplugin.Main("aip-backend-minimexoauth", service.NewProduction())
}
