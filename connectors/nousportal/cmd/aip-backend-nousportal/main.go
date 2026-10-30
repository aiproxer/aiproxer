package main

import (
	"github.com/aiproxer/aiproxer/connectors/nousportal/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-nousportal", service.NewProduction())
}
