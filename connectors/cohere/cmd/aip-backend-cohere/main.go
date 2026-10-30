package main

import (
	"github.com/aiproxer/aiproxer/connectors/cohere/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-cohere", service.NewProduction())
}
