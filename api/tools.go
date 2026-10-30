//go:build ignore

// Code generation tools are pinned in the repository root go.mod via:
//
//	tool google.golang.org/protobuf/cmd/protoc-gen-go
//	tool google.golang.org/grpc/cmd/protoc-gen-go-grpc
//
// Install and generate:
//
//	go install tool
//	cd api && buf generate --template buf.gen.yaml
//
// Verify the contract gate (buf lint, breaking-change detection, generation
// freshness) from the repository root with `make proto-check`.
package ignore
