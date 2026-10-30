package metrics

import "testing"

func TestBucketHost_coarseFamilies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, host, want string
	}{
		{"empty", "", "unknown"},
		{"openai", "api.openai.com", "openai"},
		{"anthropic", "api.anthropic.com", "anthropic"},
		{"google", "generativelanguage.googleapis.com", "google"},
		{"aws", "bedrock-runtime.us-east-1.amazonaws.com", "aws"},
		{"other", "example.com", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bucketHost(tc.host); got != tc.want {
				t.Fatalf("bucketHost(%q)=%q want %q", tc.host, got, tc.want)
			}
		})
	}
}
