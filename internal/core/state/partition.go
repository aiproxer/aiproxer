package state

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

func partitionForScope(ctx context.Context, scope aipstate.Scope) (string, error) {
	switch scope {
	case aipstate.ScopeGlobal:
		return "", nil
	case aipstate.ScopeRequest, aipstate.ScopeSession, aipstate.ScopePrincipal:
		v, ok := execctx.FromContext(ctx)
		if !ok {
			return "", aipstate.ErrMissingExecutionContext
		}
		switch scope {
		case aipstate.ScopeRequest:
			t := strings.TrimSpace(v.Attempt.TraceID)
			if t == "" {
				return "", aipstate.ErrMissingExecutionContext
			}
			return t, nil
		case aipstate.ScopeSession:
			s := strings.TrimSpace(v.Session.PartitionKey())
			if s == "" {
				return "", aipstate.ErrMissingExecutionContext
			}
			return s, nil
		case aipstate.ScopePrincipal:
			p := strings.TrimSpace(v.Principal.ID)
			if p == "" {
				return "", aipstate.ErrMissingPrincipal
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("core/state: unknown scope %q", scope)
}
