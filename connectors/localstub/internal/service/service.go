package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

var errPostTextStream = errors.New("local-stub: simulated stream failure after text delta")

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: 0,
		PluginID:      "com.aiproxer.backend.localstub",
		Version:       "0.1.0",
		BuildID:       "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              "Local Stub",
			Description:              "Deterministic no-key backend for dogfood and packaging proofs",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeAny,
			RoutePrefixes:            []string{FactoryKind},
			SupportsCountTokens:      true,
			SupportsFinalizeBilling:  true,
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation:        true,
				BidirectionalStream: true,
			},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("local-stub: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, id: req.InstanceID}, nil
}

type instance struct {
	cfg Config
	id  string
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	caps := backendplugin.CapabilitySummary{Streaming: true}
	if i.cfg.ToolName != "" {
		caps.Tools = true
	}
	return backendplugin.ResolvedProfile{
		Capabilities:             caps,
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsCountTokens:      true,
		SupportsFinalizeBilling:  true,
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           "local-stub",
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "local-stub/stub-default",
			NativeModelID:    "stub-default",
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		}},
		InventorySource: "local-stub",
		FetchedUnixMS:   1,
	}, nil
}

func (i *instance) CountTokens(context.Context, backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	in := int64(i.cfg.InputTokens)
	return backendplugin.CountTokensResponse{
		InputTokens:     &in,
		Presence:        backendplugin.UsagePresence{InputTokens: true},
		EvidenceQuality: "local-stub",
	}, nil
}

func (i *instance) FinalizeBilling(context.Context, backendplugin.FinalizeBillingRequest) (backendplugin.FinalizeBillingResponse, error) {
	in := int64(i.cfg.InputTokens)
	ot := int64(i.cfg.OutputTokens)
	total := in + ot
	return backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			InputTokens: &in, OutputTokens: &ot, TotalTokens: &total,
			Presence:     backendplugin.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			RawUsageJSON: backendplugin.RawJSONAbsentValue(),
		},
		EvidenceQuality: "local-stub",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart {
		return fmt.Errorf("%w: expected start", backendplugin.ErrInvalidFrame)
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	seq := uint64(1)
	for _, ev := range canonicalEvents(i.cfg) {
		if err := stream.Send(backendplugin.ServerFrame{
			Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: ev,
		}); err != nil {
			return err
		}
		seq++
		if i.cfg.StreamErrorAfterTextDelta && ev.Kind == backendplugin.EventTextDelta {
			return errPostTextStream
		}
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}

func canonicalEvents(cfg Config) []*backendplugin.CanonicalEvent {
	text := cfg.Text
	out := []*backendplugin.CanonicalEvent{
		{Kind: backendplugin.EventResponseStarted},
		{Kind: backendplugin.EventMessageStarted},
		{Kind: backendplugin.EventTextDelta, Delta: &text},
	}
	if cfg.ToolName != "" {
		id := stubToolCallID
		name := cfg.ToolName
		args := cfg.ToolArgs
		if args == "" {
			args = DefaultToolArgs
		}
		out = append(
			out,
			&backendplugin.CanonicalEvent{Kind: backendplugin.EventToolCallStarted, ToolCallID: &id, ToolName: &name},
			&backendplugin.CanonicalEvent{Kind: backendplugin.EventToolCallArgsDelta, ToolCallID: &id, Delta: &args},
			&backendplugin.CanonicalEvent{Kind: backendplugin.EventToolCallFinished, ToolCallID: &id},
		)
	}
	in := int64(cfg.InputTokens)
	ot := int64(cfg.OutputTokens)
	total := in + ot
	out = append(
		out,
		&backendplugin.CanonicalEvent{
			Kind: backendplugin.EventUsageDelta,
			Usage: &backendplugin.UsageEvidence{
				InputTokens: &in, OutputTokens: &ot, TotalTokens: &total,
				Presence:     backendplugin.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
				RawUsageJSON: backendplugin.RawJSONAbsentValue(),
			},
		},
		&backendplugin.CanonicalEvent{Kind: backendplugin.EventResponseFinished},
	)
	return out
}

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
	_ backendplugin.TokenCounter       = (*instance)(nil)
	_ backendplugin.BillingFinalizer   = (*instance)(nil)
)
