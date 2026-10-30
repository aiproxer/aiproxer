package diagredact_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/infra/diagredact"
)

func TestSanitize_RedactsRecognizedCredentialsBeforeTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		bad  []string
	}{
		{name: "sk-or", in: "upstream rejected sk-or-v1-abcDEF1234567890xyz", bad: []string{"sk-or-v1-abcDEF1234567890xyz"}},
		{name: "sk-ant", in: "err sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUV", bad: []string{"sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUV"}},
		{name: "sk-live", in: "key sk-live-secret-value-here-123456", bad: []string{"sk-live-secret-value-here-123456"}},
		{name: "ghp", in: "token ghp_abcdefghijklmnopqrstuvwxyz012345", bad: []string{"ghp_abcdefghijklmnopqrstuvwxyz012345"}},
		{name: "github_pat", in: "pat github_pat_11AAAAAAA_abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", bad: []string{"github_pat_11AAAAAAA_abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"}},
		{name: "akia", in: "aws AKIAIOSFODNN7EXAMPLE", bad: []string{"AKIAIOSFODNN7EXAMPLE"}},
		{name: "asia", in: "tmp ASIAY34FZKBOKMUT86SW", bad: []string{"ASIAY34FZKBOKMUT86SW"}},
		{name: "bearer", in: "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc", bad: []string{"Bearer eyJhbGciOiJIUzI1NiJ9", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abc"}},
		{name: "jwt", in: "jwt=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.signaturepart", bad: []string{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.signaturepart"}},
		{name: "pem", in: "-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----", bad: []string{"BEGIN PRIVATE KEY", "MIIE"}},
		{name: "api_key_eq", in: "failed api_key=super-secret-value-99", bad: []string{"super-secret-value-99"}},
		{name: "password_eq", in: "login password: hunter2secret", bad: []string{"hunter2secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := diagredact.Sanitize(tc.in, 256)
			if !strings.Contains(out, diagredact.Marker) {
				t.Fatalf("expected %q in %q", diagredact.Marker, out)
			}
			for _, b := range tc.bad {
				if strings.Contains(out, b) {
					t.Fatalf("leaked %q via %q", b, out)
				}
			}
		})
	}
}

func TestSanitize_LongPaddingPastMaxStillRedacts(t *testing.T) {
	t.Parallel()
	pad := strings.Repeat("x", 300)
	secret := "sk-or-v1-SHOULD-NOT-SURVIVE-TRUNCATE-BOUNDARY"
	in := pad + secret
	out := diagredact.Sanitize(in, 256)
	if len(out) > 256 {
		t.Fatalf("len=%d", len(out))
	}
	if strings.Contains(out, "SHOULD-NOT-SURVIVE") || strings.Contains(out, "sk-or-v1-") || strings.Contains(out, secret) {
		t.Fatalf("secret survived truncate: %q", out)
	}
}

func TestSanitize_PreservesOrdinaryText(t *testing.T) {
	t.Parallel()
	in := "ask-me about secretary skills and desk-top"
	out := diagredact.Sanitize(in, 256)
	if out != in {
		t.Fatalf("corrupted ordinary text: %q -> %q", in, out)
	}
}

func TestSanitize_StripsControlAndLogInjection(t *testing.T) {
	t.Parallel()
	in := "ok\x00\x1b[31mALERT\x07\nnext"
	out := diagredact.Sanitize(in, 256)
	if strings.ContainsRune(out, 0) || strings.Contains(out, "\x1b") || strings.ContainsRune(out, '\x07') {
		t.Fatalf("control chars remain: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("invalid utf8")
	}
}
