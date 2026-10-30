package codex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	defaultGPT55SourceModel = "gpt-5.5"
	defaultGPT55TargetModel = "gpt-5.4"
)

// downgradePolicy is local openaicodex policy until a backend retry-hook seam exists.
type downgradePolicy struct {
	disabled bool
	source   string
	target   string
}

func newDowngradePolicy(cfg Config) downgradePolicy {
	p := downgradePolicy{
		disabled: cfg.GPT55DowngradeDisabled,
		source:   strings.TrimSpace(cfg.GPT55DowngradeSourceModel),
		target:   strings.TrimSpace(cfg.GPT55DowngradeTargetModel),
	}
	if p.source == "" {
		p.source = defaultGPT55SourceModel
	}
	if p.target == "" {
		p.target = defaultGPT55TargetModel
	}
	return p
}

func (p downgradePolicy) modelForPlan(requested, planType string) string {
	if p.disabled || requested != p.source || !isFreePlanType(planType) {
		return requested
	}
	return p.target
}

func (p downgradePolicy) isReactiveFreePlanRejectionMessage(message string) bool {
	if p.disabled {
		return false
	}
	lower := strings.ToLower(message)
	if !strings.Contains(lower, strings.ToLower(p.source)) || !strings.Contains(lower, "free") {
		return false
	}
	return strings.Contains(lower, "unsupported") || strings.Contains(lower, "not available")
}

func (p downgradePolicy) shouldReactiveRetry(originalModel string, alreadyRetried bool, rejectionMessage string) bool {
	if alreadyRetried || originalModel != p.source || p.disabled {
		return false
	}
	return p.isReactiveFreePlanRejectionMessage(rejectionMessage)
}

func (p downgradePolicy) isFreePlanRejection(status int, body string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	return p.isReactiveFreePlanRejectionMessage(body)
}

func (p downgradePolicy) retryBody(originalModel string, alreadyRetried bool, status int, body string, payload *Payload) ([]byte, bool, error) {
	if status != http.StatusBadRequest || !p.shouldReactiveRetry(originalModel, alreadyRetried, body) {
		return nil, false, nil
	}
	payload.Model = p.target
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("%s: marshal downgrade payload: %w", ID, err)
	}
	return out, true, nil
}

func isFreePlanType(planType string) bool {
	return strings.EqualFold(strings.TrimSpace(planType), "free")
}
