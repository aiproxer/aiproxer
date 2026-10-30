package main

import (
	"github.com/aiproxer/aiproxer/connectors/commandcode-openai/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-commandcode-openai", service.New())
}
