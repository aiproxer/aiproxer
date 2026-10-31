module github.com/aiproxer/aiproxer/connectors/geminicliacp

go 1.26.6

require (
	github.com/aiproxer/aiproxer v0.1.0
	github.com/aiproxer/aiproxer/connector-support/acp v0.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Microsoft/go-winio v0.6.4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/aiproxer/aiproxer => ../..

replace github.com/aiproxer/aiproxer/connector-support/acp => ../../connector-support/acp
