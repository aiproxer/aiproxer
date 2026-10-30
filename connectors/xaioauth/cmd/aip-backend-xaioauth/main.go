package main

import (
	"github.com/aiproxer/aiproxer/connectors/xaioauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-xaioauth", service.NewProduction())
}
