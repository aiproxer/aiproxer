package main

import (
	"github.com/aiproxer/aiproxer/connectors/openrouter/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-openrouter", service.New())
}
