package main

import (
	"github.com/aiproxer/aiproxer/connectors/codex/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-codex", service.New())
}
