package backendpluginv1_test

import (
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/protobuf/proto"
)

func TestGeneratedServiceAndMessagesCompile(t *testing.T) {
	t.Parallel()
	if backendpluginv1.BackendPlugin_Negotiate_FullMethodName == "" {
		t.Fatal("missing negotiate method")
	}
	req := &backendpluginv1.ConfigureRequest{
		InstanceId:  "i",
		FactoryKind: "localstub",
		RuntimePolicy: &backendpluginv1.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         1024,
			MaxStreamFrameBytes:     512,
		},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out backendpluginv1.ConfigureRequest
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !out.GetRuntimePolicy().GetDisableTransportRetries() {
		t.Fatal("expected disable_transport_retries persisted")
	}
}
