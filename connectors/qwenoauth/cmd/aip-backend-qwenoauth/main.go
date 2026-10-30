package main

import (
	"github.com/aiproxer/aiproxer/connectors/qwenoauth/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-qwenoauth", service.NewProduction())
}
