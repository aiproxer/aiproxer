package main

import (
	"github.com/aiproxer/aiproxer/connectors/azure/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-azure", service.NewProduction())
}
