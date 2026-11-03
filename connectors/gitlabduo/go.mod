module github.com/aiproxer/aiproxer/connectors/gitlabduo

go 1.26.6

toolchain go1.26.10

replace (
	github.com/aiproxer/aiproxer => ../..
	github.com/aiproxer/aiproxer/connector-support/oauthcred => ../../connector-support/oauthcred
)

require (
	github.com/aiproxer/aiproxer v0.0.0
	github.com/aiproxer/aiproxer/connector-support/oauthcred v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.85.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Microsoft/go-winio v0.6.4 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
