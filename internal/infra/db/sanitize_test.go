package db

import (
	"strings"
	"testing"
)

func TestSanitizePostgresDSN_NoQuery(t *testing.T) {
	t.Parallel()
	const in = "postgres://u:p@host:5432/db"
	out, err := SanitizePostgresDSN(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if out != in {
		t.Fatalf("unchanged DSN must round-trip, got: %q", out)
	}
}

func TestSanitizePostgresDSN_StripsChannelBinding(t *testing.T) {
	t.Parallel()
	in := "postgres://u:p@host:5432/db?sslmode=require&channel_binding=require&connect_timeout=10"
	out, err := SanitizePostgresDSN(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "channel_binding") {
		t.Fatalf("channel_binding must be stripped, got: %q", out)
	}
	if !strings.Contains(out, "sslmode=require") {
		t.Fatalf("sslmode must be preserved, got: %q", out)
	}
	if !strings.Contains(out, "connect_timeout=10") {
		t.Fatalf("connect_timeout must be preserved, got: %q", out)
	}
}

func TestSanitizePostgresDSN_CaseInsensitive(t *testing.T) {
	t.Parallel()
	in := "postgres://u:p@host:5432/db?Channel_Binding=require&sslmode=require"
	out, err := SanitizePostgresDSN(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "channel_binding") {
		t.Fatalf("case-variant channel_binding must be stripped, got: %q", out)
	}
	if !strings.Contains(out, "sslmode=require") {
		t.Fatalf("sslmode must be preserved, got: %q", out)
	}
}

func TestSanitizePostgresDSN_NoOpWhenClean(t *testing.T) {
	t.Parallel()
	in := "postgres://u:p@host:5432/db?sslmode=require&connect_timeout=5"
	out, err := SanitizePostgresDSN(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if out != in {
		t.Fatalf("clean DSN must be returned unchanged, got: %q", out)
	}
}

func TestSanitizePostgresDSN_OnlyChannelBindingReducesToBase(t *testing.T) {
	t.Parallel()
	in := "postgres://u:p@host:5432/db?channel_binding=require"
	out, err := SanitizePostgresDSN(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if want := "postgres://u:p@host:5432/db"; out != want {
		t.Fatalf("only-channel_binding DSN must reduce to base without '?', got: %q want: %q", out, want)
	}
}
