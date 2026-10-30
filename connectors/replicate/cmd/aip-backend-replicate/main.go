package main

import (
	"github.com/aiproxer/aiproxer/connectors/replicate/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-replicate", service.NewProduction())
}
