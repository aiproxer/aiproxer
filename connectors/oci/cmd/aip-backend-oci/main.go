package main

import (
	"github.com/aiproxer/aiproxer/connectors/oci/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-oci", service.NewProduction())
}
