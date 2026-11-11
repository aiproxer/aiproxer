module github.com/aiproxer/aiproxer/connectors/azure

go 1.26.6

toolchain go1.26.10

replace github.com/aiproxer/aiproxer => ../..

replace github.com/aiproxer/aiproxer/connector-support/openaicompat => ../../connector-support/openaicompat

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.24.0
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0
	github.com/aiproxer/aiproxer v0.0.0
	github.com/aiproxer/aiproxer/connector-support/openaicompat v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.85.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.10.1 // indirect
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
