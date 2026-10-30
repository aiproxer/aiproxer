package main

import (
	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-commandcode-anthropic", service.New())
}
