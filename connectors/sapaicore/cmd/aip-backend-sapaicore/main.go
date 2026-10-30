package main

import (
	"github.com/aiproxer/aiproxer/connectors/sapaicore/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-sapaicore", service.NewProduction())
}
