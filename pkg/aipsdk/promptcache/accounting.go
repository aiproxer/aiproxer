package promptcache

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const MaxAccountingDedupeKeyBytes = 256

type AccountingSource string

const (
	AccountingSourceProviderReported AccountingSource = "provider_reported"
	AccountingSourceProviderCountAPI AccountingSource = "provider_count_api"
	AccountingSourceLocalEstimator   AccountingSource = "local_estimator"
	AccountingSourceLocalTokenizer   AccountingSource = "local_tokenizer"
)

type AccountingAuthority string

const (
	AccountingAuthorityAuthoritative AccountingAuthority = "authoritative"
	AccountingAuthorityEstimated     AccountingAuthority = "estimated"
	AccountingAuthorityDelegated     AccountingAuthority = "delegated"
	AccountingAuthorityAdvisory      AccountingAuthority = "advisory"
)

type AccountingPlane string

const AccountingPlaneProviderBillable AccountingPlane = "provider_billable"

type AccountingEvidence struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
	TotalTokens      *int64
	Presence         aipapi.UsagePresence
	Source           AccountingSource
	Authority        AccountingAuthority
	Plane            AccountingPlane
	DedupeKey        string
}

func (e AccountingEvidence) Validate() error {
	if e.Source == "" || e.Authority == "" || e.Plane == "" {
		return fmt.Errorf("%w: accounting metadata is required", ErrInvalid)
	}
	if strings.TrimSpace(e.DedupeKey) == "" || len(e.DedupeKey) > MaxAccountingDedupeKeyBytes {
		return fmt.Errorf("%w: accounting dedupe key", ErrInvalid)
	}
	values := []*int64{e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.ReasoningTokens, e.TotalTokens}
	present := []bool{e.Presence.InputTokens, e.Presence.OutputTokens, e.Presence.CacheReadTokens, e.Presence.CacheWriteTokens, e.Presence.ReasoningTokens, e.Presence.TotalTokens}
	any := false
	for i, value := range values {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: negative accounting usage", ErrInvalid)
		}
		if present[i] != (value != nil) {
			return fmt.Errorf("%w: accounting presence mismatch", ErrInvalid)
		}
		any = any || present[i]
	}
	if !any {
		return fmt.Errorf("%w: accounting usage is absent", ErrInvalid)
	}
	return nil
}

func (r RenewResponse) Validate() error {
	if err := r.Result.Validate(); err != nil {
		return err
	}
	if r.Accounting != nil {
		return r.Accounting.Validate()
	}
	return nil
}
