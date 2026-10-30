package identity_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"gopkg.in/yaml.v3"
)

func FuzzAcceptClientUserAgent(f *testing.F) {
	f.Add("Cursor/1.2")
	f.Add("")
	f.Add("a\rb")
	f.Add("a\nb")
	f.Add("a\x00b")
	f.Add(strings.Repeat("a", identity.MaxUserAgentBytes))
	f.Add(strings.Repeat("a", identity.MaxUserAgentBytes+1))
	f.Add("  trimmed  ")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > identity.MaxUserAgentBytes+64 {
			return
		}
		v, ok := identity.AcceptClientUserAgent(raw)
		if ok {
			if v == "" {
				t.Fatal("ok with empty value")
			}
			if len(v) > identity.MaxUserAgentBytes {
				t.Fatalf("accepted overlong len=%d", len(v))
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				t.Fatalf("accepted control chars: %q", v)
			}
		} else if v != "" {
			t.Fatalf("!ok but value %q", v)
		}
	})
}

func FuzzAcceptClientAppURL(f *testing.F) {
	f.Add("https://github.com/aiproxer/aiproxer")
	f.Add("http://example.com/path")
	f.Add("")
	f.Add("ftp://example.com")
	f.Add("https://user:pass@example.com/")
	f.Add("https://example.com/x#frag")
	f.Add("not-a-url")
	f.Add("https://example.com/\r\n")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > identity.MaxAppURLBytes+64 {
			return
		}
		v, ok := identity.AcceptClientAppURL(raw)
		if ok {
			if v == "" {
				t.Fatal("ok with empty value")
			}
			if len(v) > identity.MaxAppURLBytes {
				t.Fatalf("accepted overlong len=%d", len(v))
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				t.Fatalf("accepted control chars: %q", v)
			}
		} else if v != "" {
			t.Fatalf("!ok but value %q", v)
		}
	})
}

func FuzzAcceptClientAppTitle(f *testing.F) {
	f.Add("aiproxer")
	f.Add("")
	f.Add("title\n")
	f.Add(strings.Repeat("t", identity.MaxAppTitleBytes))
	f.Add(strings.Repeat("t", identity.MaxAppTitleBytes+1))
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > identity.MaxAppTitleBytes+64 {
			return
		}
		v, ok := identity.AcceptClientAppTitle(raw)
		if ok {
			if v == "" {
				t.Fatal("ok with empty value")
			}
			if len(v) > identity.MaxAppTitleBytes {
				t.Fatalf("accepted overlong len=%d", len(v))
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				t.Fatalf("accepted control chars: %q", v)
			}
		} else if v != "" {
			t.Fatalf("!ok but value %q", v)
		}
	})
}

func FuzzValidateIdentityYAML(f *testing.F) {
	f.Add([]byte(``))
	f.Add([]byte(`upstream: {}`))
	f.Add([]byte(`
upstream:
  user_agent: { mode: proxy }
  openrouter:
    app_url: { mode: proxy }
    app_title: { mode: proxy }
downstream:
  server: { mode: proxy }
`))
	f.Add([]byte(`
upstream:
  user_agent: { mode: custom, value: "Agent/1" }
downstream:
  server: { mode: drop }
`))
	f.Add([]byte(`
downstream:
  server: { mode: passthrough }
`))
	f.Add([]byte(`not: yaml: [`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 8<<10 {
			return
		}
		var cfg identity.Config
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return
		}
		_ = identity.Validate(&cfg)
	})
}
