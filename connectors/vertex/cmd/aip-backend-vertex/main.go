package main

import (
	"github.com/aiproxer/aiproxer/connectors/vertex/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-vertex", service.NewProduction())
}
