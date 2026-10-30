package main

import (
	"github.com/aiproxer/aiproxer/connectors/sagemaker/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-sagemaker", service.NewProduction())
}
