package main

import (
	"github.com/aiproxer/aiproxer/connectors/watsonx/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-watsonx", service.NewProduction())
}
