package main

import (
	"github.com/aiproxer/aiproxer/connectors/localstub/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-localstub", service.New())
}
