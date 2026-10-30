// Package osidentity resolves the current OS user (or explicit env hints) for local no-op auth.
//
// Environment (optional, non-secret hints for operators and tests):
//   - AIP_OS_PRINCIPAL_ID — stable principal id when set (non-empty).
//   - AIP_OS_DISPLAY_NAME — optional display name when AIP_OS_PRINCIPAL_ID is used.
//
// Resolution order: os/user.Current(); then AIP_OS_PRINCIPAL_ID; then USER; then USERNAME;
// then a deterministic non-empty fallback with FallbackUsed=true.
package osidentity

import (
	"context"
	"os"
	"os/user"
	"strings"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
)

// Provider resolves the current process identity. For tests, set LookupCurrentUser to force
// lookup failures or synthetic users.
type Provider struct {
	LookupCurrentUser func() (*user.User, error)
}

// Current implements [coreauth.OSIdentityProvider].
func (p *Provider) Current(ctx context.Context) (coreauth.OSIdentitySnapshot, error) {
	_ = ctx
	lookup := user.Current
	if p != nil && p.LookupCurrentUser != nil {
		lookup = p.LookupCurrentUser
	}
	if u, err := lookup(); err == nil && u != nil {
		id := strings.TrimSpace(u.Username)
		if id == "" {
			id = strings.TrimSpace(u.Uid)
		}
		name := strings.TrimSpace(u.Name)
		if id != "" {
			return coreauth.OSIdentitySnapshot{
				PrincipalID:  id,
				DisplayName:  name,
				FallbackUsed: false,
			}, nil
		}
	}

	if v := strings.TrimSpace(os.Getenv("AIP_OS_PRINCIPAL_ID")); v != "" {
		dn := strings.TrimSpace(os.Getenv("AIP_OS_DISPLAY_NAME"))
		return coreauth.OSIdentitySnapshot{
			PrincipalID:  v,
			DisplayName:  dn,
			FallbackUsed: false,
		}, nil
	}
	if v := strings.TrimSpace(os.Getenv("USER")); v != "" {
		// Best-effort shell/env identity (not the OS user record from os/user).
		return coreauth.OSIdentitySnapshot{PrincipalID: v, FallbackUsed: true}, nil
	}
	if v := strings.TrimSpace(os.Getenv("USERNAME")); v != "" {
		return coreauth.OSIdentitySnapshot{PrincipalID: v, FallbackUsed: true}, nil
	}

	return coreauth.OSIdentitySnapshot{
		PrincipalID:  coreauth.LocalUnknownOSPrincipalID,
		DisplayName:  "",
		FallbackUsed: true,
	}, nil
}

var _ coreauth.OSIdentityProvider = (*Provider)(nil)
