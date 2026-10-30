// Package adapter is the anti-corruption layer between public backendplugin
// DTOs and core-consumed execbackend.Backend / aipapi streams.
//
// It must not import provider SDKs. internal/core must not import this package's
// processhost/grpc dependencies transitively through core packages.
package adapter
