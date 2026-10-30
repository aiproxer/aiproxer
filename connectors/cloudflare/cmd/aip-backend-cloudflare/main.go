package main

import (
	"github.com/aiproxer/aiproxer/connectors/cloudflare/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-cloudflare", service.New())
}
