package auxreq

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// MaxDetachedAuxiliaryRoleBytes bounds trusted role metadata before it enters
// a detached execution context. Roles are classification tokens, not content.
const MaxDetachedAuxiliaryRoleBytes = 128

var ErrInvalidDetachedRole = errors.New("auxreq: invalid detached auxiliary role")

// withDetachedPolicy installs the trusted core-only policy for auxiliary
// execution. The canonical child Call remains free of execution-control
// fields; only the runtime context carries this authority.
func withDetachedPolicy(ctx context.Context, req auxiliary.Request) (context.Context, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	if req.Call == nil {
		return ctx, nil
	}
	role, err := normalizeDetachedRole(req.Role)
	if err != nil {
		return ctx, err
	}
	meta, marked := execctx.DetachedSessionFromContext(ctx)
	if !marked {
		meta = execctx.DetachedSession{}
	}
	parentALegID := strings.TrimSpace(req.ParentALegID)
	if strings.TrimSpace(meta.ParentALegID) != "" {
		parentALegID = strings.TrimSpace(meta.ParentALegID)
	}
	if parentALegID == "" {
		parentALegID = strings.TrimSpace(req.Call.Session.ALegID)
	}
	parentSessionID := strings.TrimSpace(req.Call.Session.AuthoritativeSessionID)
	if strings.TrimSpace(meta.ParentSessionID) != "" {
		parentSessionID = strings.TrimSpace(meta.ParentSessionID)
	}
	// Feature code normally supplies explicit parent lineage. For internal
	// callbacks that only carry a selector, retain already-bound parent
	// session/A-leg views as lineage.
	if views, ok := execctx.FromContext(ctx); ok {
		if parentSessionID == "" {
			parentSessionID = strings.TrimSpace(views.Session.AuthoritativeSessionID)
		}
		if parentALegID == "" {
			parentALegID = strings.TrimSpace(views.Session.ALegID)
		}
	}
	parentTraceID := strings.TrimSpace(req.ParentTraceID)
	if strings.TrimSpace(meta.ParentTraceID) != "" {
		parentTraceID = strings.TrimSpace(meta.ParentTraceID)
	}
	if parentTraceID == "" {
		parentTraceID = strings.TrimSpace(diag.TraceID(ctx))
	}
	if parentALegID == "" {
		parentALegID = strings.TrimSpace(diag.ALegID(ctx))
	}
	meta.ParentSessionID = parentSessionID
	meta.ParentALegID = parentALegID
	meta.ParentTraceID = parentTraceID
	meta.AuxiliaryRole = role
	// A captured opaque branch binding already present on the trusted parent
	// context is authoritative. It must be supplied explicitly by the trusted
	// feature request; canonical Call session hints are never branch authority.
	if strings.TrimSpace(meta.ParentBranchBinding) == "" {
		meta.ParentBranchBinding = strings.TrimSpace(req.ParentBranchBinding)
	}
	return execctx.WithDetachedSession(ctx, meta), nil
}

func normalizeDetachedRole(role string) (string, error) {
	trimmed := strings.TrimSpace(role)
	if role != "" && trimmed == "" {
		return "", fmt.Errorf("%w: role is empty after trimming", ErrInvalidDetachedRole)
	}
	if len(trimmed) > MaxDetachedAuxiliaryRoleBytes {
		return "", fmt.Errorf("%w: role exceeds %d bytes", ErrInvalidDetachedRole, MaxDetachedAuxiliaryRoleBytes)
	}
	for _, r := range trimmed {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return "", fmt.Errorf("%w: role contains invalid token character", ErrInvalidDetachedRole)
		}
	}
	return trimmed, nil
}
