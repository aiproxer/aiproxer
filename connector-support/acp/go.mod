module github.com/aiproxer/aiproxer/connector-support/acp

go 1.26.6

toolchain go1.26.10

require (
	github.com/aiproxer/aiproxer v0.1.0
	go.uber.org/goleak v1.3.0
)

replace github.com/aiproxer/aiproxer => ../..
