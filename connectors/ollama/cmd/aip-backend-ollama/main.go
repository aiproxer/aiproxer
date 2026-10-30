package main

import (
	"github.com/aiproxer/aiproxer/connectors/ollama/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-ollama", service.New())
}
