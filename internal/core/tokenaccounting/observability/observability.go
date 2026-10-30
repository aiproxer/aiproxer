// Package observability builds safe, bounded token-accounting observations for future metrics and logging adapters.
package observability

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Plane identifies the usage plane represented by a count observation.
type Plane string

const (
	PlaneProviderBillable Plane = "provider_billable"
	PlaneClientVisible    Plane = "client_visible"
	PlaneProxyBillable    Plane = "proxy_billable"
)

// Source identifies where a token count came from.
type Source string

const (
	SourceProviderReported      Source = "provider_reported"
	SourceProviderCountAPI      Source = "provider_count_api"
	SourceLocalTokenizer        Source = "local_tokenizer"
	SourceAdministratorSupplied Source = "administrator_supplied"
	SourceTransformedRecomputed Source = "transformed_recomputed"
	SourceRequestScopedReuse    Source = "request_scoped_reuse"
)

// Authority identifies how strongly a count should be trusted.
type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityDelegated     Authority = "delegated"
	AuthorityEstimated     Authority = "estimated"
	AuthorityAdvisory      Authority = "advisory"
)

// Status identifies the outcome of a counting attempt.
type Status string

const (
	StatusSuccess     Status = "success"
	StatusUnavailable Status = "unavailable"
	StatusError       Status = "error"
)

// Reason is a bounded reason classification for fallback, unavailable, and error outcomes.
type Reason string

const (
	ReasonUnknown             Reason = "unknown"
	ReasonError               Reason = "error"
	ReasonCanceled            Reason = "canceled"
	ReasonDeadlineExceeded    Reason = "deadline_exceeded"
	ReasonProviderUnavailable Reason = "provider_unavailable"
	ReasonUnsupportedModel    Reason = "unsupported_model"
)

// Labels are bounded dimensions safe for metrics/logging adapters.
type Labels struct {
	Backend   string
	Model     string
	Plane     Plane
	Source    Source
	Authority Authority
}

// Input is the untrusted input used to build an Observation.
type Input struct {
	Labels            Labels
	Status            Status
	FallbackReason    string
	UnavailableReason string
	Err               error
	Duration          time.Duration
	OccurredAt        time.Time
}

// Observation is a content-free, sanitized count-attempt record.
type Observation struct {
	Labels            Labels
	Status            Status
	FallbackReason    Reason
	UnavailableReason Reason
	ErrorReason       Reason
	Duration          time.Duration
	OccurredAt        time.Time
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+[^\s;,"]+`),
	regexp.MustCompile(`(?i)bearer\s+[^\s;,"]+`),
	regexp.MustCompile(`(?i)cookie\s*:\s*[^;,"]+(?:;\s*[^;,"]+)*`),
	regexp.MustCompile(`(?i)(api[_-]?key|x-api-key|secret|token|password)\s*[:=]\s*[^\s;,"]+`),
	regexp.MustCompile(`(?i)sk-[a-z0-9_-]+`),
}

// NewObservation validates labels, classifies unsafe details, and returns a bounded observation.
func NewObservation(input Input) (Observation, error) {
	if err := validateLabels(input.Labels); err != nil {
		return Observation{}, err
	}
	if input.Duration < 0 {
		return Observation{}, fmt.Errorf("duration must not be negative")
	}

	status := input.Status
	if status == "" {
		status = StatusSuccess
	}
	if !validStatus(status) {
		return Observation{}, fmt.Errorf("unknown status %q", status)
	}

	fallbackReason := classifyText(input.FallbackReason)
	unavailableReason := classifyText(input.UnavailableReason)
	errorReason := Reason("")
	if input.Err != nil {
		status, unavailableReason, errorReason = classifyError(input.Err, status, unavailableReason)
	}

	occurredAt := input.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	return Observation{
		Labels:            input.Labels,
		Status:            status,
		FallbackReason:    fallbackReason,
		UnavailableReason: unavailableReason,
		ErrorReason:       errorReason,
		Duration:          input.Duration,
		OccurredAt:        occurredAt,
	}, nil
}

// Attributes returns a bounded string map suitable for future metrics/logging adapters.
func (o Observation) Attributes() map[string]string {
	attrs := map[string]string{
		"backend":   o.Labels.Backend,
		"model":     o.Labels.Model,
		"plane":     string(o.Labels.Plane),
		"source":    string(o.Labels.Source),
		"authority": string(o.Labels.Authority),
		"status":    string(o.Status),
	}
	if o.FallbackReason != "" {
		attrs["fallback_reason"] = string(o.FallbackReason)
	}
	if o.UnavailableReason != "" {
		attrs["unavailable_reason"] = string(o.UnavailableReason)
	}
	if o.ErrorReason != "" {
		attrs["error_reason"] = string(o.ErrorReason)
	}
	return attrs
}

func validateLabels(labels Labels) error {
	if strings.TrimSpace(labels.Backend) == "" {
		return fmt.Errorf("backend is required")
	}
	if strings.TrimSpace(labels.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if !validPlane(labels.Plane) {
		return fmt.Errorf("unknown plane %q", labels.Plane)
	}
	if !validSource(labels.Source) {
		return fmt.Errorf("unknown source %q", labels.Source)
	}
	if !validAuthority(labels.Authority) {
		return fmt.Errorf("unknown authority %q", labels.Authority)
	}
	return nil
}

func validPlane(plane Plane) bool {
	switch plane {
	case PlaneProviderBillable, PlaneClientVisible, PlaneProxyBillable:
		return true
	default:
		return false
	}
}

func validSource(source Source) bool {
	switch source {
	case SourceProviderReported, SourceProviderCountAPI, SourceLocalTokenizer, SourceAdministratorSupplied,
		SourceTransformedRecomputed, SourceRequestScopedReuse:
		return true
	default:
		return false
	}
}

func validAuthority(authority Authority) bool {
	switch authority {
	case AuthorityAuthoritative, AuthorityDelegated, AuthorityEstimated, AuthorityAdvisory:
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusSuccess, StatusUnavailable, StatusError:
		return true
	default:
		return false
	}
}

func classifyError(err error, status Status, unavailable Reason) (Status, Reason, Reason) {
	switch {
	case errors.Is(err, context.Canceled):
		return StatusUnavailable, ReasonCanceled, ReasonCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return StatusUnavailable, ReasonDeadlineExceeded, ReasonDeadlineExceeded
	}
	if status == StatusSuccess {
		status = StatusError
	}
	if unavailable == "" && status == StatusUnavailable {
		unavailable = ReasonError
	}
	return status, unavailable, ReasonError
}

func classifyText(value string) Reason {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	sanitized := value
	for _, pattern := range secretPatterns {
		sanitized = pattern.ReplaceAllString(sanitized, "[redacted]")
	}
	lower := strings.ToLower(sanitized)
	switch {
	case strings.Contains(lower, "deadline") || strings.Contains(lower, "timeout"):
		return ReasonDeadlineExceeded
	case strings.Contains(lower, "cancel"):
		return ReasonCanceled
	case strings.Contains(lower, "unsupported"):
		return ReasonUnsupportedModel
	case strings.Contains(lower, "unavailable"):
		return ReasonProviderUnavailable
	case strings.Contains(sanitized, "[redacted]"):
		return ReasonError
	default:
		return ReasonUnknown
	}
}
