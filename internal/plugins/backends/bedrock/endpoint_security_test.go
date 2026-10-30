package bedrock

import "testing"

func TestValidateBedrockEndpointInput_rejectsDisableHTTPSWithoutBaseEndpoint(t *testing.T) {
	t.Parallel()
	err := validateBedrockEndpointInput(Config{
		DisableHTTPS: true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestValidateBedrockEndpointInput_allowsDisableHTTPSWithBaseEndpoint(t *testing.T) {
	t.Parallel()
	err := validateBedrockEndpointInput(Config{
		DisableHTTPS: true,
		BaseEndpoint: "http://127.0.0.1:9",
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The adapter accepts non-loopback plaintext endpoints: the loopback-only security
// policy for the standard distribution is enforced at registration by
// internal/standardplugins, not by the adapter, so embedders can apply their own policy.
func TestValidateBedrockEndpointInput_nonLoopbackIsPolicyNotInputValidation(t *testing.T) {
	t.Parallel()
	err := validateBedrockEndpointInput(Config{
		DisableHTTPS: true,
		BaseEndpoint: "http://example.com",
	})
	if err != nil {
		t.Fatalf("non-loopback endpoint must pass adapter input validation: %v", err)
	}
}

func TestValidateBedrockEndpointInput_httpsAlwaysAllowed(t *testing.T) {
	t.Parallel()
	if err := validateBedrockEndpointInput(Config{}); err != nil {
		t.Fatal(err)
	}
}
