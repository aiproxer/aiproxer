package main

import (
	"github.com/aiproxer/aiproxer/connectors/cursorcliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-cursorcliacp", service.New())
}
