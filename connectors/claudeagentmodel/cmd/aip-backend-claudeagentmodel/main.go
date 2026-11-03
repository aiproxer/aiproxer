package main

import (
	"github.com/aiproxer/aiproxer/connectors/claudeagentmodel/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-claudeagentmodel", service.New())
}
