package aipapi

// UsagePlane identifies the accounting perspective for a usage delta.
type UsagePlane string

const (
	// UsagePlaneUnknown is the zero value for absent or unspecified usage plane metadata.
	UsagePlaneUnknown UsagePlane = ""
	// UsagePlaneProviderBillable is the provider-reported or provider-derived billable plane.
	UsagePlaneProviderBillable UsagePlane = "provider_billable"
	// UsagePlaneClientVisible is the usage visible to the upstream client protocol.
	UsagePlaneClientVisible UsagePlane = "client_visible"
	// UsagePlaneProxyBillable is usage billed by proxy policy rather than directly by a provider.
	UsagePlaneProxyBillable UsagePlane = "proxy_billable"
)

// UsageSource identifies where usage accounting metadata came from.
type UsageSource string

const (
	// UsageSourceUnknown is the zero value for absent or unspecified usage source metadata.
	UsageSourceUnknown          UsageSource = ""
	UsageSourceProviderReported UsageSource = "provider_reported"
	UsageSourceProviderCountAPI UsageSource = "provider_count_api"
	UsageSourceLocalTokenizer   UsageSource = "local_tokenizer"
	UsageSourceLocalEstimator   UsageSource = "local_estimator"
	UsageSourcePolicyReserved   UsageSource = "policy_reserved"
	UsageSourceProxyAdjusted    UsageSource = "proxy_adjusted"
	UsageSourceUnavailable      UsageSource = "unavailable"
)

// UsageAuthority describes how strongly callers may rely on a usage delta.
type UsageAuthority string

const (
	// UsageAuthorityUnknown is the zero value for absent or unspecified authority metadata.
	UsageAuthorityUnknown       UsageAuthority = ""
	UsageAuthorityAuthoritative UsageAuthority = "authoritative"
	UsageAuthorityDelegated     UsageAuthority = "delegated"
	UsageAuthorityEstimated     UsageAuthority = "estimated"
	UsageAuthorityAdvisory      UsageAuthority = "advisory"
	UsageAuthorityUnavailable   UsageAuthority = "unavailable"
)

// UsagePresence records which token counters were explicitly supplied by the
// usage source. A false field means the counter was omitted, not that it was
// explicitly reported as zero. The zero value preserves compatibility with
// legacy events that predate per-field presence metadata.
type UsagePresence struct {
	InputTokens      bool
	OutputTokens     bool
	CacheReadTokens  bool
	CacheWriteTokens bool
	ReasoningTokens  bool
	TotalTokens      bool
}

// Any reports whether at least one token counter was explicitly supplied.
func (p UsagePresence) Any() bool {
	return p.InputTokens || p.OutputTokens || p.CacheReadTokens ||
		p.CacheWriteTokens || p.ReasoningTokens || p.TotalTokens
}

// Union retains explicit presence from either usage candidate.
func (p UsagePresence) Union(other UsagePresence) UsagePresence {
	return UsagePresence{
		InputTokens:      p.InputTokens || other.InputTokens,
		OutputTokens:     p.OutputTokens || other.OutputTokens,
		CacheReadTokens:  p.CacheReadTokens || other.CacheReadTokens,
		CacheWriteTokens: p.CacheWriteTokens || other.CacheWriteTokens,
		ReasoningTokens:  p.ReasoningTokens || other.ReasoningTokens,
		TotalTokens:      p.TotalTokens || other.TotalTokens,
	}
}

// TokenizerRef records provider-neutral tokenizer metadata used to derive usage.
type TokenizerRef struct {
	Type      string
	ID        string
	Version   string
	Source    string
	ModelUsed string
}

// UsageAccountingMetadata annotates usage deltas without replacing legacy token totals.
type UsageAccountingMetadata struct {
	Plane     UsagePlane
	Source    UsageSource
	Authority UsageAuthority
	Tokenizer TokenizerRef
	// Provider identifiers are opaque, bounded correlation values captured by
	// an adapter. They are never credentials and do not grant runtime
	// attribution authority; the host still supplies the trusted B-leg/store.
	ProviderAccountKey string
	ProviderRequestID  string
	ProviderChargeID   string
	// ServiceContext is an optional provider-returned context lexeme (for
	// example a service tier or endpoint class). It is retained for host-side
	// evidence diagnostics and is never interpreted as a tariff.
	ServiceContext string
	// DedupeKey is an internal accounting correlation key. It is never encoded
	// by frontend adapters and is used only to apply provider evidence once.
	DedupeKey string
	// InputTokensExcludeCacheRead declares that cache reads are not contained
	// in the associated input quantity. The zero value retains the legacy
	// inclusive-input interpretation. This affects uncached-input projections,
	// not the original quantities, usage authority or tariff selection.
	//
	// The declaration covers cache reads only. A producer that also reports
	// its cache writes outside the input quantity declares that relation
	// separately with InputTokensExcludeCacheWrite.
	InputTokensExcludeCacheRead bool `json:",omitempty"`
	// InputTokensExcludeCacheWrite declares that cache writes are not
	// contained in the associated input quantity. The zero value means the
	// input quantity already includes the cache writes, which is the legacy
	// inclusive-input interpretation.
	//
	// The declaration is independent of InputTokensExcludeCacheRead: a
	// producer may exclude reads while including writes, exclude writes while
	// including reads, or exclude both.
	InputTokensExcludeCacheWrite bool `json:",omitempty"`
}

// ScopedUsageDelta carries usage for one accounting plane while preserving Event legacy totals.
type ScopedUsageDelta struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	UsagePresence    UsagePresence
	Accounting       UsageAccountingMetadata
}

func (m UsageAccountingMetadata) validate(field string) error {
	if err := validateStringField(field+".Plane", string(m.Plane), MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Source", string(m.Source), MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Authority", string(m.Authority), MaxRefStringBytes); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"ProviderAccountKey": m.ProviderAccountKey,
		"ProviderRequestID":  m.ProviderRequestID,
		"ProviderChargeID":   m.ProviderChargeID,
		"ServiceContext":     m.ServiceContext,
	} {
		if err := validateStringField(field+"."+name, value, MaxRefStringBytes); err != nil {
			return err
		}
	}
	return m.Tokenizer.validate(field + ".Tokenizer")
}

func (r TokenizerRef) validate(field string) error {
	if err := validateStringField(field+".Type", r.Type, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".ID", r.ID, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Version", r.Version, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".Source", r.Source, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField(field+".ModelUsed", r.ModelUsed, MaxRefStringBytes); err != nil {
		return err
	}
	return nil
}

func validateScopedUsage(scopes []ScopedUsageDelta) error {
	for i, scope := range scopes {
		if err := scope.Accounting.validate(fieldIndex("UsageScopes", i) + ".Accounting"); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a deep copy of e suitable for retention: every slice and
// pointed-to struct is independently owned, so the producer's backing arrays
// cannot mutate the retained event. It is the single event-clone
// implementation; cloneEvent delegates to it.
func (e Event) Clone() Event {
	out := e
	out.UsageScopes = cloneSlice(e.UsageScopes)
	out.Opaque = cloneSlice(e.Opaque)
	out.Reasoning = cloneReasoningPart(e.Reasoning)
	if e.Item != nil {
		item := cloneItemValue(*e.Item)
		out.Item = &item
	}
	out.Decision = cloneDecisionResult(e.Decision)
	return out
}

func cloneEvent(ev Event) Event { return ev.Clone() }

func cloneDecisionResult(in *DecisionResult) *DecisionResult {
	if in == nil {
		return nil
	}
	out := *in
	out.Answers = cloneSlice(in.Answers)
	for i := range out.Answers {
		out.Answers[i].Probabilities = cloneSlice(in.Answers[i].Probabilities)
		if in.Answers[i].Confidence != nil {
			conf := *in.Answers[i].Confidence
			out.Answers[i].Confidence = &conf
		}
	}
	return &out
}
