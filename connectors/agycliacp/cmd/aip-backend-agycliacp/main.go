package main

import (
	"github.com/aiproxer/aiproxer/connectors/agycliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-agycliacp", service.New())
}
