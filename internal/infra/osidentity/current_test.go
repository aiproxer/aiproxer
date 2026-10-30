package osidentity

import (
	"context"
	"errors"
	"os/user"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
)

func TestProvider_Current_osUserWhenLookupSucceeds(t *testing.T) {
	t.Parallel()
	u, err := user.Current()
	if err != nil || u == nil {
		t.Skip("no os user in this environment")
	}
	p := &Provider{}
	snap, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if snap.FallbackUsed {
		t.Fatal("expected OS user path without fallback")
	}
	if snap.PrincipalID == "" {
		t.Fatal("empty principal from OS user")
	}
}

func TestProvider_Current_AIPEnvWhenLookupFails(t *testing.T) {
	t.Setenv("AIP_OS_PRINCIPAL_ID", "from_aip_env")
	t.Setenv("AIP_OS_DISPLAY_NAME", "AIProxer Display")
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")

	p := &Provider{
		LookupCurrentUser: func() (*user.User, error) {
			return nil, errors.New("forced failure for test")
		},
	}
	snap, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if snap.FallbackUsed {
		t.Fatal("AIP_OS_PRINCIPAL_ID is an explicit hint, not unknown fallback")
	}
	if snap.PrincipalID != "from_aip_env" {
		t.Fatalf("PrincipalID: got %q", snap.PrincipalID)
	}
	if snap.DisplayName != "AIProxer Display" {
		t.Fatalf("DisplayName: got %q", snap.DisplayName)
	}
}

func TestProvider_Current_USERWhenLookupFails(t *testing.T) {
	t.Setenv("AIP_OS_PRINCIPAL_ID", "")
	t.Setenv("AIP_OS_DISPLAY_NAME", "")
	t.Setenv("USER", "env_unix_user")
	t.Setenv("USERNAME", "")

	p := &Provider{
		LookupCurrentUser: func() (*user.User, error) {
			return nil, errors.New("forced failure for test")
		},
	}
	snap, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !snap.FallbackUsed {
		t.Fatal("USER without os/user is non-authoritative; want FallbackUsed")
	}
	if snap.PrincipalID != "env_unix_user" {
		t.Fatalf("PrincipalID: got %q", snap.PrincipalID)
	}
}

func TestProvider_Current_USERNAMEWhenLookupFails(t *testing.T) {
	t.Setenv("AIP_OS_PRINCIPAL_ID", "")
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "env_win_user")

	p := &Provider{
		LookupCurrentUser: func() (*user.User, error) {
			return nil, errors.New("forced failure for test")
		},
	}
	snap, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !snap.FallbackUsed {
		t.Fatal("USERNAME without os/user is non-authoritative; want FallbackUsed")
	}
	if snap.PrincipalID != "env_win_user" {
		t.Fatalf("PrincipalID: got %q", snap.PrincipalID)
	}
}

func TestProvider_Current_deterministicFallback(t *testing.T) {
	t.Setenv("AIP_OS_PRINCIPAL_ID", "")
	t.Setenv("AIP_OS_DISPLAY_NAME", "")
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")

	p := &Provider{
		LookupCurrentUser: func() (*user.User, error) {
			return nil, errors.New("forced failure for test")
		},
	}
	snap, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if !snap.FallbackUsed {
		t.Fatal("expected FallbackUsed when no OS user and no env")
	}
	if snap.PrincipalID != coreauth.LocalUnknownOSPrincipalID {
		t.Fatalf("PrincipalID: got %q want %q", snap.PrincipalID, coreauth.LocalUnknownOSPrincipalID)
	}
}
