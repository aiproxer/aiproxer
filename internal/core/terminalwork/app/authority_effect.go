package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// AuthorityRequestEffectConfig wires a aipsdk RequestProvider as a terminal-work
// EffectProvider that decodes durable settle/release payloads.
type AuthorityRequestEffectConfig struct {
	ProviderID string
	Provider   authority.RequestProvider
	Version    string
}

// AuthorityRequestEffectProvider recovers settle/release via the live authority
// provider using handles persisted in the durable work payload.
type AuthorityRequestEffectProvider struct {
	id       string
	provider authority.RequestProvider
	version  string
}

// NewAuthorityRequestEffectProvider validates and returns an effect adapter.
func NewAuthorityRequestEffectProvider(cfg AuthorityRequestEffectConfig) (*AuthorityRequestEffectProvider, error) {
	id := strings.TrimSpace(cfg.ProviderID)
	if id == "" {
		return nil, fmt.Errorf("%w: empty provider id", ErrMalformedProvider)
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("%w: nil request provider", ErrNilProvider)
	}
	ver := strings.TrimSpace(cfg.Version)
	if ver == "" {
		ver = "1"
	}
	return &AuthorityRequestEffectProvider{id: id, provider: cfg.Provider, version: ver}, nil
}

func (p *AuthorityRequestEffectProvider) ProviderID() string { return p.id }
func (p *AuthorityRequestEffectProvider) Version() string    { return p.version }
func (p *AuthorityRequestEffectProvider) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{
		sdk.WorkKindSettleRequestProvider,
		sdk.WorkKindReleaseRequestProvider,
	}
}

// Invoke decodes the durable handles payload and calls SettleRequest or ReleaseRequest.
func (p *AuthorityRequestEffectProvider) Invoke(ctx context.Context, rec terminalwork.WorkRecord, idempotencyKey string) error {
	if p == nil || p.provider == nil {
		return ErrNilProvider
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handles, err := DecodeHandlesPayload(rec.Payload)
	if err != nil {
		return err
	}
	requestID := strings.TrimSpace(rec.Lifecycle.RequestID)
	if requestID == "" {
		return fmt.Errorf("%w: missing request_id", ErrInvalidPayload)
	}
	switch rec.Kind {
	case sdk.WorkKindSettleRequestProvider:
		_, err := p.provider.SettleRequest(ctx, authority.RequestSettlement{
			RequestID:      requestID,
			Handles:        handles,
			IdempotencyKey: strings.TrimSpace(idempotencyKey),
		})
		return mapAuthorityInvokeErr(err)
	case sdk.WorkKindReleaseRequestProvider:
		return mapAuthorityInvokeErr(p.provider.ReleaseRequest(ctx, authority.RequestRelease{
			RequestID: requestID,
			Handles:   handles,
			Reason:    "terminal_work_recovery",
		}))
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, rec.Kind)
	}
}

// DecodeHandlesPayload extracts sorted-or-stored handles from durable intent JSON.
func DecodeHandlesPayload(payload []byte) ([]string, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty handles payload", ErrInvalidPayload)
	}
	var body struct {
		Handles []string `json:"handles"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	out := cleanHandles(body.Handles)
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no handles", ErrInvalidPayload)
	}
	return out, nil
}

func mapAuthorityInvokeErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrProviderOutage, err)
}
