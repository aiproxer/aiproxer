package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	ErrCountingDisabled    = errors.New("token counting disabled")
	ErrProviderUnsupported = errors.New("provider token counting unsupported")
	ErrProviderUnavailable = errors.New("provider token counting unavailable")
	ErrLocalUnavailable    = errors.New("local token counting unavailable")
)

type Mode string

const (
	ModeProviderFirst Mode = "provider_first"
	ModeLocalFirst    Mode = "local_first"
	ModeProviderOnly  Mode = "provider_only"
	ModeLocalOnly     Mode = "local_only"
	ModeDisabled      Mode = "disabled"
)

type ServiceConfig struct {
	Mode Mode
}

type Service struct {
	mode     Mode
	provider ProviderCounter
	local    LocalCounter
}

type ProviderCounter interface {
	SupportsCount(context.Context, ProviderCountInput) ProviderSupport
	CountText(context.Context, CountTextInput) (CountResult, error)
	CountCall(context.Context, CountCallInput) (CountResult, error)
	CountOutput(context.Context, CountOutputInput) (CountResult, error)
}

type LocalCounter interface {
	CountText(context.Context, CountTextInput) (CountResult, error)
	CountCall(context.Context, CountCallInput) (CountResult, error)
	CountOutput(context.Context, CountOutputInput) (CountResult, error)
}

type ProviderCountInput struct {
	Backend string
	Model   string
	Kind    CountKind
}

type SupportStatus string

const (
	SupportStatusSupported   SupportStatus = "supported"
	SupportStatusUnsupported SupportStatus = "unsupported"
	SupportStatusUnavailable SupportStatus = "unavailable"
)

type ProviderSupport struct {
	Status  SupportStatus
	Message string
	Err     error
}

type CountKind string

const (
	CountKindText   CountKind = "text"
	CountKindCall   CountKind = "call"
	CountKindOutput CountKind = "output"
)

type CountTextInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
}

type CountCallInput struct {
	Backend string
	Model   string
	CallID  string
	Call    aipapi.Call
}

type CountOutputInput struct {
	Backend string
	Model   string
	CallID  string
	Text    string
	Events  []aipapi.Event
}

type CountResult struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	// TotalTokensPresent distinguishes an authoritative total (including
	// explicit zero) from an omitted total that may be inferred later.
	TotalTokensPresent bool
	Accounting         aipapi.UsageAccountingMetadata
	Fallbacks          []Fallback
}

type FallbackReason string

const (
	FallbackReasonProviderUnsupported  FallbackReason = "provider_unsupported"
	FallbackReasonProviderUnavailable  FallbackReason = "provider_unavailable"
	FallbackReasonProviderError        FallbackReason = "provider_error"
	FallbackReasonLocalError           FallbackReason = "local_error"
	FallbackReasonLocalDefaultEncoding FallbackReason = "local_default_encoding"
)

type Fallback struct {
	Reason  FallbackReason
	Message string
	Err     error
}

func NewService(cfg ServiceConfig, provider ProviderCounter, local LocalCounter) *Service {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeProviderFirst
	}
	return &Service{mode: mode, provider: provider, local: local}
}

func (s *Service) CountText(ctx context.Context, input CountTextInput) (CountResult, error) {
	return s.count(ctx, ProviderCountInput{Backend: input.Backend, Model: input.Model, Kind: CountKindText}, input.Model,
		func(ctx context.Context) (CountResult, error) {
			return s.provider.CountText(ctx, input)
		},
		func(ctx context.Context) (CountResult, error) {
			return s.local.CountText(ctx, input)
		})
}

func (s *Service) CountCall(ctx context.Context, input CountCallInput) (CountResult, error) {
	return s.count(ctx, ProviderCountInput{Backend: input.Backend, Model: input.Model, Kind: CountKindCall}, input.Model,
		func(ctx context.Context) (CountResult, error) {
			return s.provider.CountCall(ctx, input)
		},
		func(ctx context.Context) (CountResult, error) {
			return s.local.CountCall(ctx, input)
		})
}

func (s *Service) CountOutput(ctx context.Context, input CountOutputInput) (CountResult, error) {
	return s.count(ctx, ProviderCountInput{Backend: input.Backend, Model: input.Model, Kind: CountKindOutput}, input.Model,
		func(ctx context.Context) (CountResult, error) {
			return s.provider.CountOutput(ctx, input)
		},
		func(ctx context.Context) (CountResult, error) {
			return s.local.CountOutput(ctx, input)
		})
}

func (s *Service) count(
	ctx context.Context,
	providerInput ProviderCountInput,
	model string,
	providerCount func(context.Context) (CountResult, error),
	localCount func(context.Context) (CountResult, error),
) (CountResult, error) {
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}

	switch s.mode {
	case ModeDisabled:
		return CountResult{}, ErrCountingDisabled
	case ModeLocalOnly:
		return s.countLocal(ctx, model, localCount)
	case ModeProviderOnly:
		return s.countProviderOnly(ctx, providerInput, providerCount)
	case ModeLocalFirst:
		return s.countLocalFirst(ctx, providerInput, model, providerCount, localCount)
	case ModeProviderFirst:
		return s.countProviderFirst(ctx, providerInput, model, providerCount, localCount)
	default:
		return CountResult{}, fmt.Errorf("unknown token counting mode %q", s.mode)
	}
}

func (s *Service) countProviderOnly(
	ctx context.Context,
	input ProviderCountInput,
	providerCount func(context.Context) (CountResult, error),
) (CountResult, error) {
	support := s.providerSupport(ctx, input)
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	switch support.Status {
	case SupportStatusSupported:
	case SupportStatusUnavailable:
		return CountResult{}, supportError(support, ErrProviderUnavailable)
	default:
		return CountResult{}, supportError(support, ErrProviderUnsupported)
	}
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	result, err := providerCount(ctx)
	if err != nil {
		return CountResult{}, fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	return result, nil
}

func (s *Service) countProviderFirst(
	ctx context.Context,
	input ProviderCountInput,
	model string,
	providerCount func(context.Context) (CountResult, error),
	localCount func(context.Context) (CountResult, error),
) (CountResult, error) {
	support := s.providerSupport(ctx, input)
	if support.Status == SupportStatusSupported {
		if err := ctx.Err(); err != nil {
			return CountResult{}, err
		}
		result, err := providerCount(ctx)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return CountResult{}, err
			}
			return result, nil
		}
		return s.countLocalWithFallback(ctx, model, localCount, Fallback{
			Reason: FallbackReasonProviderError,
			Err:    fmt.Errorf("%w: %w", ErrProviderUnavailable, err),
		})
	}
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	fallback := providerFallback(support)
	return s.countLocalWithFallback(ctx, model, localCount, fallback)
}

func (s *Service) countLocalFirst(
	ctx context.Context,
	input ProviderCountInput,
	model string,
	providerCount func(context.Context) (CountResult, error),
	localCount func(context.Context) (CountResult, error),
) (CountResult, error) {
	result, err := s.countLocal(ctx, model, localCount)
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return CountResult{}, ctxErr
	}
	support := s.providerSupport(ctx, input)
	if support.Status != SupportStatusSupported {
		return CountResult{}, err
	}
	result, providerErr := s.countProviderOnly(ctx, input, providerCount)
	if providerErr != nil {
		return CountResult{}, providerErr
	}
	result.Fallbacks = append(result.Fallbacks, Fallback{Reason: FallbackReasonLocalError, Err: err})
	return result, nil
}

func (s *Service) countLocal(ctx context.Context, model string, localCount func(context.Context) (CountResult, error)) (CountResult, error) {
	if s.local == nil {
		return CountResult{}, ErrLocalUnavailable
	}
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	result, err := localCount(ctx)
	if err != nil {
		return CountResult{}, fmt.Errorf("%w: %w", ErrLocalUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return CountResult{}, err
	}
	fillLocalDefaults(&result, model)
	return result, nil
}

func (s *Service) countLocalWithFallback(
	ctx context.Context,
	model string,
	localCount func(context.Context) (CountResult, error),
	fallback Fallback,
) (CountResult, error) {
	result, err := s.countLocal(ctx, model, localCount)
	if err != nil {
		return CountResult{}, err
	}
	result.Fallbacks = append(result.Fallbacks, fallback)
	return result, nil
}

func (s *Service) providerSupport(ctx context.Context, input ProviderCountInput) ProviderSupport {
	if s.provider == nil {
		return ProviderSupport{Status: SupportStatusUnsupported}
	}
	support := s.provider.SupportsCount(ctx, input)
	if support.Status == "" {
		support.Status = SupportStatusUnsupported
	}
	return support
}

func providerFallback(support ProviderSupport) Fallback {
	if support.Status == SupportStatusUnavailable {
		return Fallback{
			Reason:  FallbackReasonProviderUnavailable,
			Message: support.Message,
			Err:     supportError(support, ErrProviderUnavailable),
		}
	}
	return Fallback{
		Reason:  FallbackReasonProviderUnsupported,
		Message: support.Message,
		Err:     supportError(support, ErrProviderUnsupported),
	}
}

func supportError(support ProviderSupport, sentinel error) error {
	if support.Err == nil {
		return sentinel
	}
	return fmt.Errorf("%w: %w", sentinel, support.Err)
}

func fillLocalDefaults(result *CountResult, model string) {
	if result.Accounting.Source == aipapi.UsageSourceUnknown {
		result.Accounting.Source = aipapi.UsageSourceLocalTokenizer
	}
	if result.Accounting.Authority == aipapi.UsageAuthorityUnknown {
		result.Accounting.Authority = aipapi.UsageAuthorityEstimated
	}
	if result.Accounting.Tokenizer.ModelUsed == "" {
		result.Accounting.Tokenizer.ModelUsed = model
	}
}
