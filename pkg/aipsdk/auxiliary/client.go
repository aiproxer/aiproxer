package auxiliary

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// SessionMode selects trusted core execution policy for an auxiliary request.
// It is an SDK-internal request control and is not part of aipapi.Call or any
// frontend/wire contract.
type SessionMode uint8

const (
	// SessionModeDefault preserves ordinary auxiliary execution semantics.
	SessionModeDefault SessionMode = iota
	// SessionModeDetached opts into private child-A-leg execution without
	// primary secure-session turn effects.
	SessionModeDetached
)

// Request describes a child canonical call executed under lineage and role policy (design §7).
type Request struct {
	Role          string
	Visibility    string
	SessionMode   SessionMode
	ParentTraceID string
	ParentALegID  string
	ParentBLegID  string
	// ParentBranchBinding is a trusted, content-free binding captured before
	// child creation; it is never copied into the canonical Call.
	ParentBranchBinding string
	DisablePlugins      []string
	Call                *aipapi.Call
}

// Client is the plugin-facing auxiliary request surface (no executor or backend types).
type Client interface {
	Collect(ctx context.Context, req Request) (aipapi.Collected, error)
	Stream(ctx context.Context, req Request) (aipapi.EventStream, error)
}

var _ Client = DisabledClient{}

// DisabledClient rejects use until core implements auxiliary execution.
type DisabledClient struct{}

func (DisabledClient) Collect(context.Context, Request) (aipapi.Collected, error) {
	return aipapi.Collected{}, ErrNotConfigured
}

func (DisabledClient) Stream(context.Context, Request) (aipapi.EventStream, error) {
	return nil, ErrNotConfigured
}
