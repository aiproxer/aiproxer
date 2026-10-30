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

// LeaseSetEffectConfig wires concurrency release for terminal lease-set work.
type LeaseSetEffectConfig struct {
	ProviderID string
	Provider   authority.ConcurrencyProvider
	Version    string
}

// LeaseSetEffectProvider completes WorkKindReleaseLeaseSet via ReleaseLease(SetID).
type LeaseSetEffectProvider struct {
	id       string
	provider authority.ConcurrencyProvider
	version  string
}

// NewLeaseSetEffectProvider validates and returns a lease-set effect adapter.
func NewLeaseSetEffectProvider(cfg LeaseSetEffectConfig) (*LeaseSetEffectProvider, error) {
	id := strings.TrimSpace(cfg.ProviderID)
	if id == "" {
		id = "concurrency"
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("%w: nil concurrency provider", ErrNilProvider)
	}
	ver := strings.TrimSpace(cfg.Version)
	if ver == "" {
		ver = "1"
	}
	return &LeaseSetEffectProvider{id: id, provider: cfg.Provider, version: ver}, nil
}

func (p *LeaseSetEffectProvider) ProviderID() string { return p.id }
func (p *LeaseSetEffectProvider) Version() string    { return p.version }
func (p *LeaseSetEffectProvider) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindReleaseLeaseSet}
}

// Invoke releases the lease set; capacity is not freed until the store confirms.
func (p *LeaseSetEffectProvider) Invoke(ctx context.Context, rec terminalwork.WorkRecord, _ string) error {
	if p == nil || p.provider == nil {
		return ErrNilProvider
	}
	if ctx == nil {
		ctx = context.Background()
	}
	setID, reason, err := DecodeLeaseSetPayload(rec)
	if err != nil {
		return err
	}
	requestID := strings.TrimSpace(rec.Lifecycle.RequestID)
	if requestID == "" {
		return fmt.Errorf("%w: missing request_id", ErrInvalidPayload)
	}
	if rec.Kind != sdk.WorkKindReleaseLeaseSet {
		return fmt.Errorf("%w: %s", ErrUnsupportedKind, rec.Kind)
	}
	return mapAuthorityInvokeErr(p.provider.ReleaseLease(ctx, authority.LeaseRelease{
		SetID:     setID,
		RequestID: requestID,
		Reason:    reason,
	}))
}

// DecodeLeaseSetPayload extracts set identity from durable work.
func DecodeLeaseSetPayload(rec terminalwork.WorkRecord) (setID, reason string, err error) {
	setID = strings.TrimSpace(rec.LeaseSetID)
	if setID == "" && len(rec.Payload) > 0 {
		var body struct {
			SetID  string `json:"set_id"`
			Reason string `json:"reason"`
		}
		if uerr := json.Unmarshal(rec.Payload, &body); uerr != nil {
			return "", "", fmt.Errorf("%w: %v", ErrInvalidPayload, uerr)
		}
		setID = strings.TrimSpace(body.SetID)
		reason = strings.TrimSpace(body.Reason)
	} else if len(rec.Payload) > 0 {
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(rec.Payload, &body)
		reason = strings.TrimSpace(body.Reason)
	}
	if setID == "" {
		return "", "", fmt.Errorf("%w: missing set_id", ErrInvalidPayload)
	}
	if reason == "" {
		reason = "terminal_work_lease_set_release"
	}
	return setID, reason, nil
}
