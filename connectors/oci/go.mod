module github.com/aiproxer/aiproxer/connectors/oci

go 1.26.6

toolchain go1.26.11

replace github.com/aiproxer/aiproxer => ../..

require (
	github.com/aiproxer/aiproxer v0.0.0-00010101000000-000000000000
	github.com/oracle/oci-go-sdk/v65 v65.129.0
	google.golang.org/grpc v1.85.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/gofrs/flock v0.10.0 // indirect
	github.com/sony/gobreaker/v2 v2.4.0 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	golang.org/x/crypto v0.58.1-0.20261009181718-2f1f834330b2 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
