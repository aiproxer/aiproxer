package compatible

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	ProviderID         = "compatible-admission"
	namespace          = "compatible-backend"
	defaultLeaseTTL    = time.Minute
	defaultRenewBefore = 15 * time.Second
)

// Limits maps runtime backend instance IDs to positive max concurrent requests.
type Limits map[string]int

// Runtime holds generation-local compatible admission state.
type Runtime struct {
	limits  Limits
	service *concurrencyapp.Service
}

// NewRuntime constructs an immutable compatible admission runtime for one generation.
// store is the generation-local lease persistence port (wired from infra at composition).
func NewRuntime(limits Limits, store concurrencyapp.LeaseStore) (*Runtime, error) {
	rules, err := domainRules(limits)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	if store == nil {
		return nil, fmt.Errorf("compatible admission: lease store is required")
	}
	src := &staticRuleSource{rules: rules}
	return &Runtime{
		limits:  cloneLimits(limits),
		service: concurrencyapp.NewService(src, store, nil),
	}, nil
}

func domainRules(limits Limits) ([]domain.Rule, error) {
	if len(limits) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(limits))
	for id, limit := range limits {
		id = strings.TrimSpace(id)
		if id == "" || limit <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]domain.Rule, 0, len(ids))
	for _, id := range ids {
		limit := limits[id]
		if limit <= 0 {
			return nil, fmt.Errorf("compatible admission: backend %q max_concurrent_requests must be positive", id)
		}
		out = append(out, domain.Rule{
			ID:              "compatible-" + id,
			Namespace:       namespace,
			Version:         "v1",
			Mode:            domain.RuleModeStrict,
			Limit:           limit,
			LeaseTTL:        defaultLeaseTTL,
			RenewBefore:     defaultRenewBefore,
			FailureBehavior: domain.FailureBehaviorFailClosed,
			Match: domain.DimensionsMatcher{
				Labels: map[string]domain.DimensionMatcher{
					"compatible_backend": {Value: scope.Known(id)},
				},
			},
		})
	}
	return out, nil
}

type staticRuleSource struct {
	rules []domain.Rule
}

func (s *staticRuleSource) Snapshot(_ context.Context) (concurrencyapp.RuleSnapshot, error) {
	ready := domain.Readiness{State: domain.ReadinessStateReady}
	return concurrencyapp.RuleSnapshot{
		ID:        "compatible-admission",
		Version:   "v1",
		State:     concurrencyapp.SnapshotStateFromReadiness(ready),
		Readiness: ready,
		Rules:     cloneDomainRules(s.rules),
	}, nil
}

func cloneDomainRules(in []domain.Rule) []domain.Rule {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Rule, len(in))
	copy(out, in)
	return out
}

func cloneLimits(in Limits) Limits {
	if len(in) == 0 {
		return nil
	}
	out := make(Limits, len(in))
	maps.Copy(out, in)
	return out
}

func (r *Runtime) limitFor(backendID string) (int, bool) {
	if r == nil || len(r.limits) == 0 {
		return 0, false
	}
	limit, ok := r.limits[strings.TrimSpace(backendID)]
	if !ok || limit <= 0 {
		return 0, false
	}
	return limit, true
}
