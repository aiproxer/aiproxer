package openresponsescompat

import "testing"

func TestSanitizeErrorMessage_NeverEchoesProviderText(t *testing.T) {
	cases := []string{
		"https://provider.example/v1?api_key=sk-secret",
		`{"error":{"token":"bearer secret"}}`,
		"internal failure at C:\\service\\stack.go:42",
		"ordinary provider diagnostic",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if got := sanitizeErrorMessage(input); got != "upstream reported an error" {
				t.Fatalf("sanitized message = %q, want generic provider error", got)
			}
		})
	}
}
